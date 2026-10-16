package profile

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"filepanel/internal/store"
)

// ---- 测试夹具 ----

type okSpace struct{}

func (okSpace) CheckSpace(uint64) error { return nil }

type noSpace struct{}

func (noSpace) CheckSpace(uint64) error {
	return errors.New("可用空间不足，已保留系统安全空间")
}

func fill(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func pngOf(w, h int, c color.RGBA) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, fill(w, h, c)); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func jpegOf(w, h int, c color.RGBA) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, fill(w, h, c), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func gifOf() []byte {
	var b bytes.Buffer
	if err := gif.Encode(&b, fill(4, 4, color.RGBA{1, 2, 3, 255}), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// 1x1 无损 WebP（常见的特性检测样本）。
var webpOf = func() []byte {
	b, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		panic(err)
	}
	return b
}()

func openMgr(t *testing.T, dir string, space Space) (*Manager, *store.Store) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m, err := New(s, dir, space)
	if err != nil {
		t.Fatal(err)
	}
	return m, s
}

// reopen 模拟进程重启：关闭数据库后重新打开同一个数据目录。
func reopen(t *testing.T, dir string, old *store.Store) *Manager {
	t.Helper()
	_ = old.Close()
	m, _ := openMgr(t, dir, okSpace{})
	return m
}

func sampleCandidate(name string, seed uint8) Candidate {
	return Candidate{
		Fields: Fields{SiteName: name, Intro: "简介 " + name, Accent: "#1d4ed8", Footer: "© " + name, ButtonText: "马上下载"},
		Blobs: map[Role]Blob{
			Logo:       {Mime: "image/png", Data: pngOf(40+int(seed), 20, color.RGBA{seed, 100, 200, 255})},
			Banner:     {Mime: "image/jpeg", Data: jpegOf(300, 100, color.RGBA{200, seed, 50, 255})},
			Background: {Mime: "image/webp", Data: webpOf},
		},
	}
}

func mustCommit(t *testing.T, m *Manager, c Candidate) View {
	t.Helper()
	v, err := m.Commit(c)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func assetSum(t *testing.T, m *Manager, role Role) string {
	t.Helper()
	_, data, err := m.Asset(role)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:6])
}

func tree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "filepanel.db") { // SQLite 自身的 WAL 文件会变化
			return nil
		}
		info, _ := d.Info()
		size := int64(-1)
		if info != nil && !d.IsDir() {
			size = info.Size()
		}
		out[filepath.ToSlash(rel)] = size
		return nil
	})
	return out
}

func subdirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ---- 1. round-trip ----

