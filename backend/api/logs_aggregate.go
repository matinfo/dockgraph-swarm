package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/dockgraph/dockgraph/collector"
)

// selfExcludeValue is the label value marking dockgraph's own container.
const selfExcludeValue = "true"

// aggregateLine is one log line tagged with its source container.
type aggregateLine struct {
	Container string `json:"container"`
	Stream    string `json:"stream"`
	Line      string `json:"line"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ContainerLister lists containers (used to enumerate log sources).
type ContainerLister interface {
	ContainerList(ctx context.Context, options containertypes.ListOptions) ([]containertypes.Summary, error)
}

// EventSubscriber streams Docker events (used to add/remove live followers).
type EventSubscriber interface {
	Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)
}

// containerDisplayName returns the human name without Docker's leading slash.
func containerDisplayName(c containertypes.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) >= 12 {
		return c.ID[:12]
	}
	return c.ID
}

// isSelf reports whether a container is dockgraph's own (excluded from logs).
func isSelf(labels map[string]string) bool {
	return labels[collector.SelfExcludeLabel] == selfExcludeValue
}

// logSource is one stream feeding the aggregate log view: a container, or
// (in swarm mode) a service whose logs cover all its tasks cluster-wide.
type logSource struct {
	id      string
	name    string
	open    logOpener
	service bool
}

// logScope selects which workloads the aggregate log view covers. An empty
// stack means all of them. In swarm mode services are log sources and task
// containers are skipped, since their service's logs already include them.
type logScope struct {
	stack    string
	services StackServiceAPI // nil outside swarm mode
}

// newLogScope reads the ?stack= filter for the request.
func newLogScope(r *http.Request, services StackServiceAPI) logScope {
	return logScope{stack: r.URL.Query().Get("stack"), services: services}
}

// includeContainer reports whether a container with these labels belongs in scope.
func (s logScope) includeContainer(labels map[string]string) bool {
	if isSelf(labels) {
		return false
	}
	if s.services != nil && collector.IsTaskContainer(labels) {
		return false
	}
	return s.stack == "" || collector.ProjectOf(labels) == s.stack
}

// listSources enumerates the running containers (and swarm services) in scope.
func (s logScope) listSources(ctx context.Context, lister ContainerLister, logger ContainerLogger) ([]logSource, error) {
	summaries, err := lister.ContainerList(ctx, containertypes.ListOptions{})
	if err != nil {
		return nil, err
	}
	var sources []logSource
	for _, c := range summaries {
		if !s.includeContainer(c.Labels) {
			continue
		}
		sources = append(sources, logSource{id: c.ID, name: containerDisplayName(c), open: logger.ContainerLogs})
	}

	if s.services != nil {
		services, err := s.listServices(ctx)
		if err != nil {
			return nil, err
		}
		sources = append(sources, services...)
	}
	return sources, nil
}

// listServices enumerates the non-self swarm services in scope.
func (s logScope) listServices(ctx context.Context) ([]logSource, error) {
	opts := swarm.ServiceListOptions{}
	if s.stack != "" {
		opts.Filters = filters.NewArgs(filters.Arg("label", collector.StackNamespaceLabel+"="+s.stack))
	}
	services, err := s.services.ServiceList(ctx, opts)
	if err != nil {
		return nil, err
	}
	var sources []logSource
	for _, svc := range services {
		if collector.IsServiceSelfExcluded(svc) {
			continue
		}
		if s.stack != "" && collector.ProjectOf(svc.Spec.Labels) != s.stack {
			continue
		}
		sources = append(sources, logSource{id: svc.ID, name: svc.Spec.Name, open: s.services.ServiceLogs, service: true})
	}
	return sources, nil
}

// collectHistory fetches up to `limit` recent lines (before `before`, if set) from
// every source in scope, tags each with its container or service name, and
// merge-sorts ascending by timestamp. One source failing is skipped
// (best-effort), not fatal.
func collectHistory(ctx context.Context, scope logScope, lister ContainerLister, logger ContainerLogger, before string, limit int) ([]aggregateLine, error) {
	sources, err := scope.listSources(ctx, lister, logger)
	if err != nil {
		return nil, err
	}

	var all []aggregateLine
	for _, src := range sources {
		lines, err := fetchLogHistory(ctx, src.open, src.id, src.service, before, limit)
		if err != nil {
			continue
		}
		for _, e := range lines {
			all = append(all, aggregateLine{Container: src.name, Stream: e.Stream, Line: e.Line, Timestamp: e.Timestamp})
		}
	}

	// RFC3339Nano UTC timestamps sort correctly as strings.
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	return all, nil
}

// HandleAggregateLogsHistory returns a handler for GET /api/logs/history.
// Returns a merged, time-sorted JSON page of log lines across all containers
// or, with ?stack=, only those of one compose project / swarm stack. Pass a
// non-nil services in swarm mode to read service logs cluster-wide.
func HandleAggregateLogsHistory(lister ContainerLister, logger ContainerLogger, services StackServiceAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1000 {
				limit = n
			}
		}
		before := r.URL.Query().Get("before")
		scope := newLogScope(r, services)
		if scope.stack != "" && !validResourceName.MatchString(scope.stack) {
			jsonError(w, "invalid stack", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		lines, err := collectHistory(ctx, scope, lister, logger, before, limit)
		if err != nil {
			jsonError(w, "failed to read logs", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"lines": lines})
	}
}

// logAggregator fans multiple per-container log followers into one channel.
type logAggregator struct {
	logger    ContainerLogger
	since     string
	out       chan aggregateLine
	mu        sync.Mutex
	followers map[string]context.CancelFunc
}

func newLogAggregator(logger ContainerLogger, since string) *logAggregator {
	return &logAggregator{
		logger:    logger,
		since:     since,
		out:       make(chan aggregateLine, 256),
		followers: make(map[string]context.CancelFunc),
	}
}

// add starts a follower for a container (no-op if already followed).
func (a *logAggregator) add(parent context.Context, id, name string) {
	a.addSource(parent, logSource{id: id, name: name, open: a.logger.ContainerLogs})
}

// addSource starts a follower for any log source (no-op if already followed).
func (a *logAggregator) addSource(parent context.Context, src logSource) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.followers[src.id]; ok {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	a.followers[src.id] = cancel
	go a.follow(ctx, src)
}

// remove cancels and forgets a container's follower.
func (a *logAggregator) remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel, ok := a.followers[id]; ok {
		cancel()
		delete(a.followers, id)
	}
}

// follow streams one source's logs, tagging and forwarding each line.
// A read error (container gone, cancelled) ends the follower without affecting others.
func (a *logAggregator) follow(ctx context.Context, src logSource) {
	opts := containertypes.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Tail:       "0",
	}
	if a.since != "" {
		opts.Since = a.since
	}
	rc, err := src.open(ctx, src.id, opts)
	if err != nil {
		return
	}
	defer rc.Close()

	dlr := &dockerLogReader{reader: rc}
	for {
		streamType, payload, err := dlr.next()
		if err != nil {
			return
		}
		scanner := bufio.NewScanner(bytes.NewReader(payload))
		for scanner.Scan() {
			e := parseLogEntry(streamType, scanner.Text())
			select {
			case a.out <- aggregateLine{Container: src.name, Stream: e.Stream, Line: e.Line, Timestamp: e.Timestamp}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// logEventsFilter limits the event stream to container lifecycle events, plus
// service events in swarm mode.
func logEventsFilter(swarmMode bool) filters.Args {
	f := filters.NewArgs()
	f.Add("type", string(events.ContainerEventType))
	if swarmMode {
		f.Add("type", string(events.ServiceEventType))
	}
	return f
}

// HandleAggregateLogs returns a handler for GET /api/logs.
// Streams every non-self container's logs as merged SSE, adding/removing
// followers as containers start/die. ?stack= limits it to one compose project
// or swarm stack; in swarm mode (non-nil services) service logs are followed
// cluster-wide instead of the local task containers.
func HandleAggregateLogs(lister ContainerLister, logger ContainerLogger, eventsSub EventSubscriber, services StackServiceAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		disableWriteDeadline(w)
		scope := newLogScope(r, services)
		if scope.stack != "" && !validResourceName.MatchString(scope.stack) {
			jsonError(w, "invalid stack", http.StatusBadRequest)
			return
		}
		flusher.Flush()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		agg := newLogAggregator(logger, r.URL.Query().Get("since"))

		// Seed followers from the currently running, in-scope containers.
		if summaries, err := lister.ContainerList(ctx, containertypes.ListOptions{}); err == nil {
			for _, c := range summaries {
				if !scope.includeContainer(c.Labels) || c.State != stateRunning {
					continue
				}
				agg.add(ctx, c.ID, containerDisplayName(c))
			}
		}
		addServices := func() {
			if scope.services == nil {
				return
			}
			sources, err := scope.listServices(ctx)
			if err != nil {
				return
			}
			for _, src := range sources {
				agg.addSource(ctx, src)
			}
		}
		addServices()

		// React to lifecycle: add followers on container start / service
		// create, remove them on stop/die/remove. Container events carry the
		// container's labels as attributes, so the scope applies directly.
		msgCh, errCh := eventsSub.Events(ctx, events.ListOptions{Filters: logEventsFilter(scope.services != nil)})
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case err := <-errCh:
					if err != nil {
						return
					}
				case msg, ok := <-msgCh:
					if !ok {
						return
					}
					if msg.Type == events.ServiceEventType {
						switch string(msg.Action) {
						case "create":
							addServices()
						case "remove":
							agg.remove(msg.Actor.ID)
						}
						continue
					}
					switch string(msg.Action) {
					case "start":
						if scope.includeContainer(msg.Actor.Attributes) {
							agg.add(ctx, msg.Actor.ID, msg.Actor.Attributes["name"])
						}
					case "die", "destroy", "stop", "kill":
						agg.remove(msg.Actor.ID)
					}
				}
			}
		}()

		// Writer loop: drain merged lines to SSE until the client disconnects.
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-agg.out:
				data, _ := json.Marshal(line)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
