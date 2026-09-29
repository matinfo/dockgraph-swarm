package collector

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/swarm"
)

const testAgentToken = "s3cret"

// stubResolver returns fixed addresses (host:port of httptest agents).
type stubResolver struct {
	mu    sync.Mutex
	addrs []string
	err   error
	hosts []string
}

func (r *stubResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	return r.addrs, r.err
}

func (r *stubResolver) set(addrs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = addrs
}

// fakeAgent serves /info and /stats like a real agent.
type fakeAgent struct {
	srv        *httptest.Server
	nodeID     string
	samples    []ContainerSample
	statsCalls atomic.Int32
}

func newFakeAgent(t *testing.T, nodeID string, samples ...ContainerSample) *fakeAgent {
	t.Helper()
	a := &fakeAgent{nodeID: nodeID, samples: samples}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testAgentToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case AgentAPIPrefix + "/info":
			_ = json.NewEncoder(w).Encode(AgentInfo{NodeID: a.nodeID, Hostname: "host-" + a.nodeID})
		case AgentAPIPrefix + "/stats":
			a.statsCalls.Add(1)
			_ = json.NewEncoder(w).Encode(AgentStats{NodeID: a.nodeID, Samples: a.samples})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAgent) addr() string { return strings.TrimPrefix(a.srv.URL, "http://") }

func taskSample(id, name, service, taskID string, cpu float64, mem uint64) ContainerSample {
	return ContainerSample{
		ID:   id,
		Name: name,
		Labels: map[string]string{
			swarmTaskIDLabel:      taskID,
			swarmServiceNameLabel: service,
		},
		Stats: ContainerStats{CPUPercent: cpu, MemUsage: mem, MemLimit: 1000},
	}
}

func newTestPool(res *stubResolver, localNode string, tasks TaskLister) *AgentPool {
	return NewAgentPool(AgentPoolConfig{
		Addr:           "tasks.agent",
		Port:           "7801",
		Token:          testAgentToken,
		LocalNodeID:    localNode,
		Resolver:       res,
		Tasks:          tasks,
		RequestTimeout: 2 * time.Second,
	})
}

func TestAgentPoolSamplesSkipsLocalNode(t *testing.T) {
	local := newFakeAgent(t, "node-local", taskSample("aaaaaaaaaaaa1", "shop_web.1.t1", "shop_web", "t1", 10, 100))
	remote := newFakeAgent(t, "node-remote", taskSample("bbbbbbbbbbbb2", "shop_web.2.t2", "shop_web", "t2", 20, 200))
	res := &stubResolver{addrs: []string{local.addr(), remote.addr()}}

	p := newTestPool(res, "node-local", nil)
	p.Refresh(context.Background())

	samples := p.Samples(context.Background())
	if len(samples) != 1 || samples[0].Name != "shop_web.2.t2" {
		t.Fatalf("expected only the remote sample, got %+v", samples)
	}
	if local.statsCalls.Load() != 0 {
		t.Errorf("local agent must not be polled for stats")
	}
	if res.hosts[0] != "tasks.agent" {
		t.Errorf("resolved %q, want tasks.agent", res.hosts[0])
	}
}

func TestAgentPoolRejectsBadToken(t *testing.T) {
	a := newFakeAgent(t, "node-1")
	p := NewAgentPool(AgentPoolConfig{
		Addr: "tasks.agent", Port: "7801", Token: "wrong",
		Resolver: &stubResolver{addrs: []string{a.addr()}},
	})
	p.Refresh(context.Background())
	if n := len(p.remoteAgents()); n != 0 {
		t.Fatalf("agent with rejected token must be dropped, got %d", n)
	}
}

func TestAgentPoolNoAgentsFallsBack(t *testing.T) {
	res := &stubResolver{err: errors.New("no such host")}
	p := newTestPool(res, "node-local", nil)
	p.Refresh(context.Background())
	if s := p.Samples(context.Background()); len(s) != 0 {
		t.Fatalf("expected no samples, got %v", s)
	}
	if _, ok := p.LocateContainer(context.Background(), "whatever"); ok {
		t.Error("nothing should be located without agents")
	}
}

