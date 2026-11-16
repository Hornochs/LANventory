package web

import (
	"encoding/json"
	"net/http"

	"github.com/lan-dot-party/lanventory/pkg/version"
)

// Health reports the service state as JSON (no login required, no secrets).
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status, state := http.StatusOK, "ok"
	if err := s.DB.Ping(r.Context()); err != nil {
		status, state = http.StatusServiceUnavailable, "db unavailable"
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state, "version": version.Version})
}
