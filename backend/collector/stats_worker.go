package collector

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
)

// ContainerSample is one container's latest resource usage together with its
// identity. Per-node agents serve samples so the swarm server can attribute
// them to tasks and services. Labels carries only the swarm task and service
// labels (sampleLabelKeys), never the full label set.
type ContainerSample struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Stats  ContainerStats    `json:"stats"`
}

// TaskID returns the swarm task the sampled container runs, or "".
func (s ContainerSample) TaskID() string { return s.Labels[swarmTaskIDLabel] }

// ServiceName returns the swarm service the sampled container belongs to, or "".
func (s ContainerSample) ServiceName() string { return s.Labels[swarmServiceNameLabel] }

// sampleLabelKeys are the container labels copied into a ContainerSample.
var sampleLabelKeys = []string{swarmTaskIDLabel, swarmServiceNameLabel}

// pollAllStats fetches stats for all running containers and keys them by
// container name, adding per-service aggregates for swarm task containers.
func pollAllStats(ctx context.Context, cli DockerClient, maxWorkers int) StatsSnapshot {
	return BuildStatsSnapshot(PollSamples(ctx, cli, maxWorkers))
}

// PollSamples fetches stats for all running containers on the local daemon
// using a bounded worker pool. Errors on individual containers are logged and
// skipped. Samples are sorted by container name.
func PollSamples(ctx context.Context, cli DockerClient, maxWorkers int) []ContainerSample {
	containers, err := cli.ContainerList(ctx, containertypes.ListOptions{})
	if err != nil {
		log.Printf("stats: failed to list containers: %v", err)
		return []ContainerSample{}
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	var (
		mu      sync.Mutex
		results = make([]ContainerSample, 0, len(containers))
		wg      sync.WaitGroup
		sem     = make(chan struct{}, maxWorkers)
	)

	for _, c := range containers {
		if c.State != StateRunning {
			continue
		}
		if isSelfExcluded(c.Labels) {
			continue
		}
		if len(c.Names) == 0 {
			continue
		}

		sample := ContainerSample{ID: c.ID, Name: strings.TrimPrefix(c.Names[0], "/")}
		for _, k := range sampleLabelKeys {
			if v := c.Labels[k]; v != "" {
				if sample.Labels == nil {
					sample.Labels = make(map[string]string, len(sampleLabelKeys))
				}
				sample.Labels[k] = v
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cs, ok := fetchOneStats(ctx, cli, sample.ID)
			if !ok {
				return
			}
			sample.Stats = cs
			mu.Lock()
			results = append(results, sample)
			mu.Unlock()
		}()
	}

	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// BuildStatsSnapshot keys samples by container name and adds one aggregate
// entry per swarm service, keyed by service name, so service graph nodes get
// stats under their name. Aggregates sum CPU, memory, network, block I/O and
// PIDs across the service's tasks; CPU throttling takes the worst task. A
// container whose name equals a service name keeps its own entry.
func BuildStatsSnapshot(samples []ContainerSample) StatsSnapshot {
	stats := make(map[string]ContainerStats, len(samples))
	services := make(map[string]ContainerStats)
	for _, s := range samples {
		stats[s.Name] = s.Stats
		svc := s.ServiceName()
		if svc == "" {
			continue
		}
		agg := services[svc]
		agg.CPUPercent += s.Stats.CPUPercent
		agg.CPUThrottled = max(agg.CPUThrottled, s.Stats.CPUThrottled)
		agg.MemUsage += s.Stats.MemUsage
		agg.MemLimit += s.Stats.MemLimit
		agg.NetRx += s.Stats.NetRx
		agg.NetTx += s.Stats.NetTx
		agg.NetRxErrors += s.Stats.NetRxErrors
		agg.NetTxErrors += s.Stats.NetTxErrors
		agg.BlockRead += s.Stats.BlockRead
		agg.BlockWrite += s.Stats.BlockWrite
		agg.PIDs += s.Stats.PIDs
		services[svc] = agg
	}
	for name, agg := range services {
		if _, taken := stats[name]; !taken {
			stats[name] = agg
		}
	}
	return StatsSnapshot{Stats: stats}
}

// mergeSamples appends remote samples to local ones, dropping any remote
// sample for a container already sampled locally so a task is never counted
// twice in its service aggregate. Stats are keyed by container name, so a
// remote container whose name is already taken (e.g. a standalone container
// with the same name on another node) is dropped too: local samples win and
// never get overwritten by another node's container.
func mergeSamples(local, remote []ContainerSample) []ContainerSample {
	if len(remote) == 0 {
		return local
	}
	seenIDs := make(map[string]bool, len(local))
	seenNames := make(map[string]bool, len(local))
	for _, s := range local {
		seenIDs[s.ID] = true
		seenNames[s.Name] = true
	}
	merged := make([]ContainerSample, 0, len(local)+len(remote))
	merged = append(merged, local...)
	for _, s := range remote {
		if s.ID == "" || seenIDs[s.ID] || seenNames[s.Name] {
			continue
		}
		seenIDs[s.ID] = true
		seenNames[s.Name] = true
		merged = append(merged, s)
	}
	return merged
}

// fetchOneStats retrieves stats for a single container with a 10-second timeout.
func fetchOneStats(ctx context.Context, cli DockerClient, containerID string) (ContainerStats, bool) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := cli.ContainerStats(callCtx, containerID, false)
	if err != nil {
		log.Printf("stats: container %s: %v", shortID(containerID), err)
		return ContainerStats{}, false
	}
	defer resp.Body.Close()

	var raw containertypes.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		log.Printf("stats: decode %s: %v", shortID(containerID), err)
		return ContainerStats{}, false
	}

	rx, tx, rxErr, txErr := sumNetworkIO(raw.Networks)
	blockRead, blockWrite := sumBlockIO(raw.BlkioStats)

	return ContainerStats{
		CPUPercent:   calcCPUPercent(&raw),
		CPUThrottled: calcCPUThrottle(&raw),
		MemUsage:     calcMemUsage(raw.MemoryStats),
		MemLimit:     raw.MemoryStats.Limit,
		NetRx:        rx,
		NetTx:        tx,
		NetRxErrors:  rxErr,
		NetTxErrors:  txErr,
		BlockRead:    blockRead,
		BlockWrite:   blockWrite,
		PIDs:         raw.PidsStats.Current,
	}, true
}

// shortID truncates a container ID to Docker's 12-character short form.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