func TestAgentPoolKeepsAgentsOnTransientDNSError(t *testing.T) {
	a1 := newFakeAgent(t, "node-1")
	res := &stubResolver{addrs: []string{a1.addr()}}
	p := newTestPool(res, "", nil)
	p.Refresh(context.Background())
	if n := len(p.remoteAgents()); n != 1 {
		t.Fatalf("want 1 agent, got %d", n)
	}

	res.err = &net.DNSError{Err: "i/o timeout", Name: "tasks.agent", IsTimeout: true}
	p.Refresh(context.Background())
	if n := len(p.remoteAgents()); n != 1 {
		t.Fatalf("transient DNS error must keep known agents, got %d", n)
	}

	res.err = &net.DNSError{Err: "no such host", Name: "tasks.agent", IsNotFound: true}
	res.addrs = nil
	p.Refresh(context.Background())
	if n := len(p.remoteAgents()); n != 0 {
		t.Fatalf("NXDOMAIN must drop agents, got %d", n)
	}
}

func TestAgentPoolReresolveDropsGoneAgents(t *testing.T) {
	a1 := newFakeAgent(t, "node-1")
	a2 := newFakeAgent(t, "node-2")
	res := &stubResolver{addrs: []string{a1.addr(), a2.addr()}}
	p := newTestPool(res, "", nil)
	p.Refresh(context.Background())
	if n := len(p.remoteAgents()); n != 2 {
		t.Fatalf("want 2 agents, got %d", n)
	}
	res.set([]string{a2.addr()})
	p.Refresh(context.Background())
	agents := p.remoteAgents()
	if _, ok := agents["node-2"]; len(agents) != 1 || !ok {
		t.Fatalf("want only node-2, got %v", agents)
	}
}

