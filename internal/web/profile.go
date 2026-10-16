package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"

	"filepanel/internal/profile"
	"filepanel/internal/usage"
)

func (s *Server) profileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /site/{role}", s.siteAsset)
	mux.HandleFunc("GET /api/profile", s.getProfile)
	mux.HandleFunc("POST /api/profile", s.saveProfile)
	mux.HandleFunc("POST /api/profile/preview", s.previewProfile)
	mux.HandleFunc("GET /api/profile/export", s.exportProfile)
	mux.HandleFunc("POST /api/profile/import", s.importProfile)
	mux.HandleFunc("POST /api/profile/import/{token}/apply", s.applyImport)
	mux.HandleFunc("DELETE /api/profile/import/{token}", s.discardImport)
}

// siteAsset 公开提供当前站点 Logo/横幅/背景；与资源图片一样占用并发名额并计入用量。
func (s *Server) siteAsset(w http.ResponseWriter, r *http.Request) {
	role, ok := profile.ParseRole(r.PathValue("role"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	mime, data, err := s.profile.Asset(role)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.dl.ServeBytes(w, r, s.clientIP(r), usage.CategoryAsset, mime, data); err != nil {
		de := downloadError(err)
		http.Error(w, de.Message, de.Status)
	}
}

type profileView struct {
	profile.Snapshot
	Defaults map[string]string `json:"defaults"`
	Limits   map[string]int64  `json:"limits"`
}

func (s *Server) profileJSON() profileView {
	limits := map[string]int64{}
	for _, role := range profile.Roles {
		limits[string(role)] = role.MaxBytes()
	}
	return profileView{Snapshot: s.profile.Snapshot(), Limits: limits, Defaults: map[string]string{
		"site_name": profile.DefaultSiteName, "intro": profile.DefaultIntro, "footer": profile.DefaultFooter, "button_text": profile.DefaultButtonText}}
}

func (s *Server) profileError(w http.ResponseWriter, err error) {
	var ue *profile.UserError
	switch {
	case errors.As(err, &ue):
		writeError(w, http.StatusBadRequest, ue.Msg)
	case errors.Is(err, profile.ErrSpace):
		writeError(w, http.StatusInsufficientStorage, err.Error()+"，当前配置未改变")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.profileJSON())
}

type editConfig struct {
	profile.Fields
	Assets map[string]string `json:"assets"` // 角色 -> keep | remove | replace
}

func readPart(p *multipart.Part, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(p, limit+1))
	if err != nil {
		return nil, errors.New("读取上传数据失败")
	}
	return data, nil
}

// readEdit 解析表单提交：config 字段（JSON）加最多三张按角色命名的图片。请求体整体受限，
// 图片只在内存中处理，真正落盘发生在 profile.Manager 的暂存与原子切换里。
func (s *Server) readEdit(w http.ResponseWriter, r *http.Request) (profile.Edit, int64, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, profile.MaxEditBytes+(256<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return profile.Edit{}, 0, false
	}
	var cfg *editConfig
	files := map[profile.Role][]byte{}
	var received int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "读取上传数据失败或超过大小上限")
			return profile.Edit{}, 0, false
		}
		name := part.FormName()
		role, isRole := profile.ParseRole(name)
		switch {
		case name == "config" && cfg == nil:
			raw, rerr := readPart(part, 16<<10)
			if rerr != nil || len(raw) > 16<<10 {
				writeError(w, http.StatusBadRequest, "配置内容过大")
				return profile.Edit{}, 0, false
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			cfg = &editConfig{}
			if dec.Decode(cfg) != nil || dec.Decode(new(any)) != io.EOF {
				writeError(w, http.StatusBadRequest, "配置格式错误")
				return profile.Edit{}, 0, false
			}
		case isRole && files[role] == nil:
			data, rerr := readPart(part, role.MaxBytes())
			if rerr != nil {
				writeError(w, http.StatusBadRequest, rerr.Error())
				return profile.Edit{}, 0, false
			}
			files[role] = data
			received += int64(len(data))
		default:
			writeError(w, http.StatusBadRequest, "请求包含未知或重复的字段")
			return profile.Edit{}, 0, false
		}
		_ = part.Close()
	}
	if cfg == nil {
		writeError(w, http.StatusBadRequest, "缺少配置内容")
		return profile.Edit{}, 0, false
	}
	edit := profile.Edit{Fields: cfg.Fields, Changes: map[profile.Role]profile.Change{}}
	for key, action := range cfg.Assets {
		role, ok := profile.ParseRole(key)
		if !ok {
			writeError(w, http.StatusBadRequest, "未知的素材用途")
			return profile.Edit{}, 0, false
		}
		switch action {
		case "keep":
		case "remove":
			edit.Changes[role] = profile.Change{Remove: true}
		case "replace":
			if len(files[role]) == 0 {
				writeError(w, http.StatusBadRequest, role.Label()+"：请选择要上传的图片")
				return profile.Edit{}, 0, false
			}
			edit.Changes[role] = profile.Change{Data: files[role]}
		default:
			writeError(w, http.StatusBadRequest, "素材操作无效")
			return profile.Edit{}, 0, false
		}
	}
	for role, data := range files {
		if _, replaced := edit.Changes[role]; !replaced && len(data) > 0 {
			writeError(w, http.StatusBadRequest, role.Label()+"：上传了图片但未声明替换")
			return profile.Edit{}, 0, false
		}
	}
	return edit, received, true
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	edit, received, ok := s.readEdit(w, r)
	if !ok {
		return
	}
	cand, err := s.profile.Compose(edit)
	if err != nil {
		s.profileError(w, err)
		return
	}
	if _, err := s.profile.Commit(cand); err != nil {
		s.profileError(w, err)
		return
	}
	_ = s.meter.RecordReceived(usage.CategoryUpload, received)
	writeJSON(w, http.StatusOK, s.profileJSON())
}

