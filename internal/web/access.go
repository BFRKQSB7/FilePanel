package web

import (
	"context"
	"net/http"
	"time"

	"filepanel/internal/access"
	"filepanel/internal/download"
)

func (s *Server) accessRoutes(mux *http.ServeMux) {
	if s.access == nil {
		return
	}
	mux.HandleFunc("GET /.well-known/filepanel/{token}", s.access.Proof)
	mux.HandleFunc("GET /access.js", func(w http.ResponseWriter, r *http.Request) {
		data, _ := assets.ReadFile("access.js")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /api/access", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireSession(w, r); !ok {
			return
		}
		writeJSON(w, 200, s.access.View())
	})
	mux.HandleFunc("POST /api/access/check", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		var in access.Input
		if !decodeJSON(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		view, err := s.access.Check(ctx, in)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, view)
	})
	mux.HandleFunc("POST /api/access/apply", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if err := s.access.Apply(); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 202, s.access.View())
	})
}

func (s *Server) requestSecure(r *http.Request) bool {
	if s.secure || r.TLS != nil {
		return true
	}
	cfg, err := s.meter.Config()
	return err == nil && download.TrustedRemote(r, cfg) && len(r.Header.Values("X-Forwarded-Proto")) == 1 && r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) entryURL(path string) string {
	if s.access != nil {
		return s.access.URL(path)
	}
	return path
}
