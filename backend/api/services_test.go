package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/swarm"
	systemtypes "github.com/docker/docker/api/types/system"
	"github.com/dockgraph/dockgraph/collector"
)

func u64(n uint64) *uint64 { return &n }

// swarmStub is a two-service "shop" stack plus a service in another stack.
func swarmStub() *stubDockerAPI {
	web := swarm.Service{ID: "svc-web"}
	web.Spec.Name = "shop_web"
	web.Spec.Labels = map[string]string{collector.StackNamespaceLabel: "shop"}
	web.Spec.Mode.Replicated = &swarm.ReplicatedService{Replicas: u64(2)}
	web.Spec.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{
		Image:  "nginx:1.25@sha256:abc",
		Env:    []string{"MODE=prod", "DB_PASSWORD=hunter2"},
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "shop_data", Target: "/data"}},
	}
	web.Spec.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "net1", Aliases: []string{"web"}}}
	web.Endpoint.Ports = []swarm.PortConfig{{TargetPort: 80, PublishedPort: 8080, Protocol: "tcp", PublishMode: "ingress"}}

	db := swarm.Service{ID: "svc-db"}
	db.Spec.Name = "shop_db"
	db.Spec.Labels = map[string]string{collector.StackNamespaceLabel: "shop"}

	blog := swarm.Service{ID: "svc-blog"}
	blog.Spec.Name = "blog_app"
	blog.Spec.Labels = map[string]string{collector.StackNamespaceLabel: "blog"}

	node := swarm.Node{ID: "n1"}
	node.Description.Hostname = "manager-1"

	return &stubDockerAPI{
		logger:   &stubContainerLogger{},
		services: []swarm.Service{web, db, blog},
		tasks: []swarm.Task{
			{ID: "t1", ServiceID: "svc-web", NodeID: "n1", Slot: 1, DesiredState: swarm.TaskStateRunning,
				Status: swarm.TaskStatus{State: swarm.TaskStateRunning}},
			{ID: "t2", ServiceID: "svc-web", NodeID: "n1", Slot: 2, DesiredState: swarm.TaskStateRunning,
				Status: swarm.TaskStatus{State: swarm.TaskStateStarting}},
			{ID: "t9", ServiceID: "svc-blog", NodeID: "n1", Slot: 1, DesiredState: swarm.TaskStateRunning,
				Status: swarm.TaskStatus{State: swarm.TaskStateRunning}},
		},
		nodes:    []swarm.Node{node},
		networks: map[string]string{"net1": "shop_front"},
		serviceLogs: map[string]string{
			"svc-web":  frameStr("2026-06-11T10:00:02.000000000Z web-line"),
			"svc-db":   frameStr("2026-06-11T10:00:01.000000000Z db-line"),
			"svc-blog": frameStr("2026-06-11T10:00:00.000000000Z blog-line"),
			"shop_web": frameStr("2026-06-11T10:00:02.000000000Z web-line"),
		},
	}
}

func newSwarmTestServer(stub *stubDockerAPI, mode string) *httptest.Server {
	fs := fstest.MapFS{"index.html": {Data: []byte("ok")}}
	return httptest.NewServer(NewServer(NewHub(), fs, &stubHealth{}, nil, stub, nil, nil, nil, mode))
}

func TestHandleServiceInspect(t *testing.T) {
	srv := newSwarmTestServer(swarmStub(), collector.ModeSwarm)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/services/shop_web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	var body struct {
		ID       string                 `json:"id"`
		Name     string                 `json:"name"`
		Stack    string                 `json:"stack"`
		Status   string                 `json:"status"`
		Mode     string                 `json:"mode"`
		Replicas collector.ReplicaCount `json:"replicas"`
		Tasks    []collector.TaskInfo   `json:"tasks"`
		Env      []map[string]string    `json:"env"`
		Networks []map[string]any       `json:"networks"`
		Mounts   []map[string]any       `json:"mounts"`
		Ports    []map[string]any       `json:"ports"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "svc-web" || body.Name != "shop_web" || body.Stack != "shop" || body.Mode != "replicated" {
		t.Errorf("unexpected identity: %+v", body)
	}
	if body.Replicas != (collector.ReplicaCount{Running: 1, Desired: 2}) || body.Status != "degraded" {
		t.Errorf("replicas/status = %+v / %s", body.Replicas, body.Status)
	}
	if len(body.Tasks) != 2 || body.Tasks[0].NodeHostname != "manager-1" {
		t.Errorf("tasks = %+v (only this service's tasks, with hostnames)", body.Tasks)
	}
	for _, e := range body.Env {
		if e["key"] == "DB_PASSWORD" && e["value"] == "hunter2" {
			t.Error("secret env value not masked")
		}
	}
	if len(body.Networks) != 1 || body.Networks[0]["name"] != "shop_front" {
		t.Errorf("networks = %+v", body.Networks)
	}
	if len(body.Mounts) != 1 || body.Mounts[0]["name"] != "shop_data" {
		t.Errorf("mounts = %+v", body.Mounts)
	}
	if len(body.Ports) != 1 || body.Ports[0]["host"] != float64(8080) {
		t.Errorf("ports = %+v", body.Ports)
	}
}

func TestHandleServiceInspectErrors(t *testing.T) {
	srv := newSwarmTestServer(swarmStub(), collector.ModeSwarm)
	defer srv.Close()

	tests := []struct {
		path string
		want int
	}{
		{"/api/services/missing", http.StatusNotFound},
		{"/api/services/-bad", http.StatusBadRequest},
	}
	for _, tt := range tests {
		resp, err := http.Get(srv.URL + tt.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s: status %d, want %d", tt.path, resp.StatusCode, tt.want)
		}
	}
}

func TestServiceRoutesOnlyInSwarmMode(t *testing.T) {
	srv := newSwarmTestServer(swarmStub(), collector.ModeStandalone)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/services/shop_web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Falls through to the SPA handler rather than the JSON inspect handler.
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Errorf("service route should not be registered in standalone mode (content-type %s)", ct)
	}
}

func TestHandleServiceLogsHistory(t *testing.T) {
	srv := newSwarmTestServer(swarmStub(), collector.ModeSwarm)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/services/shop_web/logs/history")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Lines []logEntry `json:"lines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 1 || body.Lines[0].Line != "web-line" {
		t.Errorf("lines = %+v", body.Lines)
	}
}

