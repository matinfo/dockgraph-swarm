package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
)

// stubLocator maps container IDs to agent base URLs.
type stubLocator struct {
	mu    sync.Mutex
	urls  map[string]string
	calls int
}

func (s *stubLocator) LocateContainer(_ context.Context, id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	u, ok := s.urls[id]
	return u, ok
}

// agentRecorder is a fake agent recording the last request it received.
type agentRecorder struct {
	mu      sync.Mutex
	path    string
	query   string
	auth    string
	cookie  string
	status  int
	body    string
	ctype   string
	blockCh chan struct{} // when set, stream one event then block until closed
}

func (a *agentRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.path, a.query = r.URL.Path, r.URL.RawQuery
	a.auth, a.cookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
	status, body, ctype, block := a.status, a.body, a.ctype, a.blockCh
	a.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
	if block != nil {
		w.(http.Flusher).Flush()
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}
}

func newProxiedServer(t *testing.T, agent *agentRecorder, locator *stubLocator) *httptest.Server {
	t.Helper()
	agentSrv := httptest.NewServer(agent)
	t.Cleanup(agentSrv.Close)
	for id := range locator.urls {
		locator.urls[id] = agentSrv.URL
	}
	docker := &stubDockerAPI{logger: &stubContainerLogger{}} // ContainerInspect always fails: nothing is local
	handler := NewServer(NewHub(), fstest.MapFS{"index.html": {Data: []byte("ok")}}, &stubHealth{}, nil, docker, nil, nil, nil, "swarm",
		WithAgentProxy(&AgentProxy{Locator: locator, Token: "tok"}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentProxyInspect(t *testing.T) {
	agent := &agentRecorder{body: `{"name":"shop_web.2.t2"}`, ctype: "application/json"}
	locator := &stubLocator{urls: map[string]string{"shop_web.2.t2": ""}}
	srv := newProxiedServer(t, agent, locator)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/containers/shop_web.2.t2", nil)
	req.Header.Set("Cookie", "dg_session=browser-secret")
	req.Header.Set("Authorization", "Bearer user-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(body) != `{"name":"shop_web.2.t2"}` {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if agent.path != "/agent/v1/containers/shop_web.2.t2" {
		t.Errorf("agent path %q", agent.path)
	}
	if agent.auth != "Bearer tok" {
		t.Errorf("agent auth %q, want server token", agent.auth)
	}
	if agent.cookie != "" {
		t.Errorf("browser cookie forwarded: %q", agent.cookie)
	}
}

func TestAgentProxyLogsHistoryKeepsQuery(t *testing.T) {
	agent := &agentRecorder{body: `{"lines":[]}`, ctype: "application/json"}
	locator := &stubLocator{urls: map[string]string{"abcdef123456": ""}}
	srv := newProxiedServer(t, agent, locator)

	resp, err := http.Get(srv.URL + "/api/containers/abcdef123456/logs/history?limit=5&before=2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if agent.path != "/agent/v1/containers/abcdef123456/logs/history" || agent.query != "limit=5&before=2026-01-01T00:00:00Z" {
		t.Errorf("agent got %s?%s", agent.path, agent.query)
	}
}

func TestAgentProxyLogsStreamFlushesAndCancels(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	agent := &agentRecorder{body: "data: {\"line\":\"hi\"}\n\n", ctype: "text/event-stream", blockCh: block}
	locator := &stubLocator{urls: map[string]string{"shop_web.2.t2": ""}}
	srv := newProxiedServer(t, agent, locator)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/containers/shop_web.2.t2/logs?tail=10", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), `"line":"hi"`) {
		t.Fatalf("first event not flushed: %q %v", buf[:n], err)
	}
	if agent.path != "/agent/v1/containers/shop_web.2.t2/logs" || agent.query != "tail=10" {
		t.Errorf("agent got %s?%s", agent.path, agent.query)
	}
}

func TestAgentProxyAgentAuthFailureIsBadGateway(t *testing.T) {
	agent := &agentRecorder{status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, ctype: "application/json"}
	locator := &stubLocator{urls: map[string]string{"shop_web.2.t2": ""}}
	srv := newProxiedServer(t, agent, locator)

	resp, err := http.Get(srv.URL + "/api/containers/shop_web.2.t2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
}

func TestAgentProxyUnreachableAgent(t *testing.T) {
	locator := &stubLocator{urls: map[string]string{"shop_web.2.t2": "http://127.0.0.1:1"}}
	proxy := &AgentProxy{Locator: locator, Token: "tok"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}", proxy.wrap(HandleContainerInspect(&stubContainerInspector{err: errNotFoundStub}), &stubContainerInspector{err: errNotFoundStub}, "", false))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/containers/shop_web.2.t2", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
}

func TestAgentProxyLocalAndUnknownServedLocally(t *testing.T) {
	locator := &stubLocator{urls: map[string]string{}}
	proxy := &AgentProxy{Locator: locator, Token: "tok"}

	// Local container: served locally, locator not consulted.
	local := &stubContainerInspector{result: validContainerInspectResponse()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}", proxy.wrap(HandleContainerInspect(local), local, "", false))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/containers/web", nil))
	if rec.Code != http.StatusOK || locator.calls != 0 {
		t.Errorf("local: status %d, locator calls %d", rec.Code, locator.calls)
	}

	// Unknown everywhere: the local handler answers 404.
	missing := &stubContainerInspector{result: containertypes.InspectResponse{}, err: errNotFoundStub}
	mux = http.NewServeMux()
	mux.HandleFunc("GET /api/containers/{id}", proxy.wrap(HandleContainerInspect(missing), missing, "", false))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/containers/ghost", nil))
	if rec.Code != http.StatusNotFound || locator.calls != 1 {
		t.Errorf("unknown: status %d, locator calls %d", rec.Code, locator.calls)
	}

	// Invalid IDs never reach the locator.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/containers/-bad", nil))
	if rec.Code != http.StatusBadRequest || locator.calls != 1 {
		t.Errorf("invalid: status %d, locator calls %d", rec.Code, locator.calls)
	}
}

func TestAgentProxyNilIsPassthrough(t *testing.T) {
	var p *AgentProxy
	called := false
	h := p.wrap(func(http.ResponseWriter, *http.Request) { called = true }, nil, "", false)
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil proxy must return the local handler")
	}
}

var errNotFoundStub = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "No such container" }
