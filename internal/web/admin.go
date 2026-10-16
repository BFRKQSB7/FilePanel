package web

import (
	"errors"
	"net/http"

	"filepanel/internal/share"
	"filepanel/internal/usage"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/files/{id}/share", s.getShare)
	mux.HandleFunc("PUT /api/files/{id}/share", s.putShare)
	mux.HandleFunc("POST /api/files/{id}/assets", s.uploadAsset)
	mux.HandleFunc("DELETE /api/files/{id}/assets/{asset}", s.deleteAsset)
	mux.HandleFunc("GET /api/assets/{id}", s.adminAsset)
	mux.HandleFunc("POST /api/markdown/preview", s.previewMarkdown)
	mux.HandleFunc("GET /api/usage", s.usageStatus)
	mux.HandleFunc("PUT /api/usage/config", s.usageConfig)
	mux.HandleFunc("POST /api/usage/reconcile", s.usageReconcile)
}

func (s *Server) adminScript(w http.ResponseWriter, r *http.Request) {
	data, err := assets.ReadFile("admin-ext.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type shareView struct {
	share.Share
	Assets  []share.Asset `json:"assets"`
	Preview string        `json:"preview"`
	URL     string        `json:"url"`
}

func (s *Server) shareView(fileID string) (shareView, error) {
	sh, err := s.shares.Ensure(fileID)
	if err != nil {
		return shareView{}, err
	}
	list, err := s.shares.Assets(fileID)
	if err != nil {
		return shareView{}, err
	}
	return shareView{Share: sh, Assets: list, Preview: share.RenderMarkdown(sh.Markdown, s.shares.AssetOwned(fileID)), URL: s.entryURL("/s/" + sh.Slug)}, nil
}

func (s *Server) shareError(w http.ResponseWriter, err error) {
	if errors.Is(err, share.ErrNotFound) {
		writeError(w, http.StatusNotFound, "文件不存在或不可用")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (s *Server) getShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	view, err := s.shareView(r.PathValue("id"))
	if err != nil {
		s.shareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) putShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var in share.Input
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !decodeJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	_, revoked, err := s.shares.Update(id, in)
	if err != nil {
		s.shareError(w, err)
		return
	}
	if revoked {
		s.dl.Revoke(id) // 活动响应立即终止；漏掉也会在一个续租周期内因重新校验而终止
	}
	view, err := s.shareView(id)
	if err != nil {
		s.shareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) uploadAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, share.MaxAssetBytes+(256<<10))
	if err := r.ParseMultipartForm(share.MaxAssetBytes + (256 << 10)); err != nil {
		writeError(w, http.StatusBadRequest, "图片不能超过 2 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择要上传的图片")
		return
	}
	defer file.Close()
	asset, err := s.shares.AddAsset(r.PathValue("id"), file)
	if err != nil {
		s.shareError(w, err)
		return
	}
	_ = s.meter.RecordReceived(usage.CategoryUpload, asset.Size)
	writeJSON(w, http.StatusCreated, asset)
}

func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	if err := s.shares.RemoveAsset(r.PathValue("id"), r.PathValue("asset")); err != nil {
		s.shareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "图片已删除"})
}

func (s *Server) adminAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	a, data, err := s.shares.OpenAsset(r.PathValue("id"), false)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.dl.ServeBytes(w, r, s.clientIP(r), usage.CategoryAsset, a.Mime, data); err != nil {
		de := downloadError(err)
		http.Error(w, de.Message, de.Status)
	}
}

func (s *Server) previewMarkdown(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var in struct {
		FileID   string `json:"file_id"`
		Markdown string `json:"markdown"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !decodeJSON(w, r, &in) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": share.RenderMarkdown(in.Markdown, s.shares.AssetOwned(in.FileID))})
}

func (s *Server) usageStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	st, err := s.meter.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取用量失败")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) usageConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	var cfg usage.Config
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if err := s.meter.SetConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.usageStatus(w, r)
}

func (s *Server) usageReconcile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	released, err := s.meter.Reconcile()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "对账失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"released": released})
}