func TestPackageRoundTripIsFaithfulAndContainsNothingSensitive(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a, sa := openMgr(t, dirA, okSpace{})
	b, sb := openMgr(t, dirB, okSpace{})

	// 两个实例都有“不属于配置包”的数据：设置项（含密码哈希样式的秘密）与文件记录。
	const secret = "argon2id$SECRET-HASH-MUST-NOT-LEAK"
	for _, s := range []*store.Store{sa, sb} {
		if err := s.SetSetting("admin_password_hash", secret); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec("INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) VALUES('f1','k1','秘密文件名.bin',3,'abc','available',1,1)"); err != nil {
			t.Fatal(err)
		}
	}
	want := sampleCandidate("星河下载", 7)
	viewA := mustCommit(t, a, want)

	pkg, rev, err := a.Export()
	if err != nil || rev != 1 {
		t.Fatalf("export: rev=%d err=%v", rev, err)
	}
	if bytes.Contains(pkg, []byte(secret)) || bytes.Contains(pkg, []byte("秘密文件名")) {
		t.Fatal("package leaked non-profile data")
	}
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"assets/background.webp", "assets/banner.jpg", "assets/logo.png", "profile.json"}) {
		t.Fatalf("unexpected package entries: %v", names)
	}
	rc, _ := zr.File[0].Open()
	var raw map[string]any
	if zr.File[0].Name != "profile.json" {
		t.Fatalf("profile.json must come first, got %s", zr.File[0].Name)
	}
	if err := json.NewDecoder(rc).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	keys := []string{}
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"accent", "assets", "button_text", "footer", "format", "intro", "site_name", "version"}) {
		t.Fatalf("profile.json carries unexpected keys: %v", keys)
	}

	// 导入到另一个独立实例：先预览（暂存），确认前目标不变。
	cand, err := ParsePackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	beforeView := b.View()
	token, err := b.Stage(cand)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.View(), beforeView) {
		t.Fatal("staging must not change the live profile")
	}
	if _, err := b.Apply(token); err != nil {
		t.Fatal(err)
	}
	if got := b.View(); !reflect.DeepEqual(got, viewA) {
		t.Fatalf("imported view differs:\n got %+v\nwant %+v", got, viewA)
	}
	if !reflect.DeepEqual(a.Snapshot().Fields, b.Snapshot().Fields) {
		t.Fatal("fields not preserved")
	}
	for _, role := range Roles {
		if assetSum(t, a, role) == "" || assetSum(t, a, role) != assetSum(t, b, role) {
			t.Fatalf("asset %s not byte-identical", role)
		}
	}
	// 再导出应逐字节相同（格式确定、无时间戳漂移）。
	again, _, _ := b.Export()
	if !bytes.Equal(pkg, again) {
		t.Fatal("re-export is not byte-identical")
	}
	// 目标实例的文件与设置完全不受影响。
	var n int
	if err := sb.DB.QueryRow("SELECT COUNT(*) FROM files WHERE id='f1' AND display_name='秘密文件名.bin'").Scan(&n); err != nil || n != 1 {
		t.Fatal("target files changed by import")
	}
	if v, ok, _ := sb.Setting("admin_password_hash"); !ok || v != secret {
		t.Fatal("target settings changed by import")
	}
}

func TestDefaultsStayEmptyAndEmptyProfileRoundTrips(t *testing.T) {
	m, _ := openMgr(t, t.TempDir(), okSpace{})
	v := m.View()
	if v.SiteName != DefaultSiteName || v.ButtonText != DefaultButtonText || v.Footer != DefaultFooter || v.LogoURL != "" || v.CSS != "" {
		t.Fatalf("defaults wrong: %+v", v)
	}
	pkg, _, err := m.Export()
	if err != nil {
		t.Fatal(err)
	}
	cand, err := ParsePackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if cand.Fields != (Fields{}) || len(cand.Blobs) != 0 {
		t.Fatalf("empty profile must stay empty (defaults must not be baked in): %+v", cand)
	}
}

// ---- 2. 恶意/损坏包 ----

type ent struct {
	name  string
	data  []byte
	mode  os.FileMode
	store bool
}

func zipOf(ents ...ent) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write(e.data); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func docOf(mutate func(map[string]any)) []byte {
	m := map[string]any{"format": FormatName, "version": 1, "site_name": "站", "intro": "", "accent": "", "footer": "", "button_text": "",
		"assets": map[string]string{"logo": "assets/logo.png"}}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return b
}

var goodLogo = pngOf(16, 16, color.RGBA{10, 20, 30, 255})

func goodEnts() []ent {
	return []ent{{name: "profile.json", data: docOf(nil)}, {name: "assets/logo.png", data: goodLogo, store: true}}
}

// lyingZip 的 ZIP 头声明的解压大小远小于真实内容。
func lyingZip(name string, real []byte, declared uint64) []byte {
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	_, _ = fw.Write(real)
	_ = fw.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(real), CompressedSize64: uint64(comp.Len()), UncompressedSize64: declared}
	h.SetMode(0o644)
	w, err := zw.CreateRaw(h)
	if err != nil {
		panic(err)
	}
	_, _ = w.Write(comp.Bytes())
	_ = zw.Close()
	return buf.Bytes()
}

