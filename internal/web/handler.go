package web

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"filepanel/internal/access"
	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/install"
	"filepanel/internal/nodes"
	"filepanel/internal/profile"
	"filepanel/internal/release"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

//go:embed index.html admin.js admin-ext.js access.js nodes.js moves.js update.js
var assets embed.FS

type Server struct {
	store   *store.Store
	auth    *auth.Auth
	files   *files.Manager
	shares  *share.Manager
	profile *profile.Manager
	meter   *usage.Meter
	dl      *download.Service
	access  *access.Manager
	nodes   *nodes.Panel
	secure  bool
	dataDir string
}

// Deps 汇集处理器依赖，便于测试注入可控时钟与小额度。
type Deps struct {
	Store    *store.Store
	Auth     *auth.Auth
	Files    *files.Manager
	Shares   *share.Manager
	Profile  *profile.Manager
	Meter    *usage.Meter
	Download *download.Service
	Access   *access.Manager
	Nodes    *nodes.Panel
}

// Build 用默认配置装配分享、计量与下载服务。
func Build(s *store.Store, a *auth.Auth, f *files.Manager, options ...access.Options) (Deps, error) {
	shares, err := share.New(s, f)
	if err != nil {
		return Deps{}, err
	}
	prof, err := profile.New(s, f.DataDir(), f)
	if err != nil {
		return Deps{}, err
	}
	meter, err := usage.New(s, nil)
	if err != nil {
		return Deps{}, err
	}
	dl, err := download.New(s, f, meter)
	if err != nil {
		return Deps{}, err
	}
	var opts access.Options
	if len(options) > 0 {
		opts = options[0]
	}
	entry, err := access.New(s, f, opts)
	if err != nil {
		return Deps{}, err
	}
	peers, err := nodes.NewPanel(s, f, shares, a, meter, dl)
	if err != nil {
		return Deps{}, err
	}
	return Deps{Store: s, Auth: a, Files: f, Shares: shares, Profile: prof, Meter: meter, Download: dl, Access: entry, Nodes: peers}, nil
}

// New 使用默认依赖创建处理器，装配失败时 panic，仅用于测试；服务入口使用 Build + NewServer。
func New(s *store.Store, a *auth.Auth, f *files.Manager, secure bool) http.Handler {
	deps, err := Build(s, a, f)
	if err != nil {
		panic(err)
	}
	return NewServer(deps, secure)
}

