package web

import (
	"encoding/base64"
	"encoding/json"
	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/nodes"
	"filepanel/internal/usage"
	"io"
	"net/http"
)

func (s *Server) nodeRoutes(mux *http.ServeMux) {
	if s.nodes == nil {
		return
	}
	s.nodes.Routes(mux)
	s.moveRoutes(mux)
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /api/nodes/{id}/update", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				if _, ok := s.requireCSRF(w, r); !ok {
					return
				}
			} else {
				if _, ok := s.requireSession(w, r); !ok {
					return
				}
			}
			if r.ContentLength != 0 {
				writeError(w, 400, "节点更新不接受参数")
				return
			}
			b, e := s.nodes.SoftwareUpdate(r.Context(), r.PathValue("id"), r.Method == "POST")
			if e != nil {
				writeError(w, 503, "节点离线、不兼容或维护代理未就绪，未安排后台更新")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		})
	}
	mux.HandleFunc("POST /api/nodes/migration/export", s.nodeMigrationExport)
	mux.HandleFunc("POST /api/nodes/migration/preview", s.nodeMigrationImport)
	mux.HandleFunc("POST /api/nodes/migration/apply", s.nodeMigrationImport)
	mux.HandleFunc("POST /api/nodes/{id}/rebind", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if e := s.nodes.Rebind(r.Context(), r.PathValue("id")); e != nil {
			writeError(w, 503, "节点地址不可达或新面板 TLS / 身份验证失败，节点保留原配置")
			return
		}
		s.nodeList(w, r)
	})
	mux.HandleFunc("GET /nodes.js", func(w http.ResponseWriter, r *http.Request) {
		b, e := assets.ReadFile("nodes.js")
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /api/nodes", s.nodeList)
	mux.HandleFunc("POST /api/nodes/ticket", s.nodeTicket)
	mux.HandleFunc("PUT /api/nodes/settings", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		var in nodes.Settings
		if !decodeJSON(w, r, &in) {
			return
		}
		if e := s.nodes.SetSettings(in); e != nil {
			writeError(w, 400, e.Error())
			return
		}
		s.nodeList(w, r)
	})
	mux.HandleFunc("PUT /api/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		var in nodes.Node
		if !decodeJSON(w, r, &in) {
			return
		}
		if e := s.nodes.Update(r.PathValue("id"), in); e != nil {
			writeError(w, 400, e.Error())
			return
		}
		s.nodeList(w, r)
	})
	mux.HandleFunc("POST /api/nodes/{id}/probe", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if e := s.nodes.Probe(r.Context(), r.PathValue("id")); e != nil {
			writeError(w, 503, "面板到节点的 TLS / 身份 / 连接检查失败")
			return
		}
		s.nodeList(w, r)
	})
	mux.HandleFunc("POST /api/nodes/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if e := s.nodes.Revoke(r.PathValue("id")); e != nil {
			writeError(w, 400, e.Error())
			return
		}
		s.nodeList(w, r)
	})
	mux.HandleFunc("DELETE /api/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireCSRF(w, r); !ok {
			return
		}
		if e := s.nodes.RemoveNode(r.PathValue("id")); e != nil {
			writeError(w, 409, e.Error())
			return
		}
		s.nodeList(w, r)
	})
}