// previewHTML 用与公开页相同的模板和样式渲染候选配置。图片以 data URL 内联，
// 因此预览不需要任何落盘，也不会泄漏到公开路径。
func previewHTML(c profile.Candidate) (string, error) {
	urls := map[profile.Role]string{}
	for role, b := range c.Blobs {
		urls[role] = "data:" + b.Mime + ";base64," + base64.StdEncoding.EncodeToString(b.Data)
	}
	view := profile.NewView(c.Fields, urls)
	data := struct {
		page
		Button string
	}{pageFor(view, "预览", ""), view.ButtonText}
	var buf bytes.Buffer
	if err := publicTemplates.ExecuteTemplate(&buf, "preview", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) previewProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	edit, _, ok := s.readEdit(w, r)
	if !ok {
		return
	}
	cand, err := s.profile.Compose(edit)
	if err != nil {
		s.profileError(w, err)
		return
	}
	html, err := previewHTML(cand)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "预览生成失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

func (s *Server) exportProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r); !ok {
		return
	}
	pkg, rev, err := s.profile.Export()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "导出失败")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="filepanel-profile-r`+strconv.FormatInt(rev, 10)+`.zip"`)
	if err := s.dl.ServeBytes(w, r, s.clientIP(r), usage.CategoryAdmin, "application/zip", pkg); err != nil {
		de := downloadError(err)
		writeError(w, de.Status, de.Message)
	}
}

type importAsset struct {
	Role  string `json:"role"`
	Label string `json:"label"`
	Mime  string `json:"mime"`
	Size  int    `json:"size"`
}

// importProfile 只做“上传 → 校验 → 暂存 → 预览”。当前配置在管理员确认前不会被触碰。
func (s *Server) importProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, profile.MaxPackageBytes+(64<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择要导入的配置包")
		return
	}
	var pkg []byte
	for pkg == nil {
		part, err := reader.NextPart()
		if err != nil {
			writeError(w, http.StatusBadRequest, "请选择要导入的配置包（或文件超过大小上限）")
			return
		}
		if part.FormName() != "package" {
			_ = part.Close()
			continue
		}
		pkg, err = readPart(part, profile.MaxPackageBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	cand, err := profile.ParsePackage(pkg)
	if err != nil {
		s.profileError(w, err)
		return
	}
	html, err := previewHTML(cand)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "预览生成失败")
		return
	}
	token, err := s.profile.Stage(cand)
	if err != nil {
		s.profileError(w, err)
		return
	}
	assets := []importAsset{}
	for _, role := range profile.Roles {
		if b, ok := cand.Blobs[role]; ok {
			assets = append(assets, importAsset{Role: string(role), Label: role.Label(), Mime: b.Mime, Size: len(b.Data)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "fields": cand.Fields, "assets": assets, "html": html})
}

func (s *Server) applyImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	if _, err := s.profile.Apply(r.PathValue("token")); err != nil {
		s.profileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.profileJSON())
}

func (s *Server) discardImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCSRF(w, r); !ok {
		return
	}
	s.profile.Discard(r.PathValue("token"))
	writeJSON(w, http.StatusOK, map[string]string{"message": "已放弃导入"})
}
