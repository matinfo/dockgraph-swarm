package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/dockgraph/dockgraph/collector"
)

// Remote proxy timeouts. Streams (SSE logs) have no overall deadline and end
// when the client disconnects; one-shot requests are bounded.
const (
	remoteDialTimeout    = 5 * time.Second
	remoteHeaderTimeout  = 15 * time.Second
	remoteRequestTimeout = 20 * time.Second
	localProbeTimeout    = 5 * time.Second
)

// maxRemoteResponseBytes caps a proxied one-shot agent response.
const maxRemoteResponseBytes = 16 << 20

// errAgentResponseTooLarge reports an agent response over the cap.
var errAgentResponseTooLarge = errors.New("agent response too large")

// limitedBody reads through a limit but closes the underlying body.
type limitedBody struct {
	io.Reader
	io.Closer
}

// errAgentAuth reports that an agent rejected the shared token.
var errAgentAuth = errors.New("agent rejected token (check DG_AGENT_TOKEN on server and agents)")

// RemoteContainerLocator finds the agent serving a container that runs on
// another swarm node. *collector.AgentPool implements it.
type RemoteContainerLocator interface {
	// LocateContainer returns the agent base URL (http://host:port) for
	// container id, or false when the container is local or unknown.
	LocateContainer(ctx context.Context, id string) (baseURL string, ok bool)
}

// AgentProxy forwards container inspect and log requests for containers on
// other swarm nodes to the agent running there.
type AgentProxy struct {
	Locator RemoteContainerLocator
	// Token is the bearer secret presented to agents.
	Token string
	// Transport defaults to a transport with dial and response-header
	// timeouts.
	Transport http.RoundTripper
}

// ServerOption customises NewServer.
type ServerOption func(*serverOptions)

type serverOptions struct {
	agentProxy *AgentProxy
}

// WithAgentProxy routes container inspect/logs for containers that are not
// on the local daemon to their node's agent.
func WithAgentProxy(p *AgentProxy) ServerOption {
	return func(o *serverOptions) { o.agentProxy = p }
}

func (p *AgentProxy) transport() http.RoundTripper {
	if p.Transport != nil {
		return p.Transport
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: remoteDialTimeout}).DialContext,
		ResponseHeaderTimeout: remoteHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
}

// wrap returns a handler that serves {id} locally when the local daemon
// knows the container, and otherwise proxies to the owning agent at
// suffix (e.g. "/logs") under /agent/v1/containers/{id}. stream disables the
// overall request deadline and the server write deadline for long-lived
// responses.
func (p *AgentProxy) wrap(local http.HandlerFunc, inspector ContainerInspector, suffix string, stream bool) http.HandlerFunc {
	if p == nil || p.Locator == nil {
		return local
	}
	rt := p.transport()
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validResourceName.MatchString(id) {
			local(w, r)
			return
		}

		probeCtx, cancel := context.WithTimeout(r.Context(), localProbeTimeout)
		_, err := inspector.ContainerInspect(probeCtx, id)
		cancel()
		if err == nil {
			local(w, r)
			return
		}

		base, ok := p.Locator.LocateContainer(r.Context(), id)
		if !ok {
			local(w, r)
			return
		}
		target, err := url.Parse(base)
		if err != nil || target.Host == "" {
			log.Printf("agent proxy: bad agent URL %q for %s", base, id)
			local(w, r)
			return
		}

		if stream {
			disableWriteDeadline(w)
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), remoteRequestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}

		rp := &httputil.ReverseProxy{
			Transport:     rt,
			FlushInterval: -1, // flush SSE events immediately
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = target.Scheme
				pr.Out.URL.Host = target.Host
				pr.Out.URL.Path = collector.AgentAPIPrefix + "/containers/" + url.PathEscape(id) + suffix
				pr.Out.URL.RawPath = ""
				pr.Out.URL.RawQuery = r.URL.RawQuery
				pr.Out.Host = target.Host
				// Never forward the browser's cookies or credentials.
				pr.Out.Header = http.Header{}
				pr.Out.Header.Set("Authorization", "Bearer "+p.Token)
				if accept := pr.In.Header.Get("Accept"); accept != "" {
					pr.Out.Header.Set("Accept", accept)
				}
			},
			// A rejected token is a server misconfiguration; don't surface
			// it as a 401, which the UI would treat as its own session.
			ModifyResponse: func(resp *http.Response) error {
				if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
					return errAgentAuth
				}
				// One-shot responses (inspect, log pages) are bounded; only
				// the SSE stream is unbounded by design.
				if !stream {
					if resp.ContentLength > maxRemoteResponseBytes {
						return errAgentResponseTooLarge
					}
					resp.Body = limitedBody{io.LimitReader(resp.Body, maxRemoteResponseBytes), resp.Body}
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				if r.Context().Err() != nil && stream {
					return // client went away
				}
				log.Printf("agent proxy %s%s: %v", id, suffix, err)
				jsonError(w, "agent unreachable", http.StatusBadGateway)
			},
		}
		rp.ServeHTTP(w, r)
	}
}
