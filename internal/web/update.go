package web

import (
	"filepanel/internal/install"
	"filepanel/internal/release"
	"net/http"
)

func (s *Server) updateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireSession(w, r); !ok {
			return
		}
		b, e := install.AgentCall(r.Context(), false)
		if e != nil {
			writeJSON(w, 200, map[string]any{"current": release.Info(), "available": false, "error": e.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	mux.HandleFunc("POST /api/update", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if r.ContentLength != 0 {
			writeError(w, 400, "更新不接受 URL、路径或其他参数")
			return
		}
		b, e := install.AgentCall(r.Context(), true)
		if e != nil {
			writeError(w, 503, e.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		w.Write(b)
	})
	mux.HandleFunc("GET /update.js", func(w http.ResponseWriter, r *http.Request) {
		b, e := assets.ReadFile("update.js")
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(b)
	})
}
