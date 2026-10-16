package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filepanel/internal/share"
	"filepanel/internal/usage"
)

func TestRangeIfRangeAndResumeAfterTokenExpiry(t *testing.T) {
	e := newEnv(t)
	data := content(200_000, 3)
	item := e.upload("pkg.bin", data)
	sh := e.publish(item.ID, "pkg", true)
	ip := "203.0.113.7"
	url := func() string { return "/d/" + item.ID + "?t=" + e.token(sh, ip) }
	etag := `"` + sum(data) + `"`

	rec := e.get(url(), map[string]string{"Range": "bytes=100-199"}, false)
	if rec.Code != 206 || rec.Header().Get("Content-Range") != "bytes 100-199/200000" || !bytes.Equal(rec.Body.Bytes(), data[100:200]) {
		t.Fatalf("single range: %d %q", rec.Code, rec.Header().Get("Content-Range"))
	}
	if rec = e.get(url(), map[string]string{"Range": "bytes=-50"}, false); rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), data[len(data)-50:]) {
		t.Fatalf("tail range: %d", rec.Code)
	}
	if rec = e.get(url(), map[string]string{"Range": "bytes=199990-"}, false); rec.Code != 206 || rec.Body.Len() != 10 {
		t.Fatalf("open range: %d len=%d", rec.Code, rec.Body.Len())
	}
	for _, bad := range []string{"bytes=0-1,5-9", "bytes=abc", "bytes=500-100", "bytes=999999999-", "items=0-5", "bytes=-0", "bytes=1-2-3"} {
		rec = e.get(url(), map[string]string{"Range": bad}, false)
		if rec.Code != 416 || rec.Header().Get("Content-Range") != "bytes */200000" || rec.Body.Len() != 0 {
			t.Fatalf("Range %q => %d %q", bad, rec.Code, rec.Header().Get("Content-Range"))
		}
	}
	// If-Range 不匹配 → 忽略 Range，发送完整 200；匹配 → 206。
	if rec = e.get(url(), map[string]string{"Range": "bytes=0-9", "If-Range": `"stale"`}, false); rec.Code != 200 || rec.Body.Len() != len(data) {
		t.Fatalf("If-Range mismatch: %d len=%d", rec.Code, rec.Body.Len())
	}
	if rec = e.get(url(), map[string]string{"Range": "bytes=0-9", "If-Range": etag}, false); rec.Code != 206 {
		t.Fatalf("If-Range match: %d", rec.Code)
	}
	// HEAD 只返回头，不计入用量。
	req := httptest.NewRequest(http.MethodHead, url(), nil)
	req.RemoteAddr = ip + ":1"
	head := httptest.NewRecorder()
	e.handler.ServeHTTP(head, req)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "200000" || head.Header().Get("ETag") != etag {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}

	// 续传：先取前 70000 字节，初次凭证过期后重新授权，用 If-Range 取剩余并拼接。
	first := e.get(url(), map[string]string{"Range": "bytes=0-69999"}, false)
	stale := url()
	e.now = e.now.Add(6 * time.Minute) // 超过 300 秒 + 5 秒容限
	if rec = e.get(stale, nil, false); rec.Code != 410 {
		t.Fatalf("expired credential: %d", rec.Code)
	}
	link := e.get("/s/"+sh.Slug+"/get", nil, false)
	if link.Code != http.StatusFound {
		t.Fatalf("re-authorize: %d", link.Code)
	}
	rest := e.get(link.Header().Get("Location"), map[string]string{"Range": "bytes=70000-", "If-Range": etag}, false)
	if rest.Code != 206 {
		t.Fatalf("resume: %d", rest.Code)
	}
	if sum(append(append([]byte{}, first.Body.Bytes()...), rest.Body.Bytes()...)) != sum(data) {
		t.Fatal("resumed bytes do not reproduce the original hash")
	}
}

