package main

import (
	"fmt"

	"github.com/docker/docker/api/types/swarm"
	"github.com/dockgraph/dockgraph/collector"
)

// resolveMode turns the configured DG_MODE into the effective runtime mode
// given the daemon's swarm state. Auto picks swarm on an active manager and
// standalone outside a swarm. A swarm worker cannot see the cluster, so it is
// rejected unless running as an agent; explicit swarm mode requires a manager.
// Explicit standalone is always honoured.
func resolveMode(requested string, sw swarm.Info) (string, error) {
	active := sw.LocalNodeState == swarm.LocalNodeStateActive
	manager := active && sw.ControlAvailable

	switch requested {
	case collector.ModeAgent, collector.ModeStandalone:
		return requested, nil
	case collector.ModeSwarm:
		if !manager {
			return "", fmt.Errorf("DG_MODE=swarm requires a swarm manager node (local node state %q)", sw.LocalNodeState)
		}
		return collector.ModeSwarm, nil
	default:
		if manager {
			return collector.ModeSwarm, nil
		}
		if active {
			return "", fmt.Errorf("this node is a swarm worker: run dockgraph on a manager, or set DG_MODE=agent (or DG_MODE=standalone for local-only view)")
		}
		return collector.ModeStandalone, nil
	}
}