func TestHandleServiceLogsStream(t *testing.T) {
	h := HandleServiceLogs(swarmStub())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/services/shop_web/logs", nil).WithContext(ctx)
	req.SetPathValue("id", "shop_web")
	rec := httptest.NewRecorder()
	h(rec, req)
	if !strings.Contains(rec.Body.String(), "web-line") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

// --- aggregate logs with ?stack= ---

func decodeAggregate(t *testing.T, rec *httptest.ResponseRecorder) []aggregateLine {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Lines []aggregateLine `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Lines
}

func sourcesOf(lines []aggregateLine) map[string]bool {
	m := map[string]bool{}
	for _, l := range lines {
		m[l.Container] = true
	}
	return m
}

func TestAggregateLogsHistoryStackStandalone(t *testing.T) {
	lister := mockLister{summaries: []containertypes.Summary{
		{ID: "a", Names: []string{"/shop-web-1"}, Labels: map[string]string{"com.docker.compose.project": "shop"}},
		{ID: "b", Names: []string{"/blog-app-1"}, Labels: map[string]string{"com.docker.compose.project": "blog"}},
		{ID: "c", Names: []string{"/loose"}},
	}}
	logger := mockLogger{byID: map[string]string{
		"a": frameStr("2026-06-11T10:00:02.000000000Z a"),
		"b": frameStr("2026-06-11T10:00:01.000000000Z b"),
		"c": frameStr("2026-06-11T10:00:00.000000000Z c"),
	}}
	h := HandleAggregateLogsHistory(lister, logger, nil)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/logs/history?stack=shop", nil))
	got := sourcesOf(decodeAggregate(t, rec))
	if len(got) != 1 || !got["shop-web-1"] {
		t.Errorf("sources = %v", got)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/logs/history", nil))
	if got := sourcesOf(decodeAggregate(t, rec)); len(got) != 3 {
		t.Errorf("no stack filter should include all, got %v", got)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/logs/history?stack=../x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid stack: status %d", rec.Code)
	}
}

func TestAggregateLogsHistoryStackSwarm(t *testing.T) {
	stub := swarmStub()
	lister := mockLister{summaries: []containertypes.Summary{
		// Local task container of shop_web: covered by the service logs.
		{ID: "task", Names: []string{"/shop_web.1.x"}, Labels: map[string]string{
			collector.StackNamespaceLabel: "shop", "com.docker.swarm.task.id": "t1",
		}},
		{ID: "side", Names: []string{"/shop-helper"}, Labels: map[string]string{collector.StackNamespaceLabel: "shop"}},
	}}
	logger := mockLogger{byID: map[string]string{
		"task": frameStr("2026-06-11T10:00:03.000000000Z dup"),
		"side": frameStr("2026-06-11T10:00:04.000000000Z helper"),
	}}
	h := HandleAggregateLogsHistory(lister, logger, stub)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/logs/history?stack=shop", nil))
	lines := decodeAggregate(t, rec)
	got := sourcesOf(lines)
	want := []string{"shop_web", "shop_db", "shop-helper"}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing source %s in %v", w, got)
		}
	}
	// Merge-sorted ascending by timestamp.
	if lines[0].Container != "shop_db" {
		t.Errorf("order: %+v", lines)
	}
}

func TestLogScopeIncludeContainer(t *testing.T) {
	task := map[string]string{collector.StackNamespaceLabel: "shop", "com.docker.swarm.task.id": "t"}
	self := map[string]string{collector.SelfExcludeLabel: "true"}
	tests := []struct {
		name   string
		scope  logScope
		labels map[string]string
		want   bool
	}{
		{"self excluded", logScope{}, self, false},
		{"standalone keeps task containers", logScope{}, task, true},
		{"swarm drops task containers", logScope{services: swarmStub()}, task, false},
		{"stack match", logScope{stack: "shop"}, map[string]string{"com.docker.compose.project": "shop"}, true},
		{"stack mismatch", logScope{stack: "shop"}, map[string]string{"com.docker.compose.project": "blog"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.includeContainer(tt.labels); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleAggregateLogsStreamsStackServices(t *testing.T) {
	stub := swarmStub()
	// Follow-style service logs: replay then block until cancelled.
	follow := &followServiceStub{stubDockerAPI: stub}
	h := HandleAggregateLogs(mockLister{}, mockLogger{}, noEvents{}, follow)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?stack=blog", nil).WithContext(ctx)
	rec := newSyncWriter()

	done := make(chan struct{})
	go func() { h(rec, req); close(done) }()

	deadline := time.After(2 * time.Second)
	for !strings.Contains(rec.body(), "blog-line") {
		select {
		case <-deadline:
			t.Fatalf("did not stream service line; body=%q", rec.body())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if strings.Contains(rec.body(), "web-line") {
		t.Error("other stack's service logs leaked into the stream")
	}
}

// followServiceStub keeps service log streams open like a real follow.
type followServiceStub struct{ *stubDockerAPI }

func (f *followServiceStub) ServiceLogs(ctx context.Context, id string, _ containertypes.LogsOptions) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(f.serviceLogs[id]))
		<-ctx.Done()
		pw.Close()
	}()
	return pr, nil
}

// --- stats history ?stack= ---

func TestHandleStatsHistoryStackFilter(t *testing.T) {
	h := collector.NewStatsHistory(24 * time.Hour)
	h.Record(time.Now().Add(-time.Minute), collector.StatsSnapshot{Stats: map[string]collector.ContainerStats{
		"shop-web-1": {CPUPercent: 1},
		"blog-app-1": {CPUPercent: 2},
	}})
	lister := mockLister{summaries: []containertypes.Summary{
		{Names: []string{"/shop-web-1"}, Labels: map[string]string{"com.docker.compose.project": "shop"}},
		{Names: []string{"/blog-app-1"}, Labels: map[string]string{collector.StackNamespaceLabel: "blog"}},
	}}
	handler := HandleStatsHistory(h, lister, nil)

	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"shop-web-1", "blog-app-1"}},
		{"&stack=shop", []string{"shop-web-1"}},
		{"&stack=blog", []string{"blog-app-1"}},
		{"&stack=none", nil},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/stats/history?range=5m"+tt.query, nil))
		var body struct {
			Containers map[string]any `json:"containers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Containers) != len(tt.want) {
			t.Errorf("%q: containers = %v, want %v", tt.query, body.Containers, tt.want)
			continue
		}
		for _, name := range tt.want {
			if _, ok := body.Containers[name]; !ok {
				t.Errorf("%q: missing %s", tt.query, name)
			}
		}
	}

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/stats/history?stack=a/b", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid stack: status %d", rec.Code)
	}
}

