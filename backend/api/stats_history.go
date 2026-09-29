package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/swarm"
	"github.com/dockgraph/dockgraph/collector"
)

var validRanges = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

// HandleStatsHistory returns a handler for GET /api/stats/history?range={5m|1h|6h|24h}.
// An optional ?stack= keeps only the series of containers belonging to that
// compose project / swarm stack. History is keyed by container name only, so
// stack membership is resolved from the current container list (lister); a
// container that has since been removed can no longer be attributed. In swarm
// mode (services non-nil) the stack's service aggregates, keyed by service
// name, and its task containers on any node ({service}.*) are kept too.
func HandleStatsHistory(history *collector.StatsHistory, lister ContainerLister, services ServiceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rangeStr := r.URL.Query().Get("range")
		if rangeStr == "" {
			rangeStr = "1h"
		}

		dur, ok := validRanges[rangeStr]
		if !ok {
			jsonError(w, "invalid range: use 5m, 1h, 6h, or 24h", http.StatusBadRequest)
			return
		}

		stack := r.URL.Query().Get("stack")
		if stack != "" && !validResourceName.MatchString(stack) {
			jsonError(w, "invalid stack", http.StatusBadRequest)
			return
		}

		now := time.Now()
		result := history.Query(now.Add(-dur), now)

		if stack != "" {
			members, err := stackContainerNames(r.Context(), lister, stack)
			if err != nil {
				jsonError(w, "failed to resolve stack", http.StatusInternalServerError)
				return
			}
			svcNames, err := stackServiceNames(r.Context(), services, stack)
			if err != nil {
				jsonError(w, "failed to resolve stack", http.StatusInternalServerError)
				return
			}
			for name := range result.Containers {
				if !members[name] && !isServiceSeries(name, svcNames) {
					delete(result.Containers, name)
				}
			}
		}

		resolution := 3
		if dur > time.Hour {
			resolution = 30
		}

		resp := map[string]any{
			"range":      rangeStr,
			"resolution": resolution,
			"timestamps": result.Timestamps,
			"containers": result.Containers,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// stackContainerNames returns the names of all containers (running or not)
// whose compose project or stack namespace is stack.
func stackContainerNames(ctx context.Context, lister ContainerLister, stack string) (map[string]bool, error) {
	names := make(map[string]bool)
	if lister == nil {
		return names, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	summaries, err := lister.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, c := range summaries {
		if collector.ProjectOf(c.Labels) == stack {
			names[containerDisplayName(c)] = true
		}
	}
	return names, nil
}

// stackServiceNames returns the names of the swarm services deployed in stack.
func stackServiceNames(ctx context.Context, services ServiceLister, stack string) (map[string]bool, error) {
	names := make(map[string]bool)
	if services == nil {
		return names, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := services.ServiceList(ctx, swarm.ServiceListOptions{})
	if err != nil {
		return nil, err
	}
	for _, svc := range list {
		if collector.ProjectOf(svc.Spec.Labels) == stack {
			names[svc.Spec.Name] = true
		}
	}
	return names, nil
}

// isServiceSeries reports whether a stats series belongs to one of the
// services: its aggregate (the service name) or one of its task containers
// ({service}.{slot|node}.{taskID}).
func isServiceSeries(name string, services map[string]bool) bool {
	if services[name] {
		return true
	}
	for svc := range services {
		if strings.HasPrefix(name, svc+".") {
			return true
		}
	}
	return false
}
