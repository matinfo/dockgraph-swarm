package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/swarm"
)

// AgentAPIPrefix is the path prefix of every per-node agent endpoint.
const AgentAPIPrefix = "/agent/v1"

// AgentInfo is the payload of GET /agent/v1/info: the swarm node an agent
// runs on.
type AgentInfo struct {
	NodeID   string `json:"nodeId"`
	Hostname string `json:"hostname"`
}

// AgentStats is the payload of GET /agent/v1/stats: the latest samples of
// the containers running on the agent's node.
type AgentStats struct {
	NodeID  string            `json:"nodeId"`
	Samples []ContainerSample `json:"samples"`
}

// Agent pool defaults.
const (
	defaultAgentResolveInterval = 30 * time.Second
	defaultAgentRequestTimeout  = 5 * time.Second
	agentResolveTimeout         = 5 * time.Second
	agentTaskLookupTimeout      = 5 * time.Second
	// maxAgentResponseBytes bounds a decoded agent response.
	maxAgentResponseBytes = 16 << 20
)

// HostResolver resolves a DNS name to addresses; *net.Resolver satisfies it.
type HostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// TaskLister lists swarm tasks; used to find which node runs a container.
type TaskLister interface {
	TaskList(ctx context.Context, options swarm.TaskListOptions) ([]swarm.Task, error)
}

// AgentPoolConfig configures an AgentPool.
type AgentPoolConfig struct {
	// Addr is the DNS name resolving to every agent task (e.g. tasks.agent),
	// optionally with ":port" overriding Port.
	Addr string
	// Port is the agent HTTP port used when Addr carries none.
	Port string
	// Token is the bearer secret sent to agents.
	Token string
	// LocalNodeID is this server's swarm node. Its agent is not polled for
	// stats (the local stats worker already covers it) and its containers
	// are never proxied.
	LocalNodeID string
	// Resolver defaults to net.DefaultResolver. A resolved entry that already
	// has a port (host:port) is used as is.
	Resolver HostResolver
	// Tasks is used to locate stopped or not-yet-sampled task containers.
	// Optional.
	Tasks TaskLister
	// Client defaults to an http.Client with RequestTimeout.
	Client *http.Client
	// ResolveInterval defaults to 30s.
	ResolveInterval time.Duration
	// RequestTimeout bounds each /info and /stats call (default 5s).
	RequestTimeout time.Duration
}

// agentEndpoint is one discovered agent.
type agentEndpoint struct {
	addr     string // host:port
	hostname string
}

// AgentPool discovers per-node agents through DNS, maps each to its swarm
// node, polls their stats and locates containers running on other nodes.
type AgentPool struct {
	cfg AgentPoolConfig

	mu     sync.RWMutex
	agents map[string]agentEndpoint // nodeID → agent
	// owners maps container IDs, names and task IDs seen in the last remote
	// stats poll to the node that reported them.
	owners map[string]string
	// reporting lists the hostnames of agents that answered the last remote
	// stats poll, even with no samples (e.g. only the agent runs there).
	reporting []string

	lastCount int // agents found by the previous refresh; -1 before the first
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewAgentPool creates a pool; call Start to begin discovery.
func NewAgentPool(cfg AgentPoolConfig) *AgentPool {
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultAgentRequestTimeout
	}
	if cfg.Client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		cfg.Client = &http.Client{Timeout: cfg.RequestTimeout, Transport: transport}
	}
	if cfg.ResolveInterval <= 0 {
		cfg.ResolveInterval = defaultAgentResolveInterval
	}
	return &AgentPool{
		cfg:       cfg,
		agents:    make(map[string]agentEndpoint),
		owners:    make(map[string]string),
		lastCount: -1,
	}
}

// Start resolves agents immediately, then every ResolveInterval until ctx is
// cancelled or Stop is called.
func (p *AgentPool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.Refresh(ctx)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.cfg.ResolveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Refresh(ctx)
			}
		}
	}()
}

// Stop ends discovery and waits for the background loop to exit.
func (p *AgentPool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

// Refresh re-resolves the agent DNS name and queries every address's /info
// to map nodes to agents. Unreachable agents are dropped until they answer.
func (p *AgentPool) Refresh(ctx context.Context) {
	host, port := p.cfg.Addr, p.cfg.Port
	if h, pt, err := net.SplitHostPort(p.cfg.Addr); err == nil {
		host, port = h, pt
	}

	rctx, cancel := context.WithTimeout(ctx, agentResolveTimeout)
	ips, err := p.cfg.Resolver.LookupHost(rctx, host)
	cancel()
	if err != nil && ctx.Err() != nil {
		return
	}
	// A transient resolver failure (timeout, SERVFAIL) must not drop every
	// known agent until the next refresh; only an authoritative "no such
	// host" (no agent task running) empties the pool.
	if err != nil && !isDNSNotFound(err) {
		log.Printf("agents: resolving %s: %v; keeping %d known agent(s)", host, err, len(p.remoteAgents()))
		return
	}

	addrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		if _, _, splitErr := net.SplitHostPort(ip); splitErr == nil {
			addrs = append(addrs, ip)
		} else {
			addrs = append(addrs, net.JoinHostPort(ip, port))
		}
	}
	sort.Strings(addrs)

	infos := make([]*AgentInfo, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var info AgentInfo
			if getErr := p.getJSON(ctx, addr, "/info", &info); getErr != nil {
				log.Printf("agents: %s /info: %v", addr, getErr)
				return
			}
			if info.NodeID == "" {
				log.Printf("agents: %s reported no swarm node ID", addr)
				return
			}
			infos[i] = &info
		}()
	}
	wg.Wait()

	agents := make(map[string]agentEndpoint, len(addrs))
	for i, info := range infos {
		if info == nil {
			continue
		}
		if _, dup := agents[info.NodeID]; dup {
			continue
		}
		agents[info.NodeID] = agentEndpoint{addr: addrs[i], hostname: info.Hostname}
	}

	p.mu.Lock()
	p.agents = agents
	changed := len(agents) != p.lastCount
	p.lastCount = len(agents)
	p.mu.Unlock()

	if changed {
		if len(agents) == 0 {
			reason := ""
			if err != nil {
				reason = fmt.Sprintf(" (%v)", err)
			}
			log.Printf("agents: 0 via %s%s; stats and logs limited to this node", host, reason)
		} else {
			log.Printf("agents: %d via %s", len(agents), host)
		}
	}
}