func TestMaliciousPackagesAreRejectedWithoutWritingAnything(t *testing.T) {
	if _, err := ParsePackage(zipOf(goodEnts()...)); err != nil {
		t.Fatalf("baseline package must be valid: %v", err)
	}
	many := goodEnts()
	for i := 0; i < 10; i++ {
		many = append(many, ent{name: fmt.Sprintf("assets/x%d.png", i), data: goodLogo})
	}
	good := zipOf(goodEnts()...)
	cases := []struct {
		name string
		pkg  []byte
		want string
	}{
		{"路径穿越", zipOf(append(goodEnts(), ent{name: "../evil.txt", data: []byte("x")})...), "条目路径包含 .."},
		{"嵌套穿越", zipOf(append(goodEnts(), ent{name: "assets/../../evil.png", data: []byte("x")})...), "条目路径包含 .."},
		{"绝对路径", zipOf(append(goodEnts(), ent{name: "/etc/passwd", data: []byte("x")})...), "绝对路径"},
		{"盘符路径", zipOf(append(goodEnts(), ent{name: "C:/evil.txt", data: []byte("x")})...), "绝对路径"},
		{"反斜杠穿越", zipOf(append(goodEnts(), ent{name: `..\evil.txt`, data: []byte("x")})...), "反斜杠"},
		{"未规范化双斜杠", zipOf(append(goodEnts(), ent{name: "assets//logo.png", data: goodLogo})...), "未规范化"},
		{"未规范化点段", zipOf(append(goodEnts(), ent{name: "./profile.json", data: docOf(nil)})...), "未规范化"},
		{"控制字符", zipOf(append(goodEnts(), ent{name: "assets/lo\x00go.png", data: goodLogo})...), "控制字符"},
		{"重复 profile.json", zipOf(append(goodEnts(), ent{name: "profile.json", data: docOf(nil)})...), "重复"},
		{"重复素材", zipOf(append(goodEnts(), ent{name: "assets/logo.png", data: goodLogo})...), "重复"},
		{"大小写重复", zipOf(append(goodEnts(), ent{name: "PROFILE.JSON", data: docOf(nil)})...), "不允许的文件"},
		{"符号链接", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777}), "符号链接"},
		{"设备文件", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: nil, mode: os.ModeDevice | 0o644}), "不是普通文件"},
		{"条目过多", zipOf(many...), "条目过多"},
		{"目录条目", zipOf(append(goodEnts(), ent{name: "evil/", mode: os.ModeDir | 0o755})...), "不允许的文件"},
		{"非白名单文件", zipOf(append(goodEnts(), ent{name: "evil.html", data: []byte("<script>1</script>")})...), "不允许的文件"},
		{"SVG 文件", zipOf(append(goodEnts(), ent{name: "assets/logo.svg", data: []byte("<svg/>")})...), "不允许的文件"},
		{"未知素材用途", zipOf(append(goodEnts(), ent{name: "assets/extra.png", data: goodLogo})...), "不允许的文件"},
		{"profile.json 超限", zipOf(ent{name: "profile.json", data: bytes.Repeat([]byte(" "), 70<<10)}), "超过"},
		{"素材超限", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: bytes.Repeat([]byte{7, 8, 9, 10, 11, 13, 17}, 90<<10), store: true}), "超过"},
		{"压缩比炸弹", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: make([]byte, 400<<10)}), "压缩比"},
		{"ZIP 头谎报大小", lyingZip("profile.json", bytes.Repeat([]byte("a"), 4000), 5), "声明"},
		{"ZIP 头谎报巨大", lyingZip("profile.json", []byte("{}"), 1<<40), "声明大小"},
		{"不是 ZIP", []byte("this is not a zip file at all"), "不是有效的 ZIP"},
		{"截断的 ZIP", good[:len(good)/2], "不是有效的 ZIP"},
		{"空文件", nil, "不是有效的 ZIP"},
		{"整包超限", append(append([]byte(nil), good...), make([]byte, MaxPackageBytes)...), "超过"},
		{"缺少 profile.json", zipOf(ent{name: "assets/logo.png", data: goodLogo}), "缺少 profile.json"},
		{"JSON 损坏", zipOf(ent{name: "profile.json", data: []byte("{not json")}), "不是有效的 JSON"},
		{"格式标识错误", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["format"] = "other" })}), "不是 FilePanel"},
		{"未来版本", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["version"] = 2; m["brand_new_field"] = true })}), "版本 2"},
		{"版本 0", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["version"] = 0 })}), "版本 0"},
		{"未知字段", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["css"] = "body{display:none}" })}), "未知"},
		{"强调色格式", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["accent"] = "red" })}), "强调色"},
		{"强调色过浅", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["accent"] = "#ffee00" })}), "太浅"},
		{"站点名过长", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["site_name"] = strings.Repeat("长", 41) })}), "站点名"},
		{"引用不存在的素材", zipOf(ent{name: "profile.json", data: docOf(nil)}), "不存在"},
		{"引用路径非规范", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"logo": "logo.png"} })}, ent{name: "assets/logo.png", data: goodLogo}), "不是规范的"},
		{"引用角色错配", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"banner": "assets/logo.png"} })}, ent{name: "assets/logo.png", data: goodLogo}), "不是规范的"},
		{"引用穿越", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"logo": "../assets/logo.png"} })}, ent{name: "assets/logo.png", data: goodLogo}), "不是规范的"},
		{"未知素材键", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"icon": "assets/logo.png"} })}, ent{name: "assets/logo.png", data: goodLogo}), "未知的素材用途"},
		{"未被引用的素材", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{} })}, ent{name: "assets/logo.png", data: goodLogo}), "没有被 profile.json 引用"},
		{"伪装成 PNG 的 SVG", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)}), "仅支持"},
		{"GIF 不被接受", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: gifOf()}), "仅支持"},
		{"扩展名与内容不符", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"logo": "assets/logo.jpg"} })}, ent{name: "assets/logo.jpg", data: goodLogo}), "扩展名"},
		{"PNG 头损坏", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)}), "不是有效"},
		{"图片尺寸过大", zipOf(ent{name: "profile.json", data: docOf(nil)}, ent{name: "assets/logo.png", data: pngOf(5000, 1, color.RGBA{1, 2, 3, 255})}), "尺寸"},
		{"WebP 夹带数据", zipOf(ent{name: "profile.json", data: docOf(func(m map[string]any) { m["assets"] = map[string]string{"background": "assets/background.webp"} })}, ent{name: "assets/background.webp", data: append(append([]byte(nil), webpOf...), []byte("EXTRA")...)}), "不是有效"},
	}
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	m, _ := openMgr(t, dir, okSpace{})
	mustCommit(t, m, sampleCandidate("旧站", 3)) // 现有配置不得被任何恶意包影响
	oldView, oldSum := m.View(), assetSum(t, m, Logo)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tree(t, base)
			cand, err := ParsePackage(tc.pkg)
			if err == nil {
				// 万一解析通过，也必须走完整暂存路径验证“暂存外无写入”。
				_, _ = m.Stage(cand)
				t.Fatalf("malicious package was accepted")
			}
			var ue *UserError
			if !errors.As(err, &ue) || !strings.Contains(ue.Msg, tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if after := tree(t, base); !reflect.DeepEqual(before, after) {
				t.Fatalf("file system changed while rejecting a bad package:\nbefore=%v\nafter=%v", before, after)
			}
			for _, evil := range []string{"evil.txt", "evil.png", "passwd"} {
				if _, err := os.Stat(filepath.Join(base, evil)); err == nil {
					t.Fatalf("%s escaped the staging area", evil)
				}
			}
		})
	}
	if !reflect.DeepEqual(m.View(), oldView) || assetSum(t, m, Logo) != oldSum {
		t.Fatal("rejected packages altered the live profile")
	}
}

