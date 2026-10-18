package web

import (
	"net/http"
)

func (s *Server) moveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /moves.js", func(w http.ResponseWriter, r *http.Request) {
		b, e := assets.ReadFile("moves.js")
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /api/moves", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireSession(w, r); !ok {
			return
		}
		list, e := s.nodes.Moves()
		if e != nil {
			writeError(w, 500, "移动任务读取失败")
			return
		}
		writeJSON(w, 200, list)
	})
	mux.HandleFunc("POST /api/files/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		var in struct {
			RequestID string `json:"request_id"`
			Target    string `json:"target"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		m, e := s.nodes.StartMove(r.Context(), in.RequestID, r.PathValue("id"), in.Target)
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
		writeJSON(w, 202, m)
	})
	mux.HandleFunc("POST /api/moves/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		var e error
		switch r.PathValue("action") {
		case "retry":
			e = s.nodes.RetryMove(r.PathValue("id"))
		case "cancel":
			e = s.nodes.CancelMove(r.PathValue("id"))
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
		m, e := s.nodes.Move(r.PathValue("id"))
		if e != nil {
			writeError(w, 404, "任务不存在")
			return
		}
		writeJSON(w, 202, m)
	})
}
