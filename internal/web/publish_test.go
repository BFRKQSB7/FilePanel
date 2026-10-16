package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/share"
	"filepanel/internal/store"
)

type env struct {
	t       *testing.T
	store   *store.Store
	files   *files.Manager
	deps    Deps
	handler http.Handler
	cookie  string
	csrf    string
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
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
	e := &env{t: t, store: s, files: m, now: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	deps, err := Build(s, a, m)
	if err != nil {
		t.Fatal(err)
	}
	e.deps = deps
	deps.Download.Now = func() time.Time { return e.now }
	e.handler = NewServer(deps, false)
	e.cookie, e.csrf = loginForTest(t, e.handler, a)
	return e
}

func content(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) ^ seed ^ byte(i>>8)
	}
	return b
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (e *env) upload(name string, data []byte) files.Item {
	e.t.Helper()
	item, err := e.files.Upload(context.Background(), name, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	return item
}

func (e *env) publish(id, title string, featured bool) share.Share {
	e.t.Helper()
	sh, _, err := e.deps.Shares.Update(id, share.Input{Title: title, Enabled: true, Featured: featured})
	if err != nil {
		e.t.Fatal(err)
	}
	return sh
}

func (e *env) token(sh share.Share, ip string) string {
	tok, _ := e.deps.Download.Issue(download.Claims{File: sh.FileID, SHA: sh.SHA256, AuthVersion: sh.AuthVersion, Scope: "public", IP: ip})
	return tok
}

func (e *env) get(path string, headers map[string]string, cookie bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.7:4000"
	req.Header.Set("Accept", "text/html")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestShareLifecycleHidesPrivateAndInvalidatesOldCredentials(t *testing.T) {
	e := newEnv(t)
	data := content(300_000, 1)
	item := e.upload("setup.bin", data)
	sh, err := e.deps.Shares.Ensure(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Enabled || sh.Featured {
		t.Fatal("uploads must default to private")
	}
	// 私有：匿名主页、介绍页、搜索、计数与内容入口都看不到。
	if rec := e.get("/", nil, false); rec.Code != 200 || strings.Contains(rec.Body.String(), "setup.bin") || !strings.Contains(rec.Body.String(), "还没有公开资源") {
		t.Fatalf("private file leaked on homepage: %d", rec.Code)
	}
	if _, total, _ := e.deps.Shares.Public("setup", 1); total != 0 {
		t.Fatal("private record counted in anonymous search")
	}
	if rec := e.get("/s/"+sh.Slug, nil, false); rec.Code != 404 {
		t.Fatalf("private intro page: %d", rec.Code)
	}
	if rec := e.get("/d/"+item.ID+"?t="+e.token(sh, "203.0.113.7"), nil, false); rec.Code != 410 {
		t.Fatalf("token for private share must not work: %d", rec.Code)
	}
	// 主页展示蕴含分享开启。
	live, _, err := e.deps.Shares.Update(item.ID, share.Input{Title: "安装包", Featured: true})
	if err != nil || !live.Enabled || !live.Featured {
		t.Fatalf("featured must imply enabled: %+v %v", live, err)
	}
	if rec := e.get("/", nil, false); !strings.Contains(rec.Body.String(), "安装包") {
		t.Fatal("featured share missing from homepage")
	}
	// 改名不改 slug。
	if err := e.files.Rename(item.ID, "renamed.bin"); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.deps.Shares.Get(item.ID); again.Slug != sh.Slug {
		t.Fatal("slug changed after rename")
	}
	// 通过介绍页 → 跳转 → 下载，哈希一致。
	link := e.get("/s/"+live.Slug+"/get", nil, false)
	if link.Code != http.StatusFound {
		t.Fatalf("get link: %d %s", link.Code, link.Body.String())
	}
	dl := e.get(link.Header().Get("Location"), nil, false)
	if dl.Code != 200 || sum(dl.Body.Bytes()) != sum(data) {
		t.Fatalf("download %d, hash mismatch", dl.Code)
	}
	oldToken := e.token(live, "203.0.113.7")
	// 关闭分享：取消展示、授权版本 +1、旧凭证失效。
	closed, revoked, err := e.deps.Shares.Update(item.ID, share.Input{Title: "安装包"})
	if err != nil || !revoked || closed.Enabled || closed.Featured || closed.AuthVersion != live.AuthVersion+1 {
		t.Fatalf("close share: %+v revoked=%v err=%v", closed, revoked, err)
	}
	if rec := e.get("/d/"+item.ID+"?t="+oldToken, nil, false); rec.Code != 410 {
		t.Fatalf("old credential after close: %d", rec.Code)
	}
	if rec := e.get("/s/"+live.Slug, nil, false); rec.Code != 404 {
		t.Fatalf("intro page after close: %d", rec.Code)
	}
	// 重新开启后旧凭证仍然无效（授权版本不同）。
	if _, _, err := e.deps.Shares.Update(item.ID, share.Input{Title: "安装包", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/d/"+item.ID+"?t="+oldToken, nil, false); rec.Code != 410 && rec.Code != 403 {
		t.Fatalf("old credential after reopen: %d", rec.Code)
	}
}

// stalledDownload 打开下载后只读一点点，然后停住，模拟慢速/外部下载器。
func stalledDownload(t *testing.T, url string) (*http.Response, func() (int64, error)) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("download status %d", resp.StatusCode)
	}
	first := make([]byte, 1024)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	return resp, func() (int64, error) {
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return n + 1024, err
	}
}

func waitLeases(t *testing.T, e *env, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for e.deps.Download.ActiveLeases() != 0 {
		if time.Since(start) > limit {
			t.Fatalf("active response still running after %s", limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start)
}

func TestActiveResponseStopsWithinBoundAfterRevocation(t *testing.T) {
	e := newEnv(t)
	data := content(48<<20, 7)
	item := e.upload("big.bin", data)
	sh := e.publish(item.ID, "big", true)
	e.deps.Download.RenewEvery = 50 * time.Millisecond
	e.deps.Download.Now = time.Now
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	url := srv.URL + "/d/" + item.ID + "?t=" + e.token(sh, "127.0.0.1")

	// 1) 仅靠周期重新校验（模拟错过了即时撤销通知）。
	_, rest := stalledDownload(t, url)
	if e.deps.Download.ActiveLeases() != 1 {
		t.Fatal("expected one active lease")
	}
	if _, _, err := e.deps.Shares.Update(item.ID, share.Input{Title: "big"}); err != nil {
		t.Fatal(err)
	}
	took := waitLeases(t, e, 3*time.Second)
	got, _ := rest()
	st, _ := e.deps.Meter.Status()
	if got >= int64(len(data)) || st.Period.Sent >= int64(len(data)) {
		t.Fatalf("revoked response still delivered everything: client=%d metered=%d", got, st.Period.Sent)
	}
	t.Logf("periodic revalidation stopped the response in %s", took)

	// 2) 立即撤销路径（管理员关闭分享时调用 Revoke）。
	sh2 := e.publish(item.ID, "big", true)
	url = srv.URL + "/d/" + item.ID + "?t=" + e.token(sh2, "127.0.0.1")
	e.deps.Download.RenewEvery = time.Hour // 启动响应前配置，排除周期校验且避免与监控协程竞争
	_, rest = stalledDownload(t, url)
	_ = e.deps.Shares.Revoke(item.ID)
	if n := e.deps.Download.Revoke(item.ID); n < 1 {
		t.Fatalf("Revoke hit %d leases", n)
	}
	waitLeases(t, e, time.Second)
	_, _ = rest()
}

func TestLeaseExpiryBoundsStalledResponse(t *testing.T) {
	e := newEnv(t)
	data := content(48<<20, 9)
	item := e.upload("stall.bin", data)
	sh := e.publish(item.ID, "stall", true)
	d := e.deps.Download
	d.Now = time.Now
	d.LeaseTTL, d.RenewEvery, d.Skew = 200*time.Millisecond, time.Hour, 0 // 永不续租 → 租约到期即终止
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	_, rest := stalledDownload(t, srv.URL+"/d/"+item.ID+"?t="+e.token(sh, "127.0.0.1"))
	took := waitLeases(t, e, 3*time.Second)
	got, _ := rest()
	if got >= int64(len(data)) {
		t.Fatal("expired lease still delivered the full file")
	}
	t.Logf("lease expiry stopped the response in %s", took)
}