func TestStageWritesOnlyInsideStagingArea(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	m, _ := openMgr(t, dir, okSpace{})
	before := tree(t, base)
	cand, err := ParsePackage(zipOf(goodEnts()...))
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.Stage(cand)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "data/profile/staging/" + token
	for path := range tree(t, base) {
		if _, existed := before[path]; existed || path == "data/profile/staging" {
			continue
		}
		if path != prefix && !strings.HasPrefix(path, prefix+"/") {
			t.Fatalf("staging wrote outside its area: %s", path)
		}
	}
	m.Discard(token)
	if !reflect.DeepEqual(tree(t, base), before) {
		t.Fatal("discard did not remove the staged files")
	}
}

// ---- 3. 原子切换：故障、崩溃、低空间 ----

func TestApplyFailureOrCrashKeepsOldOrNewCompleteConfig(t *testing.T) {
	oldC, newC := sampleCandidate("旧站", 1), sampleCandidate("新站", 9)
	type result int
	const (
		wantOld result = iota
		wantNew
	)
	crash := func() error { panic("simulated crash") }
	cases := []struct {
		name  string
		hook  func(*Manager)
		panic bool
		want  result
	}{
		{"重命名后失败", func(m *Manager) { m.hooks.afterRename = func() error { return errors.New("disk error") } }, false, wantOld},
		{"提交前失败", func(m *Manager) { m.hooks.beforeCommit = func() error { return errors.New("db busy") } }, false, wantOld},
		{"重命名后崩溃", func(m *Manager) { m.hooks.afterRename = crash }, true, wantOld},
		{"提交前崩溃", func(m *Manager) { m.hooks.beforeCommit = crash }, true, wantOld},
		{"提交后崩溃（旧目录未删）", func(m *Manager) { m.hooks.afterCommit = func() { panic("simulated crash") } }, true, wantNew},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m, s := openMgr(t, dir, okSpace{})
			mustCommit(t, m, oldC)
			oldFields, oldSums := m.Snapshot().Fields, map[Role]string{}
			for _, r := range Roles {
				oldSums[r] = assetSum(t, m, r)
			}
			tc.hook(m)
			func() {
				defer func() {
					r := recover()
					if tc.panic != (r != nil) {
						t.Fatalf("panic=%v, recovered=%v", tc.panic, r)
					}
				}()
				_, err := m.Commit(newC)
				if !tc.panic && err == nil {
					t.Fatal("expected the apply to fail")
				}
			}()
			check := func(label string, m *Manager) {
				t.Helper()
				snap := m.Snapshot()
				if tc.want == wantOld {
					if snap.Fields != oldFields {
						t.Fatalf("%s: fields = %+v, want old", label, snap.Fields)
					}
					for _, r := range Roles {
						if assetSum(t, m, r) != oldSums[r] {
							t.Fatalf("%s: asset %s is not the old one", label, r)
						}
					}
				} else {
					if snap.Fields != newC.Fields {
						t.Fatalf("%s: fields = %+v, want new", label, snap.Fields)
					}
					for _, r := range Roles {
						sum := sha256.Sum256(newC.Blobs[r].Data)
						if assetSum(t, m, r) != fmt.Sprintf("%x", sum[:6]) {
							t.Fatalf("%s: asset %s is not the new one (mixed references)", label, r)
						}
					}
				}
			}
			if !tc.panic { // 失败路径：当前进程内状态也必须完整，且无残留
				check("live", m)
				if names := subdirs(t, filepath.Join(dir, "profile", "sets")); len(names) != 1 {
					t.Fatalf("leftover asset sets after failure: %v", names)
				}
				if names := subdirs(t, filepath.Join(dir, "profile", "staging")); len(names) != 0 {
					t.Fatalf("leftover staging after failure: %v", names)
				}
			}
			restarted := reopen(t, dir, s)
			check("restart", restarted)
			if names := subdirs(t, filepath.Join(dir, "profile", "sets")); len(names) != 1 {
				t.Fatalf("restart left %d asset sets: %v", len(names), names)
			}
			if names := subdirs(t, filepath.Join(dir, "profile", "staging")); len(names) != 0 {
				t.Fatalf("restart left staging: %v", names)
			}
		})
	}
}

