package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseTrustBoundary(t *testing.T) {
	dir := t.TempDir()
	data := []byte("trusted artifact")
	src := filepath.Join(dir, "filepanel")
	if e := os.WriteFile(src, data, 0600); e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(data)
	a := Artifact{File: "filepanel", SHA256: hex.EncodeToString(h[:]), Size: int64(len(data))}
	path := filepath.Join(dir, "release.json")
	write := func(a Artifact) string {
		b, _ := json.Marshal(Release{Version: "test", Artifacts: map[string]Artifact{"linux-" + runtime.GOARCH: a}})
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	pin := write(a)
	if p, _, e := VerifyRelease(path, pin); e != nil || p != src {
		t.Fatalf("valid artifact: %s %v", p, e)
	}
	if _, _, e := VerifyRelease(path, strings.Repeat("0", 64)); e == nil {
		t.Fatal("untrusted manifest accepted")
	}
	for _, bad := range []Artifact{{File: "../filepanel", SHA256: a.SHA256, Size: a.Size}, {File: a.File, SHA256: a.SHA256, Size: a.Size + 1}, {File: a.File, SHA256: strings.Repeat("0", 64), Size: a.Size}} {
		if _, _, e := VerifyRelease(path, write(bad)); e == nil {
			t.Fatalf("bad artifact accepted: %+v", bad)
		}
	}
}

func TestConfigBoundaries(t *testing.T) {
	c := Config{Instance: "test", Role: "panel", Listen: "127.0.0.1:8080", Reserve: 16 << 20}
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	for _, edit := range []func(*Config){func(c *Config) { c.Instance = "../other" }, func(c *Config) { c.Listen = "127.0.0.1:80" }, func(c *Config) { c.Reserve = 1 }, func(c *Config) { c.Role = "node"; c.Listen = "0.0.0.0:8081" }, func(c *Config) { c.Listen = "127.0.0.1:99999" }} {
		bad := c
		edit(&bad)
		if Validate(bad) == nil {
			t.Fatalf("invalid config accepted: %+v", bad)
		}
	}
}

func TestBoundedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	l := &Log{Path: path}
	b := []byte(strings.Repeat("x", 700000))
	for i := 0; i < 5; i++ {
		if _, e := l.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []string{path, path + ".1"} {
		s, e := os.Stat(p)
		if e != nil || s.Size() > 1<<20 {
			t.Fatalf("unbounded log: %v %v", s, e)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatalf("log retained %d files", len(entries))
	}
}
