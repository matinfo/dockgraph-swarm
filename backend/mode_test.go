package main

import (
	"testing"

	"github.com/docker/docker/api/types/swarm"
)

func TestResolveMode(t *testing.T) {
	manager := swarm.Info{LocalNodeState: swarm.LocalNodeStateActive, ControlAvailable: true}
	worker := swarm.Info{LocalNodeState: swarm.LocalNodeStateActive}
	none := swarm.Info{LocalNodeState: swarm.LocalNodeStateInactive}

	tests := []struct {
		name      string
		requested string
		info      swarm.Info
		want      string
		wantErr   bool
	}{
		{"auto_manager", "auto", manager, "swarm", false},
		{"auto_no_swarm", "auto", none, "standalone", false},
		{"auto_unknown_state", "auto", swarm.Info{}, "standalone", false},
		{"auto_worker_rejected", "auto", worker, "", true},
		{"swarm_manager", "swarm", manager, "swarm", false},
		{"swarm_worker_rejected", "swarm", worker, "", true},
		{"swarm_no_swarm_rejected", "swarm", none, "", true},
		{"standalone_forced_on_manager", "standalone", manager, "standalone", false},
		{"agent_on_worker", "agent", worker, "agent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMode(tt.requested, tt.info)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