func TestLowSpaceRejectsImportWithoutTouchingCurrentAssets(t *testing.T) {
	dir := t.TempDir()
	m, _ := openMgr(t, dir, okSpace{})
	mustCommit(t, m, sampleCandidate("旧站", 1))
	oldSum, oldView := assetSum(t, m, Logo), m.View()
	before := tree(t, dir)

	m.space = noSpace{}
	cand, err := ParsePackage(zipOf(goodEnts()...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stage(cand); !errors.Is(err, ErrSpace) {
		t.Fatalf("import with no space: %v", err)
	}
	if _, err := m.Commit(sampleCandidate("新站", 2)); !errors.Is(err, ErrSpace) {
		t.Fatalf("form save with no space: %v", err)
	}
	if assetSum(t, m, Logo) != oldSum || !reflect.DeepEqual(m.View(), oldView) {
		t.Fatal("current profile changed under low space")
	}
	if after := tree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("low space left files behind:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestStagedImportExpiresAndTamperingIsDetected(t *testing.T) {
	dir := t.TempDir()
	m, _ := openMgr(t, dir, okSpace{})
	mustCommit(t, m, sampleCandidate("旧站", 1))
	oldSum := assetSum(t, m, Logo)
	cand, _ := ParsePackage(zipOf(goodEnts()...))

	token, err := m.Stage(cand)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now.Add(stagedTTL + time.Minute) }
	if _, err := m.Apply(token); !errors.Is(err, ErrStagedGone) {
		t.Fatalf("expired import applied: %v", err)
	}
	m.now = time.Now

	token, err = m.Stage(cand)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile", "staging", token, "logo.png"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(token); err == nil {
		t.Fatal("tampered staged asset was applied")
	}
	if _, err := m.Apply("../../../etc/passwd"); !errors.Is(err, ErrStagedGone) {
		t.Fatal("malformed token must be rejected")
	}
	if assetSum(t, m, Logo) != oldSum || m.Snapshot().Fields.SiteName != "旧站" {
		t.Fatal("current profile changed")
	}
}

func TestStaleSetsAreRemovedOnlyAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	m, _ := openMgr(t, dir, okSpace{})
	for i := 0; i < 3; i++ {
		mustCommit(t, m, sampleCandidate(fmt.Sprintf("站%d", i), uint8(i)))
		if names := subdirs(t, filepath.Join(dir, "profile", "sets")); len(names) != 1 {
			t.Fatalf("expected exactly the active set, got %v", names)
		}
	}
	if m.Snapshot().Revision != 3 {
		t.Fatalf("revision = %d", m.Snapshot().Revision)
	}
}