func TestHandleStatsHistoryStackFilterSwarm(t *testing.T) {
	h := collector.NewStatsHistory(24 * time.Hour)
	h.Record(time.Now().Add(-time.Minute), collector.StatsSnapshot{Stats: map[string]collector.ContainerStats{
		"shop_web":          {CPUPercent: 3}, // service aggregate
		"shop_web.2.remote": {CPUPercent: 2}, // task on another node
		"shop_webx":         {CPUPercent: 9}, // unrelated look-alike
		"blog_app":          {CPUPercent: 1},
	}})
	services := &stubDockerAPI{services: []swarm.Service{
		{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "shop_web", Labels: map[string]string{collector.StackNamespaceLabel: "shop"}}}},
		{ID: "s2", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "blog_app", Labels: map[string]string{collector.StackNamespaceLabel: "blog"}}}},
	}}
	handler := HandleStatsHistory(h, mockLister{}, services)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/stats/history?range=5m&stack=shop", nil))
	var body struct {
		Containers map[string]any `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Containers) != 2 || body.Containers["shop_web"] == nil || body.Containers["shop_web.2.remote"] == nil {
		t.Errorf("containers = %v, want shop_web and shop_web.2.remote", body.Containers)
	}
}

// --- system info ---

func TestHandleSystemInfoSwarm(t *testing.T) {
	tests := []struct {
		name      string
		info      systemtypes.Info
		mode      string
		wantSwarm bool
	}{
		{"standalone", systemtypes.Info{}, "standalone", false},
		{"swarm manager", systemtypes.Info{Swarm: swarm.Info{
			LocalNodeState: swarm.LocalNodeStateActive, NodeID: "n1", Managers: 1, Nodes: 3,
		}}, "swarm", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := HandleSystemInfo(&stubSystemInfoProvider{info: tt.info}, tt.mode)
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["mode"] != tt.mode {
				t.Errorf("mode = %v", body["mode"])
			}
			sw, _ := body["swarm"].(map[string]any)
			if (sw != nil) != tt.wantSwarm {
				t.Fatalf("swarm = %v", body["swarm"])
			}
			if tt.wantSwarm && (sw["nodeId"] != "n1" || sw["managers"] != float64(1) || sw["nodes"] != float64(3)) {
				t.Errorf("swarm = %v", sw)
			}
		})
	}
}