func NewServer(d Deps, secure bool) http.Handler {
	server := &Server{store: d.Store, auth: d.Auth, files: d.Files, shares: d.Shares, profile: d.Profile, meter: d.Meter, dl: d.Download, access: d.Access, secure: secure, dataDir: d.Files.DataDir()}
	server.nodes = d.Nodes
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", server.home)
	mux.HandleFunc("GET /s/{slug}", server.sharePage)
	mux.HandleFunc("GET /s/{slug}/get", server.getLink)
	mux.HandleFunc("GET /d/{id}", server.content)
	mux.HandleFunc("GET /a/{id}", server.publicAsset)
	mux.HandleFunc("GET /admin", server.index)
	mux.HandleFunc("GET /admin-ext.js", server.adminScript)
	mux.HandleFunc("GET /admin.js", func(w http.ResponseWriter, r *http.Request) {
		data, _ := assets.ReadFile("admin.js")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	server.adminRoutes(mux)
	server.profileRoutes(mux)
	server.accessRoutes(mux)
	server.nodeRoutes(mux)
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /api/status", server.status)
	mux.HandleFunc("POST /api/init", server.initialize)
	mux.HandleFunc("POST /api/login", server.login)
	mux.HandleFunc("POST /api/logout", server.logout)
	mux.HandleFunc("POST /api/password", server.password)
	mux.HandleFunc("GET /api/files", server.listFiles)
	mux.HandleFunc("POST /api/files", server.upload)
	mux.HandleFunc("GET /api/files/{id}", server.download)
	mux.HandleFunc("PATCH /api/files/{id}", server.rename)
	mux.HandleFunc("DELETE /api/files/{id}", server.remove)
	mux.HandleFunc("GET /api/data", server.data)
	server.updateRoutes(mux)
	return securityHeaders(install.MaintenanceGate(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "后台页面不可用", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "数据库不可用")
		return
	}
	writeJSON(w, http.StatusOK, release.Info())
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.auth.Initialized()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取初始化状态失败")
		return
	}
	session, signedIn := s.session(r)
	state := map[string]any{"initialized": initialized, "authenticated": signedIn}
	if signedIn {
		state["csrf"] = session.CSRF
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) initialize(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.auth.Initialize(input.Token, input.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"message": "管理员初始化完成，请登录"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	token, csrf, err := s.auth.Login(r, input.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	auth.SessionCookie(w, token, s.requestSecure(r))
	writeJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	if err := s.auth.Logout(auth.Cookie(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "退出失败")
		return
	}
	auth.ClearCookie(w, s.requestSecure(r))
	writeJSON(w, http.StatusOK, map[string]string{"message": "已退出"})
}

func (s *Server) password(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var input struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.auth.ChangePassword(input.Current, input.Next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	auth.ClearCookie(w, s.requestSecure(r))
	writeJSON(w, http.StatusOK, map[string]string{"message": "密码已更新，请重新登录"})
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	items, err := s.files.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取文件列表失败")
		return
	}
	summaries, err := s.shares.Summaries()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取分享状态失败")
		return
	}
	type adminItem struct {
		files.Item
		share.Summary
	}
	out := make([]adminItem, 0, len(items))
	for _, item := range items {
		out = append(out, adminItem{Item: item, Summary: summaries[item.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	if r.ContentLength > files.DefaultMaxUpload+(2<<20) {
		writeError(w, http.StatusRequestEntityTooLarge, "上传请求超过大小上限")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, files.DefaultMaxUpload+(2<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择要上传的文件")
		return
	}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			writeError(w, http.StatusBadRequest, "请选择要上传的文件")
			return
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "读取上传数据失败")
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		// Validate the remainder while the object is still temporary. A rejected
		// request must never leave a committed (or recoverable verified) file.
		counted := &countingReader{r: part}
		validate := func() error {
			for {
				next, err := reader.NextPart()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("读取上传数据失败: %w", err)
				}
				if next.FormName() == "file" {
					return errors.New("一次只能上传一个文件")
				}
				if err := next.Close(); err != nil {
					return err
				}
			}
		}
		var result files.Item
		target := r.URL.Query().Get("storage")
		if target != "" && s.nodes != nil {
			result, err = s.nodes.Upload(r.Context(), target, part.FileName(), auth.Cookie(r), counted, validate)
		} else {
			result, err = s.files.UploadChecked(r.Context(), part.FileName(), -1, counted, validate)
		}
		_ = part.Close()
		if target == "" {
			_ = s.meter.RecordReceived(usage.CategoryUpload, counted.n)
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusRequestTimeout
			}
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, result)
		return
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	if s.remoteAdmin(w, r) {
		return
	}
	// 管理员下载同样走受控出口：Range、租约与用量计量一并生效，不能静默绕过额度。
	err := s.dl.Serve(w, r, download.Request{FileID: r.PathValue("id"), Category: usage.CategoryAdmin, IP: s.clientIP(r),
		Revalidate: func() error {
			if _, ok := s.session(r); !ok {
				return errors.New("会话已失效")
			}
			return nil
		}})
	if err != nil {
		de := downloadError(err)
		writeError(w, de.Status, de.Message)
	}
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.files.Rename(r.PathValue("id"), input.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "文件不存在")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "文件名已更新"})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	// 先撤销分享并终止活动响应，否则删除会等待读者结束。
	if err := s.shares.Revoke(id); err != nil {
		writeError(w, http.StatusInternalServerError, "撤销分享失败："+err.Error())
		return
	}
	s.dl.Revoke(id)
	s.shares.RemoveFileAssets(id)
	var err error
	if s.nodes != nil {
		err = s.nodes.Delete(r.Context(), id)
	} else {
		err = s.files.Remove(id)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "删除失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "文件已删除"})
}

func (s *Server) data(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	total, free, used, err := s.store.DiskInfo(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取磁盘信息失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]uint64{"total": total, "free": free, "used": used})
}

func (s *Server) session(r *http.Request) (auth.Session, bool) {
	return s.auth.Validate(auth.Cookie(r))
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	session, ok := s.session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "请先登录")
		return session, false
	}
	return session, true
}

func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	session, ok := s.requireSession(w, r)
	if !ok {
		return session, false
	}
	if r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("X-CSRF-Token") != session.CSRF {
		writeError(w, http.StatusForbidden, "请求校验失败，请刷新后重试")
		return session, false
	}
	return session, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
