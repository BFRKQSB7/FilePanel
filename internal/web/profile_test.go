package web

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/files"
	"filepanel/internal/share"
	"filepanel/internal/store"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func pngBytes(w, h int, c color.RGBA) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, solid(w, h, c))
	return b.Bytes()
}

func jpegBytes(w, h int, c color.RGBA) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, solid(w, h, c), nil)
	return b.Bytes()
}

func webpBytes() []byte {
	b, _ := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	return b
}

func (e *env) send(method, path string, body []byte, contentType string, withCSRF bool) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	if withCSRF {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func profileForm(t *testing.T, cfg map[string]any, files map[string][]byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(cfg)
	fw, _ := w.CreateFormField("config")
	_, _ = fw.Write(raw)
	for role, data := range files {
		part, _ := w.CreateFormFile(role, role+".bin")
		_, _ = part.Write(data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func packageForm(t *testing.T, pkg []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("package", "profile.zip")
	_, _ = part.Write(pkg)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		return ""
	}
	return s[i : i+j+len(to)]
}

func brandedConfig() map[string]any {
	return map[string]any{"site_name": "星河下载", "intro": "我们的资源站", "accent": "#1d4ed8", "footer": "© 星河", "button_text": "马上下载",
		"assets": map[string]string{"logo": "replace", "banner": "replace", "background": "replace"}}
}

func brandedFiles() map[string][]byte {
	return map[string][]byte{"logo": pngBytes(48, 48, color.RGBA{20, 80, 200, 255}), "banner": jpegBytes(600, 160, color.RGBA{30, 60, 140, 255}), "background": webpBytes()}
}

func (e *env) saveProfile(cfg map[string]any, files map[string][]byte) *httptest.ResponseRecorder {
	e.t.Helper()
	body, ct := profileForm(e.t, cfg, files)
	return e.send(http.MethodPost, "/api/profile", body, ct, true)
}

func TestSiteProfileAppliesToHomeAndDetailAndPerResourceButtonWins(t *testing.T) {
	e := newEnv(t)
	a := e.upload("a.bin", content(5000, 1))
	b := e.upload("b.bin", content(6000, 2))
	shA := e.publish(a.ID, "资源甲", true)
	shB, _, err := e.deps.Shares.Update(b.ID, share.Input{Title: "资源乙", ButtonText: "获取", Enabled: true, Featured: true})
	if err != nil {
		t.Fatal(err)
	}

	// 默认外观保持原样：官方品牌、无 Logo、无主题覆盖。
	home := e.get("/", nil, false).Body.String()
	for _, want := range []string{"FilePanel", "由 FilePanel 提供文件发布", `<span class="mark">F</span>`, "公开发布的文件与版本说明"} {
		if !strings.Contains(home, want) {
			t.Fatalf("default home lacks %q", want)
		}
	}
	if strings.Contains(home, "/site/") || strings.Contains(home, "--blue:#1d4ed8") {
		t.Fatal("default page references site assets or a theme")
	}

	if rec := e.saveProfile(brandedConfig(), brandedFiles()); rec.Code != http.StatusOK {
		t.Fatalf("save profile: %d %s", rec.Code, rec.Body.String())
	}
	home = e.get("/", nil, false).Body.String()
	for _, want := range []string{"<title>资源下载 · 星河下载</title>", `<img class="logo" src="/site/logo?v=1" alt="">星河下载`, "我们的资源站", "© 星河",
		"--blue:#1d4ed8", `url("/site/banner?v=1")`, `url("/site/background?v=1")`, "prefers-color-scheme:dark", `name="q"`, "资源甲", "资源乙"} {
		if !strings.Contains(home, want) {
			t.Fatalf("branded home lacks %q", want)
		}
	}
	if strings.Contains(home, "由 FilePanel 提供文件发布") {
		t.Fatal("old footer still shown")
	}
	// 搜索仍可用。
	if q := e.get("/?q=资源甲", nil, false).Body.String(); !strings.Contains(q, "资源甲") || strings.Contains(q, "资源乙") {
		t.Fatal("site search broke")
	}
	// 详情页：全站默认按钮文字只用于没有单独设置的资源。
	detailA := e.get("/s/"+shA.Slug, nil, false).Body.String()
	detailB := e.get("/s/"+shB.Slug, nil, false).Body.String()
	if !strings.Contains(detailA, ">马上下载<") || strings.Contains(detailA, "立即下载") {
		t.Fatal("default button text not applied to a resource without its own")
	}
	if !strings.Contains(detailB, ">获取<") || strings.Contains(detailB, "马上下载") {
		t.Fatal("per-resource button text must override the site default")
	}
	if !strings.Contains(detailA, "/s/"+shA.Slug+"/get") || !strings.Contains(detailA, "星河下载") {
		t.Fatal("download link or brand missing on detail")
	}
	// 素材路径公开可读且类型正确。
	for role, mime := range map[string]string{"logo": "image/png", "banner": "image/jpeg", "background": "image/webp"} {
		rec := e.get("/site/"+role, nil, false)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != mime || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("/site/%s: %d %q", role, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if e.get("/site/unknown", nil, false).Code != 404 || e.get("/site/..%2Ffilepanel.db", nil, false).Code != 404 {
		t.Fatal("unexpected site asset route behaviour")
	}

	// 文字被转义，强调色不能夹带样式，保留素材、移除其他素材。
	cfg := map[string]any{"site_name": `<script>alert(1)</script>`, "intro": "", "accent": "", "footer": "", "button_text": "",
		"assets": map[string]string{"logo": "keep", "banner": "remove"}}
	if rec := e.saveProfile(cfg, nil); rec.Code != 200 {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}
	home = e.get("/", nil, false).Body.String()
	if strings.Contains(home, "<script>alert(1)") || !strings.Contains(home, "&lt;script&gt;") {
		t.Fatal("site name not escaped")
	}
	if !strings.Contains(home, "/site/logo?v=2") || strings.Contains(home, "/site/banner") || strings.Contains(home, "--blue:#1d4ed8") {
		t.Fatal("keep/remove semantics wrong")
	}
	if e.get("/site/banner", nil, false).Code != 404 || e.get("/site/logo", nil, false).Code != 200 {
		t.Fatal("removed asset still served or kept asset lost")
	}
	for _, bad := range []string{"#fff", "red", "#000;}body{display:none"} {
		cfg["accent"] = bad
		if rec := e.saveProfile(cfg, nil); rec.Code != 400 {
			t.Fatalf("accent %q accepted: %d", bad, rec.Code)
		}
	}
}

func TestSiteProfileRequiresSessionAndCSRF(t *testing.T) {
	e := newEnv(t)
	body, ct := profileForm(t, brandedConfig(), brandedFiles())
	for _, tc := range []struct{ method, path string }{{"GET", "/api/profile"}, {"POST", "/api/profile"}, {"POST", "/api/profile/preview"}, {"GET", "/api/profile/export"},
		{"POST", "/api/profile/import"}, {"POST", "/api/profile/import/AAAAAAAAAAAAAAAAAAAAAAAA/apply"}, {"DELETE", "/api/profile/import/AAAAAAAAAAAAAAAAAAAAAAAA"}} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d", tc.method, tc.path, rec.Code)
		}
	}
	if rec := e.send(http.MethodPost, "/api/profile", body, ct, false); rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", rec.Code)
	}
	if home := e.get("/", nil, false).Body.String(); strings.Contains(home, "星河下载") {
		t.Fatal("rejected request changed the site")
	}
}

func TestSiteProfileExportImportBetweenInstancesPreviewsBeforeApplying(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	if rec := src.saveProfile(brandedConfig(), brandedFiles()); rec.Code != 200 {
		t.Fatalf("source save: %d %s", rec.Code, rec.Body.String())
	}
	exp := src.send(http.MethodGet, "/api/profile/export", nil, "", false)
	if exp.Code != 200 || exp.Header().Get("Content-Type") != "application/zip" || !strings.Contains(exp.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", exp.Code, exp.Header())
	}
	pkg := exp.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil || len(zr.File) != 4 {
		t.Fatalf("package entries: %v %v", len(zr.File), err)
	}

	// 目标实例有自己的文件、分享与按钮文字，导入不得改变它们。
	item := dst.upload("target.bin", content(4000, 9))
	sh, _, err := dst.deps.Shares.Update(item.ID, share.Input{Title: "目标资源", ButtonText: "拿走", Enabled: true, Featured: true})
	if err != nil {
		t.Fatal(err)
	}
	beforeHome := dst.get("/", nil, false).Body.String()
	if !strings.Contains(beforeHome, "FilePanel") {
		t.Fatal("target should start with the default look")
	}

	body, ct := packageForm(t, pkg)
	rec := dst.send(http.MethodPost, "/api/profile/import", body, ct, true)
	if rec.Code != 200 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var staged struct {
		Token  string `json:"token"`
		HTML   string `json:"html"`
		Assets []struct{ Role string }
		Fields struct {
			SiteName string `json:"site_name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Token == "" || staged.Fields.SiteName != "星河下载" || len(staged.Assets) != 3 || !strings.Contains(staged.HTML, "星河下载") || !strings.Contains(staged.HTML, "data:image/png;base64,") {
		t.Fatalf("import preview incomplete: %+v", staged.Fields)
	}
	if !strings.Contains(staged.HTML, "示例资源") || strings.Contains(staged.HTML, "目标资源") {
		t.Fatal("preview must use sample content, never real resources")
	}
	// 预览阶段：目标站点没有任何变化。
	if dst.get("/", nil, false).Body.String() != beforeHome || dst.get("/site/logo", nil, false).Code != 404 {
		t.Fatal("staged import changed the live site before confirmation")
	}

	if rec := dst.send(http.MethodPost, "/api/profile/import/"+staged.Token+"/apply", nil, "", true); rec.Code != 200 {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if rec := dst.send(http.MethodPost, "/api/profile/import/"+staged.Token+"/apply", nil, "", true); rec.Code != 400 {
		t.Fatalf("replayed token accepted: %d", rec.Code)
	}
	srcHome, dstHome := src.get("/", nil, false).Body.String(), dst.get("/", nil, false).Body.String()
	for _, part := range [][2]string{{"<style>", "</style>"}, {"<header", "</header>"}, {`<section class="hero">`, "</section>"}, {"<footer", "</footer>"}} {
		if a, b := between(srcHome, part[0], part[1]), between(dstHome, part[0], part[1]); a == "" || a != b {
			t.Fatalf("%s differs between instances", part[0])
		}
	}
	for _, role := range []string{"logo", "banner", "background"} {
		a, b := src.get("/site/"+role, nil, false), dst.get("/site/"+role, nil, false)
		if b.Code != 200 || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
			t.Fatalf("asset %s differs after import", role)
		}
	}
	// 目标站点的资源、分享与单资源按钮文字原样保留；默认按钮文字只影响没有自己设置的资源。
	detail := dst.get("/s/"+sh.Slug, nil, false).Body.String()
	if !strings.Contains(dstHome, "目标资源") || !strings.Contains(detail, ">拿走<") || strings.Contains(detail, "马上下载") {
		t.Fatal("target resource or its button text was disturbed")
	}
	var list []map[string]any
	if rec := dst.send(http.MethodGet, "/api/files", nil, "", false); json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 {
		t.Fatalf("target file list changed: %s", rec.Body.String())
	}
}

func TestSiteProfileImportRejectsBadPackagesOverHTTPAndKeepsConfig(t *testing.T) {
	e := newEnv(t)
	cfg := map[string]any{"site_name": "旧站", "intro": "", "accent": "", "footer": "", "button_text": "", "assets": map[string]string{}}
	if rec := e.saveProfile(cfg, nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"profile.json", "../../evil.txt"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(`{"format":"filepanel-profile","version":1}`))
	}
	_ = zw.Close()
	body, ct := packageForm(t, buf.Bytes())
	rec := e.send(http.MethodPost, "/api/profile/import", body, ct, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "配置包无效") {
		t.Fatalf("malicious package: %d %s", rec.Code, rec.Body.String())
	}
	body, ct = packageForm(t, []byte("not a zip"))
	if rec := e.send(http.MethodPost, "/api/profile/import", body, ct, true); rec.Code != 400 {
		t.Fatalf("garbage package: %d", rec.Code)
	}
	if rec := e.send(http.MethodPost, "/api/profile/import", body, ct, false); rec.Code != 403 {
		t.Fatalf("import without CSRF: %d", rec.Code)
	}
	if rec := e.send(http.MethodPost, "/api/profile/import/AAAAAAAAAAAAAAAAAAAAAAAA/apply", nil, "", true); rec.Code != 400 {
		t.Fatalf("apply of an unknown token: %d", rec.Code)
	}
	if home := e.get("/", nil, false).Body.String(); !strings.Contains(home, "旧站") {
		t.Fatal("rejected imports altered the site")
	}
	// 预览与放弃：放弃后令牌失效。
	good, _, _ := e.deps.Profile.Export()
	body, ct = packageForm(t, good)
	rec = e.send(http.MethodPost, "/api/profile/import", body, ct, true)
	var staged struct{ Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &staged)
	if rec.Code != 200 || staged.Token == "" {
		t.Fatalf("re-import of own export: %d", rec.Code)
	}
	if rec := e.send(http.MethodDelete, "/api/profile/import/"+staged.Token, nil, "", true); rec.Code != 200 {
		t.Fatalf("discard: %d", rec.Code)
	}
	if rec := e.send(http.MethodPost, "/api/profile/import/"+staged.Token+"/apply", nil, "", true); rec.Code != 400 {
		t.Fatalf("discarded token still applies: %d", rec.Code)
	}
}

func TestSiteProfileLowSpaceKeepsCurrentSiteOverHTTP(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a := auth.New(s)
	m, err := files.New(s, dir, 128<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := Build(s, a, m)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: s, files: m, deps: deps, now: time.Now()}
	e.handler = NewServer(deps, false)
	e.cookie, e.csrf = loginForTest(t, e.handler, a)
	if rec := e.saveProfile(brandedConfig(), brandedFiles()); rec.Code != 200 {
		t.Fatalf("initial save: %d %s", rec.Code, rec.Body.String())
	}
	logo := e.get("/site/logo", nil, false).Body.Bytes()

	// 同一数据目录以“磁盘安全空间耗尽”的预算重新装配服务（保留量远超可用空间）：
	// 保存和导入都应被受控拒绝，原站点完好。
	low, err := files.New(s, dir, 128<<20, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	lowDeps, err := Build(s, a, low)
	if err != nil {
		t.Fatal(err)
	}
	e.deps, e.handler = lowDeps, NewServer(lowDeps, false)
	cfg := brandedConfig()
	cfg["site_name"] = "不该生效"
	if rec := e.saveProfile(cfg, brandedFiles()); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("save under low space: %d %s", rec.Code, rec.Body.String())
	}
	pkg, _, _ := e.deps.Profile.Export()
	body, ct := packageForm(t, pkg)
	if rec := e.send(http.MethodPost, "/api/profile/import", body, ct, true); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("import under low space: %d %s", rec.Code, rec.Body.String())
	}
	if home := e.get("/", nil, false).Body.String(); !strings.Contains(home, "星河下载") || strings.Contains(home, "不该生效") {
		t.Fatal("site changed under low space")
	}
	if !bytes.Equal(e.get("/site/logo", nil, false).Body.Bytes(), logo) {
		t.Fatal("current logo damaged under low space")
	}
}