func TestAgentPoolPortFromAddr(t *testing.T) {
	res := &stubResolver{addrs: []string{"10.0.0.5"}}
	var gotHost string
	p := NewAgentPool(AgentPoolConfig{
		Addr: "tasks.dg_agent:9000", Port: "7801", Token: testAgentToken, Resolver: res,
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotHost = r.URL.Host
			return nil, errors.New("offline")
		})},
	})
	p.Refresh(context.Background())
	if res.hosts[0] != "tasks.dg_agent" || gotHost != "10.0.0.5:9000" {
		t.Errorf("resolved %q dialled %q", res.hosts[0], gotHost)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAgentPoolLocateContainer(t *testing.T) {
	remote := newFakeAgent(t, "node-remote", taskSample("bbbbbbbbbbbbbbbb", "shop_web.2.t2", "shop_web", "t2", 1, 1))
	local := newFakeAgent(t, "node-local")
	tasks := &stubDockerClient{tasks: []swarm.Task{
		{ID: "t9", NodeID: "node-remote", Status: swarm.TaskStatus{ContainerStatus: &swarm.ContainerStatus{ContainerID: "cccccccccccccccc"}}},
		{ID: "t8", NodeID: "node-local"},
		{ID: "t7", NodeID: "node-gone"},
	}}
	p := newTestPool(&stubResolver{addrs: []string{remote.addr(), local.addr()}}, "node-local", tasks)
	p.Refresh(context.Background())
	p.Samples(context.Background())

	want := "http://" + remote.addr()
	cases := []struct {
		id string
		ok bool
	}{
		{"bbbbbbbbbbbbbbbb", true},   // sampled ID
		{"bbbbbbbbbbbb", true},       // ID prefix
		{"shop_web.2.t2", true},      // sampled name
		{"t2", true},                 // sampled task ID
		{"cccccccccccc", true},       // stopped container via task list
		{"shop_api.1.t9", true},      // task container name via task list
		{"shop_api.1.t8", false},     // local node
		{"t7", false},                // node without agent
		{"unknown-container", false}, // not a task
	}
	for _, c := range cases {
		got, ok := p.LocateContainer(context.Background(), c.id)
		if ok != c.ok || (ok && got != want) {
			t.Errorf("LocateContainer(%q) = %q,%v want ok=%v", c.id, got, ok, c.ok)
		}
	}
}

func TestAgentPoolStartStop(t *testing.T) {
	a := newFakeAgent(t, "node-1")
	res := &stubResolver{addrs: []string{a.addr()}}
	p := NewAgentPool(AgentPoolConfig{
		Addr: "tasks.agent", Port: "7801", Token: testAgentToken, Resolver: res,
		ResolveInterval: 10 * time.Millisecond,
	})
	p.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		res.mu.Lock()
		n := len(res.hosts)
		res.mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pool did not re-resolve")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.Stop()
}

// remoteStub is a RemoteSampler returning fixed samples.
type remoteStub struct{ samples []ContainerSample }

func (r remoteStub) Samples(context.Context) []ContainerSample { return r.samples }

func TestStatsCollectorMergesRemoteWithoutDoubleCounting(t *testing.T) {
	stats := sampleStatsResponse(1_000_000, 10_000_000, 2)
	cli := &stubDockerClient{
		containers: containerSummaries("aaaaaaaaaaaa", "shop_web.1.t1", map[string]string{
			swarmTaskIDLabel: "t1", swarmServiceNameLabel: "shop_web",
		}),
		statsFn: func(string) (containerStatsReader, error) { return fakeStatsBody(stats), nil },
	}
	sc := NewStatsCollector(cli, time.Hour, 2)
	sc.SetRemote(remoteStub{samples: []ContainerSample{
		// Same container reported by the local agent: must be ignored.
		taskSample("aaaaaaaaaaaa", "shop_web.1.t1", "shop_web", "t1", 999, 999),
		taskSample("bbbbbbbbbbbb", "shop_web.2.t2", "shop_web", "t2", 5, 300),
	}})

	snap := sc.poll(context.Background())
	local := snap.Stats["shop_web.1.t1"]
	if local.CPUPercent == 999 {
		t.Fatal("remote duplicate overrode local sample")
	}
	agg, ok := snap.Stats["shop_web"]
	if !ok {
		t.Fatalf("missing service aggregate: %v", snap.Stats)
	}
	if agg.CPUPercent != local.CPUPercent+5 || agg.MemUsage != local.MemUsage+300 {
		t.Errorf("aggregate %+v, local %+v", agg, local)
	}
	if _, ok := snap.Stats["shop_web.2.t2"]; !ok {
		t.Error("missing remote task stats")
	}
}

func TestBuildStatsSnapshotAggregates(t *testing.T) {
	snap := BuildStatsSnapshot([]ContainerSample{
		taskSample("a", "svc.1.x", "svc", "x", 10, 100),
		{
			ID: "b", Name: "svc.2.y", Labels: map[string]string{swarmServiceNameLabel: "svc"},
			Stats: ContainerStats{CPUPercent: 5, CPUThrottled: 40, MemUsage: 50, MemLimit: 1000, NetRx: 7, PIDs: 3},
		},
		{ID: "c", Name: "standalone", Stats: ContainerStats{CPUPercent: 1}},
		// A container named like a service keeps its own entry.
		{ID: "d", Name: "other", Stats: ContainerStats{CPUPercent: 2}},
		taskSample("e", "other.1.z", "other", "z", 30, 30),
	})
	agg := snap.Stats["svc"]
	want := ContainerStats{CPUPercent: 15, CPUThrottled: 40, MemUsage: 150, MemLimit: 2000, NetRx: 7, PIDs: 3}
	if agg != want {
		t.Errorf("aggregate = %+v, want %+v", agg, want)
	}
	if snap.Stats["other"].CPUPercent != 2 {
		t.Errorf("container entry clobbered by aggregate: %+v", snap.Stats["other"])
	}
	if len(snap.Stats) != 6 {
		t.Errorf("want 6 entries, got %d: %v", len(snap.Stats), snap.Stats)
	}
}

func TestPollSamplesCarriesSwarmLabels(t *testing.T) {
	stats := sampleStatsResponse(1_000_000, 10_000_000, 2)
	cli := &stubDockerClient{
		containers: containerSummaries("aaaaaaaaaaaa", "shop_web.1.t1", map[string]string{
			swarmTaskIDLabel: "t1", swarmServiceNameLabel: "shop_web", "unrelated": "x",
		}),
		statsFn: func(string) (containerStatsReader, error) { return fakeStatsBody(stats), nil },
	}
	samples := PollSamples(context.Background(), cli, 2)
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	s := samples[0]
	if s.ID != "aaaaaaaaaaaa" || s.TaskID() != "t1" || s.ServiceName() != "shop_web" {
		t.Errorf("unexpected sample %+v", s)
	}
	if _, leaked := s.Labels["unrelated"]; leaked {
		t.Error("unrelated labels must not be copied")
	}
}

// containerStatsReader shortens the stats stub signature.
type containerStatsReader = containertypes.StatsResponseReader

// containerSummaries returns one running container with the given labels.
func containerSummaries(id, name string, labels map[string]string) []containertypes.Summary {
	return []containertypes.Summary{{ID: id, Names: []string{"/" + name}, State: StateRunning, Labels: labels}}
}
