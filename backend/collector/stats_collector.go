package collector

import (
	"context"
	"log"
	"sync"
	"time"
)

// StatsCollector periodically polls container resource usage via a worker pool.
// Stats are emitted as StatsSnapshot values on a channel, separate from
// topology updates to avoid triggering graph relayout.
type StatsCollector struct {
	client     DockerClient
	interval   time.Duration
	maxWorkers int
	remote     RemoteSampler
	updates    chan StatsSnapshot
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// RemoteSampler supplies container samples from other nodes (per-node agents
// in swarm mode). Samples must return within its own timeouts and honour ctx.
type RemoteSampler interface {
	Samples(ctx context.Context) []ContainerSample
}

// SetRemote adds a source of samples from other nodes, merged into every
// snapshot alongside local samples. Must be called before Start.
func (s *StatsCollector) SetRemote(r RemoteSampler) {
	s.remote = r
}

// NewStatsCollector creates a collector that polls container stats at the given interval.
func NewStatsCollector(cli DockerClient, interval time.Duration, maxWorkers int) *StatsCollector {
	return &StatsCollector{
		client:     cli,
		interval:   interval,
		maxWorkers: maxWorkers,
		updates:    make(chan StatsSnapshot, 4),
		stopCh:     make(chan struct{}),
	}
}

// Updates returns a read-only channel that emits stats snapshots.
func (s *StatsCollector) Updates() <-chan StatsSnapshot {
	return s.updates
}

// Start launches the periodic stats polling loop.
func (s *StatsCollector) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.pollLoop(ctx)
	}()
	log.Printf("stats collector started (interval=%s, workers=%d)", s.interval, s.maxWorkers)
}

// Stop signals the polling loop to exit and waits for it to finish.
func (s *StatsCollector) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

func (s *StatsCollector) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Initial poll on start.
	snap := s.poll(ctx)
	select {
	case s.updates <- snap:
	case <-ctx.Done():
		return
	}

	for {
		select {
		case <-ticker.C:
			snap := s.poll(ctx)
			select {
			case s.updates <- snap:
			default:
				// Drop if consumer is slow — stats are ephemeral.
			}
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// poll samples local containers and, concurrently, the remote sampler, then
// merges both (local wins on duplicate container IDs) into one snapshot.
func (s *StatsCollector) poll(ctx context.Context) StatsSnapshot {
	if s.remote == nil {
		return pollAllStats(ctx, s.client, s.maxWorkers)
	}
	remoteCh := make(chan []ContainerSample, 1)
	go func() {
		remoteCh <- s.remote.Samples(ctx)
	}()
	local := PollSamples(ctx, s.client, s.maxWorkers)
	return BuildStatsSnapshot(mergeSamples(local, <-remoteCh))
}
