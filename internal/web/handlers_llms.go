package web

import (
	"net/http"

	"github.com/lan-dot-party/lanventory"
)

// LLMs serves llms.txt (public, no login), a short description of LANventory
// for tools and agents.
func (s *Server) LLMs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(lanventory.LLMsTxt))
}