func TestCredentialIsBoundToFileSourceAndSignature(t *testing.T) {
	e := newEnv(t)
	a := e.upload("a.bin", content(5000, 1))
	b := e.upload("b.bin", content(5000, 2))
	shA, shB := e.publish(a.ID, "a", true), e.publish(b.ID, "b", true)
	tokA := e.token(shA, "203.0.113.7")
	if rec := e.get("/d/"+b.ID+"?t="+tokA, nil, false); rec.Code != 403 {
		t.Fatalf("credential for file A used on file B: %d", rec.Code)
	}
	if rec := e.get("/d/"+a.ID+"?t="+e.token(shA, "198.51.100.1"), nil, false); rec.Code != 403 {
		t.Fatalf("credential from another source: %d", rec.Code)
	}
	if rec := e.get("/d/"+a.ID+"?t="+tokA[:len(tokA)-3]+"AAA", nil, false); rec.Code != 403 {
		t.Fatalf("tampered signature: %d", rec.Code)
	}
	if rec := e.get("/d/"+a.ID, nil, false); rec.Code != 403 {
		t.Fatalf("no credential: %d", rec.Code)
	}
	// Referer 不能替代凭证。
	if rec := e.get("/d/"+a.ID, map[string]string{"Referer": "http://example.test/s/" + shB.Slug}, false); rec.Code != 403 {
		t.Fatalf("referer accepted: %d", rec.Code)
	}
	if rec := e.get("/d/"+a.ID+"?t="+tokA, nil, false); rec.Code != 200 {
		t.Fatalf("valid credential: %d", rec.Code)
	}
}

func TestAdminDownloadIsMeteredAndStopsAtLine(t *testing.T) {
	e := newEnv(t)
	data := content(300_000, 5)
	item := e.upload("admin.bin", data)
	cfg := usage.DefaultConfig()
	cfg.QuotaBytes = 100_000 // 执行线 95_000
	cfg.IPPercent = 100
	if err := e.deps.Meter.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/api/files/"+item.ID, nil, true)
	if rec.Code != 200 || rec.Body.Len() > 95_000 || rec.Body.Len() == 0 {
		t.Fatalf("admin download delivered %d bytes (status %d); must stop at the line", rec.Body.Len(), rec.Code)
	}
	st, _ := e.deps.Meter.Status()
	var admin int64
	for _, c := range st.Categories {
		if c.Category == usage.CategoryAdmin && c.Direction == "sent" {
			admin = c.Bytes
		}
	}
	if admin != int64(rec.Body.Len()) || st.Period.Sent != admin {
		t.Fatalf("admin bytes not metered: delivered=%d metered=%d total=%d", rec.Body.Len(), admin, st.Period.Sent)
	}
	if rec = e.get("/api/files/"+item.ID, nil, true); rec.Code != 503 {
		t.Fatalf("admin download after the line: %d (quota must not be silently bypassed)", rec.Code)
	}
	// 访客页面保持可浏览但下载按钮停用并给出说明。
	sh := e.publish(item.ID, "admin", true)
	page := e.get("/s/"+sh.Slug, nil, false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "保护线") || !strings.Contains(page.Body.String(), `aria-disabled="true"`) {
		t.Fatalf("exhausted state not explained: %d", page.Code)
	}
	if rec = e.get("/s/"+sh.Slug+"/get", nil, false); rec.Code != 503 {
		t.Fatalf("visitor link at the line: %d", rec.Code)
	}
}

func multipartImage(t *testing.T, field, name string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(body)
	w.Close()
	return &buf, w.FormDataContentType()
}