// isDNSNotFound reports whether err is an authoritative "host not found".
// Errors that are not *net.DNSError (e.g. from a stub resolver) count as
// not found so the pool never keeps agents it cannot vouch for.
func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return true
}

// remoteAgents returns the discovered agents except the local node's.
func (p *AgentPool) remoteAgents() map[string]agentEndpoint {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]agentEndpoint, len(p.agents))
	for nodeID, a := range p.agents {
		if nodeID != p.cfg.LocalNodeID {
			out[nodeID] = a
		}
	}
	return out
}

// Samples polls /stats on every remote agent concurrently and returns their
// samples, each stamped with the agent's node ID and hostname. Agents that
// fail or time out are skipped for this round. It also
// records which node owns each sampled container for LocateContainer.
func (p *AgentPool) Samples(ctx context.Context) []ContainerSample {
	agents := p.remoteAgents()
	if len(agents) == 0 {
		p.mu.Lock()
		p.owners = make(map[string]string)
		p.reporting = nil
		p.mu.Unlock()
		return nil
	}

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		samples   []ContainerSample
		owners    = make(map[string]string)
		reporting []string
	)
	for nodeID, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var resp AgentStats
			if err := p.getJSON(ctx, a.addr, "/stats", &resp); err != nil {
				if ctx.Err() == nil {
					log.Printf("agents: %s (node %s) /stats: %v", a.addr, a.hostname, err)
				}
				return
			}
			mu.Lock()
			defer mu.Unlock()
			reporting = append(reporting, a.hostname)
			for _, s := range resp.Samples {
				if s.ID == "" || s.Name == "" {
					continue
				}
				// Attribute the sample to the node the pool discovered the
				// agent on; whatever the agent reported is overridden.
				s.NodeID = nodeID
				s.NodeHostname = a.hostname
				samples = append(samples, s)
				owners[s.ID] = nodeID
				owners[s.Name] = nodeID
				if tid := s.TaskID(); tid != "" {
					owners[tid] = nodeID
				}
			}
		}()
	}
	wg.Wait()

	sort.Strings(reporting)
	p.mu.Lock()
	p.owners = owners
	p.reporting = reporting
	p.mu.Unlock()
	return samples
}

// ReportingNodes returns the hostnames of the remote agents that answered
// the last Samples round, including those that reported no containers.
func (p *AgentPool) ReportingNodes() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.reporting...)
}

// LocateContainer returns the base URL (http://host:port) of the agent on
// the node running container id (an ID, ID prefix, name or task ID), when
// that node is not the local one and has a known agent.
func (p *AgentPool) LocateContainer(ctx context.Context, id string) (string, bool) {
	nodeID := p.ownerFromSamples(id)
	if nodeID == "" {
		nodeID = p.ownerFromTasks(ctx, id)
	}
	if nodeID == "" || nodeID == p.cfg.LocalNodeID {
		return "", false
	}
	p.mu.RLock()
	a, ok := p.agents[nodeID]
	p.mu.RUnlock()
	if !ok {
		return "", false
	}
	return "http://" + a.addr, true
}

// ownerFromSamples looks id up in the last remote stats poll.
func (p *AgentPool) ownerFromSamples(id string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if nodeID, ok := p.owners[id]; ok {
		return nodeID
	}
	if len(id) >= 12 {
		for key, nodeID := range p.owners {
			if strings.HasPrefix(key, id) {
				return nodeID
			}
		}
	}
	return ""
}

// ownerFromTasks finds the task running container id through the swarm
// task list. Task container names are {service}.{slot|node}.{taskID}.
func (p *AgentPool) ownerFromTasks(ctx context.Context, id string) string {
	if p.cfg.Tasks == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, agentTaskLookupTimeout)
	defer cancel()
	tasks, err := p.cfg.Tasks.TaskList(ctx, swarm.TaskListOptions{})
	if err != nil {
		log.Printf("agents: task lookup for %s: %v", id, err)
		return ""
	}
	nameTaskID := ""
	if i := strings.LastIndexByte(id, '.'); i >= 0 {
		nameTaskID = id[i+1:]
	}
	for _, t := range tasks {
		if t.ID == id || (nameTaskID != "" && t.ID == nameTaskID) {
			return t.NodeID
		}
		if cs := t.Status.ContainerStatus; cs != nil && cs.ContainerID != "" {
			if cs.ContainerID == id || (len(id) >= 12 && strings.HasPrefix(cs.ContainerID, id)) {
				return t.NodeID
			}
		}
	}
	return ""
}

// getJSON performs an authenticated GET on an agent endpoint and decodes
// the JSON response into out.
func (p *AgentPool) getJSON(ctx context.Context, addr, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+AgentAPIPrefix+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxAgentResponseBytes)).Decode(out)
}