// ---- 4. 文字与图片校验 ----

func TestFieldsNormalizeAndValidate(t *testing.T) {
	ok, err := Fields{SiteName: "  Foo\nBar\x00 ", Accent: "#4263EB", Intro: "a\u202eb"}.Normalize()
	if err != nil || ok.SiteName != "Foo Bar" || ok.Accent != "#4263eb" || ok.Intro != "ab" {
		t.Fatalf("normalize: %+v %v", ok, err)
	}
	for name, f := range map[string]Fields{
		"accent 三位": {Accent: "#fff"}, "accent 名称": {Accent: "red"}, "accent 注入": {Accent: "#000000;}body{display:none"},
		"accent 过浅": {Accent: "#ffff00"}, "站点名超长": {SiteName: strings.Repeat("x", 41)}, "简介超长": {Intro: strings.Repeat("x", 201)},
		"页脚超长": {Footer: strings.Repeat("x", 121)}, "按钮超长": {ButtonText: strings.Repeat("x", 21)}, "非法 UTF-8": {SiteName: "\xff\xfe"},
	} {
		if _, err := f.Normalize(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestValidateImageByContentOnly(t *testing.T) {
	for name, data := range map[string][]byte{"png": goodLogo, "jpeg": jpegOf(8, 8, color.RGBA{1, 2, 3, 255}), "webp": webpOf} {
		if _, err := ValidateImage(Banner, data); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	for name, data := range map[string][]byte{"svg": []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), "gif": gifOf(), "html": []byte("<html><script>1</script>"), "empty": nil} {
		if _, err := ValidateImage(Banner, data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ValidateImage(Logo, pngOf(700, 700, color.RGBA{9, 9, 9, 255})); err != nil {
		t.Logf("large logo: %v", err) // 纯色 PNG 压缩后很小，只做记录
	}
}

func TestThemeCSSContainsOnlyValidatedValues(t *testing.T) {
	v := NewView(Fields{Accent: "#1d4ed8"}, map[Role]string{Logo: "/site/logo?v=2", Banner: `/x");}body{display:none;/*`, Background: "data:image/png;base64,AAAA"})
	if strings.Contains(v.CSS, "display:none") {
		t.Fatal("unsafe URL reached the stylesheet")
	}
	if !strings.Contains(v.CSS, "--blue:#1d4ed8") || !strings.Contains(v.CSS, "prefers-color-scheme:dark") || !strings.Contains(v.CSS, "data:image/png;base64,AAAA") {
		t.Fatalf("theme css incomplete: %s", v.CSS)
	}
	if v.LogoURL != "/site/logo?v=2" || v.Mark != "F" {
		t.Fatalf("view = %+v", v)
	}
	// 管理端取色器的初始值就是官方默认蓝，用户勾选“自定义”后不改动也必须被接受。
	if _, err := (Fields{Accent: "#4263eb"}).Normalize(); err != nil {
		t.Fatalf("the built-in default accent must satisfy its own contrast rule: %v", err)
	}
}

// 遍历颜色网格：凡是被接受的强调色，亮/暗两种模式下的文字、按钮对比度都必须达到 AA。
func TestAcceptedAccentsMeetContrastInBothModes(t *testing.T) {
	accepted := 0
	for r := 0; r <= 255; r += 17 {
		for g := 0; g <= 255; g += 17 {
			for b := 0; b <= 255; b += 17 {
				hex := rgb{float64(r), float64(g), float64(b)}.hex()
				if _, err := (Fields{Accent: hex}).Normalize(); err != nil {
					continue
				}
				accepted++
				c, _ := parseHex(hex)
				deep := mix(c, black, 0.2)
				txt := forDark(c, darkSoft(c))
				checks := map[string]float64{
					"按钮白字/强调色": contrast(white, c), "按钮白字/深色端": contrast(white, deep),
					"亮色 文字/卡片": contrast(c, white), "亮色 文字/着色底": contrast(c, lightSoft(c)),
					"暗色 文字/卡片": contrast(txt, darkCard), "暗色 文字/着色底": contrast(txt, darkSoft(c)),
				}
				for name, ratio := range checks {
					if ratio < minAccentContrast {
						t.Fatalf("%s: %s = %.2f < %.1f", hex, name, ratio, minAccentContrast)
					}
				}
			}
		}
	}
	if accepted < 500 {
		t.Fatalf("only %d colours accepted; the contrast rule is unreasonably strict", accepted)
	}
}

func TestOnlyWhitelistedURLsBecomeTrustedLogoURLs(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:text/html;base64,PHNjcmlwdD4=", "data:image/svg+xml;base64,PHN2Zz4=", "https://evil.example/x.png", "//evil.example/x.png", `/site/logo"onerror="x`, ""} {
		if got := NewView(Fields{}, map[Role]string{Logo: bad}).LogoURL; got != "" {
			t.Errorf("%q became a trusted logo URL: %q", bad, got)
		}
	}
	for _, good := range []string{"/site/logo?v=3", "data:image/png;base64,iVBORw0KGgo=", "data:image/webp;base64,UklGRg=="} {
		if got := NewView(Fields{}, map[Role]string{Logo: good}).LogoURL; got != good {
			t.Errorf("%q rejected", good)
		}
	}
}
