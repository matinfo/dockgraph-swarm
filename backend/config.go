package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dockgraph/dockgraph/auth"
	"github.com/dockgraph/dockgraph/collector"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	BindAddr      string        // Listen address (DG_BIND_ADDR, default "0.0.0.0")
	Port          string        // HTTP listen port (DG_PORT, default "7800")
	PollInterval  time.Duration // Docker API poll interval (DG_POLL_INTERVAL, default 30s)
	ComposePaths  []string      // Explicit compose paths override (DG_COMPOSE_PATH); nil means auto-detect from container mounts
	PasswordHash  string        // Argon2id hash of DG_PASSWORD; empty means auth disabled
	StatsInterval time.Duration // Container stats poll interval (DG_STATS_INTERVAL, default 3s)
	StatsWorkers  int           // Max concurrent stats API calls (DG_STATS_WORKERS, default 50)
	// Mode selects the runtime mode (DG_MODE: auto|standalone|swarm|agent,
	// default auto). Auto resolves at startup from the daemon's swarm state.
	Mode string
	// SwarmPollInterval is how often swarm tasks are polled for changes on
	// remote nodes (DG_SWARM_POLL_INTERVAL, default 5s).
	SwarmPollInterval time.Duration
}

// validModes lists the accepted DG_MODE values.
var validModes = map[string]bool{
	collector.ModeAuto:       true,
	collector.ModeStandalone: true,
	collector.ModeSwarm:      true,
	collector.ModeAgent:      true,
}

// LoadConfig reads configuration from environment variables with sensible defaults.
func LoadConfig() Config {
	cfg := Config{
		BindAddr:      "0.0.0.0",
		Port:          "7800",
		PollInterval:  30 * time.Second,
		StatsInterval: 3 * time.Second,
		StatsWorkers:  50,

		Mode:              collector.ModeAuto,
		SwarmPollInterval: 5 * time.Second,
	}

	if v := os.Getenv("DG_BIND_ADDR"); v != "" {
		cfg.BindAddr = v
	}
	if v := os.Getenv("DG_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			log.Printf("invalid DG_PORT %q, using default %s", v, cfg.Port)
		} else {
			cfg.Port = v
		}
	}

	cfg.PollInterval = parseDuration("DG_POLL_INTERVAL", cfg.PollInterval, time.Second)
	cfg.StatsInterval = parseDuration("DG_STATS_INTERVAL", cfg.StatsInterval, time.Second)
	cfg.SwarmPollInterval = parseDuration("DG_SWARM_POLL_INTERVAL", cfg.SwarmPollInterval, time.Second)

	if v := os.Getenv("DG_MODE"); v != "" {
		mode := strings.ToLower(strings.TrimSpace(v))
		if validModes[mode] {
			cfg.Mode = mode
		} else {
			log.Printf("invalid DG_MODE %q, using default %s", v, cfg.Mode)
		}
	}

	if v := os.Getenv("DG_COMPOSE_PATH"); v != "" {
		parts := strings.Split(v, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}
		cfg.ComposePaths = parts
	}

	if v := os.Getenv("DG_STATS_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			log.Printf("invalid DG_STATS_WORKERS %q, using default %d", v, cfg.StatsWorkers)
		} else {
			cfg.StatsWorkers = n
		}
	}

	if raw := os.Getenv("DG_PASSWORD"); raw != "" {
		if auth.IsHashedPassword(raw) {
			if err := auth.ValidateHash(raw); err != nil {
				log.Fatalf("DG_PASSWORD contains invalid hash: %v", err)
			}
			cfg.PasswordHash = raw
		} else {
			hash, err := auth.HashPassword(raw)
			if err != nil {
				log.Fatalf("Failed to hash DG_PASSWORD: %v", err)
			}
			cfg.PasswordHash = hash
		}
	}

	return cfg
}

// parseDuration reads a duration from an environment variable with minimum enforcement.
func parseDuration(envKey string, fallback, minimum time.Duration) time.Duration {
	v := os.Getenv(envKey)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid %s %q, using default %s", envKey, v, fallback)
		return fallback
	}
	if d < minimum {
		log.Printf("%s %s is too low, using minimum %s", envKey, v, minimum)
		return minimum
	}
	return d
}