func (e *env) postAsset(id, name string, body []byte) *httptest.ResponseRecorder {
	buf, ctype := multipartImage(e.t, "image", name, body)
	req := httptest.NewRequest(http.MethodPost, "/api/files/"+id+"/assets", buf)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xfc\xff\x9f\xa1\x1e\x00\x07\x82\x02\x7f=\xc8H\xef\x00\x00\x00\x00IEND\xaeB`\x82")

func TestDangerousMarkdownAndAssetBoundaries(t *testing.T) {
	e := newEnv(t)
	a := e.upload("a.bin", content(1000, 1))
	b := e.upload("b.bin", content(1000, 2))
	if rec := e.postAsset(a.ID, "x.png", tinyPNG); rec.Code != 201 {
		t.Fatalf("png upload: %d %s", rec.Code, rec.Body.String())
	}
	var assetA share.Asset
	assets, _ := e.deps.Shares.Assets(a.ID)
	assetA = assets[0]
	// 脚本、SVG、伪装成图片的 HTML 都不能成为素材。
	for name, body := range map[string][]byte{
		"x.svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"x.png":  []byte(`<html><script>alert(1)</script></html>`),
		"x.html": []byte(`<script>alert(1)</script>`),
	} {
		if rec := e.postAsset(a.ID, name, body); rec.Code != 400 {
			t.Fatalf("%s accepted as asset: %d", name, rec.Code)
		}
	}
	// 管理员能读素材；匿名只能读已开启分享的素材。
	if rec := e.get("/api/assets/"+assetA.ID, nil, true); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("admin asset: %d %v", rec.Code, rec.Header())
	}
	if rec := e.get("/a/"+assetA.ID, nil, false); rec.Code != 404 {
		t.Fatalf("private asset served to anonymous visitor: %d", rec.Code)
	}
	if rec := e.get("/api/assets/"+assetA.ID, nil, false); rec.Code != 401 {
		t.Fatalf("admin asset without session: %d", rec.Code)
	}
	for _, p := range []string{"/a/..%2F..%2Ffilepanel.db", "/a/../../etc/passwd", "/a/" + strings.Repeat("A", 24)} {
		if rec := e.get(p, nil, false); rec.Code == 200 || rec.Body.Len() > 0 && !strings.Contains(rec.Body.String(), "Temporary Redirect") && !strings.Contains(rec.Body.String(), "not found") && !strings.Contains(rec.Body.String(), "Moved") {
			t.Fatalf("%s => %d", p, rec.Code)
		}
	}
	md := "# 标题\n<script>alert(1)</script>\n\n[点我](javascript:alert(1)) [ok](https://example.com/a?b=1) [d](data:text/html,<script>1</script>)\n\n" +
		"![本分享](asset:" + assetA.ID + ") ![外链](https://evil.test/x.png) ![别人的](asset:" + strings.Repeat("Z", 24) + ")\n\n" +
		"<img src=x onerror=alert(1)> **粗** `<b>` \n\n```\n<script>x</script>\n```\n"
	if _, _, err := e.deps.Shares.Update(a.ID, share.Input{Title: `<b>t</b>`, Summary: `<img src=x onerror=1>`, Markdown: md, Enabled: true, Featured: true}); err != nil {
		t.Fatal(err)
	}
	sh, _ := e.deps.Shares.Get(a.ID)
	page := e.get("/s/"+sh.Slug, nil, false).Body.String()
	for _, banned := range []string{"<script>alert", "<script>x", "javascript:", "href=\"data:", "href=\"javascript", "evil.test", "<img src=x", "<b>t</b>"} {
		if strings.Contains(page, banned) {
			t.Fatalf("unsafe output %q in public page", banned)
		}
	}
	if !strings.Contains(page, "&lt;img src=x onerror") {
		t.Fatal("raw HTML in markdown must be shown escaped, not dropped silently or executed")
	}
	if !strings.Contains(page, `src="/a/`+assetA.ID+`"`) || !strings.Contains(page, `href="https://example.com/a?b=1"`) {
		t.Fatal("safe content was removed")
	}
	// 别的分享不能引用本分享素材；本分享开启后匿名可读。
	if got := share.RenderMarkdown("![x](asset:"+assetA.ID+")", e.deps.Shares.AssetOwned(b.ID)); strings.Contains(got, "<img") {
		t.Fatal("asset of another share can be embedded")
	}
	if rec := e.get("/a/"+assetA.ID, nil, false); rec.Code != 200 {
		t.Fatalf("asset of enabled share: %d", rec.Code)
	}
}

func TestMarkdownPreviewSanitizesAndWritesNeedCSRF(t *testing.T) {
	e := newEnv(t)
	a := e.upload("a.bin", content(1000, 1))
	body, _ := json.Marshal(map[string]string{"file_id": a.ID, "markdown": "<script>1</script>[x](javascript:1)"})
	req := httptest.NewRequest(http.MethodPost, "/api/markdown/preview", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	var out struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(out.HTML, "<script") || strings.Contains(out.HTML, "javascript:") || strings.Contains(out.HTML, "href=") {
		t.Fatalf("unsafe preview: %s", out.HTML)
	}
	req = httptest.NewRequest(http.MethodPut, "/api/files/"+a.ID+"/share", strings.NewReader(`{"enabled":true}`))
	req.RemoteAddr = "127.0.0.1:1"
	req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	rec = httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("share write without CSRF: %d", rec.Code)
	}
	if got, _ := e.deps.Shares.Get(a.ID); got.Enabled {
		t.Fatal("share was enabled without CSRF")
	}
}
