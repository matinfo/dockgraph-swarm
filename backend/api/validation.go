package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/dockgraph/dockgraph/secrets"
)

// validResourceName matches safe Docker resource identifiers (container names,
// volume names, network names). Used across all inspect and log handlers.
var validResourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

// validStackName matches compose project / swarm stack names used by the
// ?stack= filters. Unlike validResourceName it accepts a single character,
// since compose allows one-character project names.
var validStackName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// jsonError writes a JSON error response with the correct Content-Type header.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// maskLabels returns a copy of labels with credential-looking values masked,
// using the same key rule as environment variables. Service labels often
// carry reverse-proxy settings such as Traefik basic-auth user hashes.
func maskLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if secrets.IsSensitiveKey(k) {
			v = secrets.Masked
		}
		out[k] = v
	}
	return out
}

func filterEnvVars(envList []string) []map[string]string {
	result := make([]map[string]string, 0, len(envList))
	for _, e := range envList {
		k, v, _ := strings.Cut(e, "=")
		if secrets.IsSensitiveKey(k) {
			v = secrets.Masked
		}
		result = append(result, map[string]string{"key": k, "value": v})
	}
	return result
}