func (s *Server) nodeMigrationExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	b, e := s.nodes.ExportMigration(in.Password)
	if e != nil {
		writeError(w, 409, e.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="filepanel-nodes.enc.json"`)
	_, _ = w.Write(b)
}
func (s *Server) nodeMigrationImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var in struct {
		Package  string `json:"package"`
		Password string `json:"password"`
		Endpoint string `json:"endpoint"`
		CA       string `json:"ca"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, nodes.MaxMigration*2)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "迁移包请求格式错误")
		return
	}
	data, e := base64.StdEncoding.DecodeString(in.Package)
	if e != nil {
		writeError(w, 400, "迁移包格式错误")
		return
	}
	summary, e := s.nodes.ImportMigration(data, in.Password, in.Endpoint, in.CA, r.URL.Path == "/api/nodes/migration/apply")
	if e != nil {
		writeError(w, 409, e.Error())
		return
	}
	writeJSON(w, 200, summary)
}
func (s *Server) nodeList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	list, e := s.nodes.List()
	if e != nil {
		writeError(w, 500, "节点状态不可用")
		return
	}
	cfg, e := s.nodes.Settings()
	if e != nil {
		writeError(w, 500, "节点设置不可用")
		return
	}
	base, _, _ := s.store.Setting("node_panel_endpoint")
	ca, _, _ := s.store.Setting("node_panel_ca")
	writeJSON(w, 200, map[string]any{"nodes": list, "settings": cfg, "panel_id": s.nodes.ID(), "endpoint": base, "ca": ca})
}
func (s *Server) nodeTicket(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		CA       string `json:"ca"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if e := s.nodes.ConfigureEndpoint(in.Endpoint, in.CA); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	ticket, e := s.nodes.NewTicket()
	if e != nil {
		writeError(w, 500, "入网票据生成失败")
		return
	}
	writeJSON(w, 201, map[string]string{"command": s.nodes.JoinCommand(in.Endpoint, ticket), "note": "先填写节点实际 HTTPS 地址，再执行在线安装命令。票据 5 分钟内一次有效；安装器自动下载匹配架构的签名版本，成功后启用节点服务并删除入网文件。公开 HTTPS 反代仍由管理员配置。"})
}

func (s *Server) remoteAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.nodes == nil {
		return false
	}
	v, e := s.files.Metadata(r.PathValue("id"))
	if e != nil || v.StorageNode == "" {
		return false
	}
	req, e := s.nodes.SourceRequest(r.Context(), v.ID, s.clientIP(r), auth.Cookie(r), 0)
	if e == nil {
		req.Category = usage.CategoryAdmin
		e = s.dl.Serve(w, r, req)
	}
	if e != nil {
		de := downloadError(e)
		writeError(w, de.Status, de.Message)
	}
	return true
}

// distribute is called after the original public credential and current share
// version have both been checked. Slugs and panel URLs remain stable.
func (s *Server) distribute(w http.ResponseWriter, r *http.Request, c download.Claims) bool {
	if s.nodes == nil {
		return false
	}
	v, e := s.files.Metadata(c.File)
	if e != nil {
		return false
	}
	cfg, e := s.nodes.Settings()
	if e != nil {
		writeError(w, 503, "线路设置暂不可用")
		return true
	}
	relay := r.URL.Query().Get("relay")
	if relay == "" {
		relay = cfg.DefaultRelay
	}
	if relay == "local" {
		relay = ""
	}
	if relay != "" {
		t, e := s.nodes.NewDownload(r.Context(), c.File, relay, s.clientIP(r), "", c.AuthVersion)
		if e == nil {
			http.Redirect(w, r, s.nodes.TicketURL(t), http.StatusTemporaryRedirect)
			return true
		}
		if !cfg.Fallback {
			s.errorPage(w, r, 503, "下载出口暂不可用", "请稍后重试或联系管理员。", "", "", "/")
			return true
		}
	}
	if v.StorageNode == "" {
		return false
	}
	req, e := s.nodes.SourceRequest(r.Context(), c.File, s.clientIP(r), "", c.AuthVersion)
	if e == nil {
		e = s.dl.Serve(w, r, req)
	}
	if e != nil {
		de := downloadError(e)
		s.errorPage(w, r, de.Status, "文件源暂不可用", "唯一文件源或数据路径离线，恢复后可重试。", "", "", "/")
	}
	return true
}

func (s *Server) needsLocalBudget(r *http.Request, id string) bool {
	if s.nodes == nil {
		return true
	}
	v, e := s.files.Metadata(id)
	if e != nil || v.StorageNode == "" {
		return true
	}
	cfg, e := s.nodes.Settings()
	if e != nil {
		return true
	}
	relay := r.URL.Query().Get("relay")
	if relay == "" {
		relay = cfg.DefaultRelay
	}
	return relay == "" || relay == "local"
}
