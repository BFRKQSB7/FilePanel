//go:build linux

package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPrivateUmaskKeepsServiceReadable(t *testing.T) {
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	config := filepath.Join(dir, "config")
	if e := os.WriteFile(src, []byte("binary"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := copyExclusive(src, dst, 0755); e != nil {
		t.Fatal(e)
	}
	if e := put(config, []byte("config"), 0640); e != nil {
		t.Fatal(e)
	}
	for path, mode := range map[string]os.FileMode{dst: 0755, config: 0640} {
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm() != mode {
			t.Fatalf("umask lost service permissions: %s %v %v", path, i, e)
		}
	}
}

func TestPlatformRequiresRealInit(t *testing.T) {
	for _, tc := range []struct {
		body       string
		sd, rc, ok bool
	}{{"ID=alpine", false, true, true}, {"ID=debian", true, false, true}, {"ID=ubuntu\nVERSION_ID=\"24.04\"", true, false, true}, {"ID=ubuntu\nVERSION_ID=22.04", true, false, false}, {"ID=debian", false, false, false}, {"ID=fedora", true, false, false}, {"ID=alpine", true, false, false}} {
		_, e := platform(tc.body, tc.sd, tc.rc)
		if (e == nil) != tc.ok {
			t.Fatalf("%+v: %v", tc, e)
		}
	}
}

func TestDestructivePathBoundaries(t *testing.T) {
	for _, p := range []string{"/", "/var/lib", "/mnt/disk", "/var/lib/filepanel-other", "/opt/filepanel-test/data", "/var/lib/../filepanel-test"} {
		if validData(p, "test") == nil {
			t.Fatalf("accepted shared path %s", p)
		}
	}
	if e := validData("/mnt/data/filepanel-test", "test"); e != nil {
		t.Fatal(e)
	}
	if os.Geteuid() != 0 {
		t.Skip("ownership checks need root in isolated Linux")
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	// /tmp is deliberately writable: no privileged target may descend through it.
	if trusted(dir, 0) == nil {
		t.Fatal("accepted mutable ancestor")
	}
	if e := os.Symlink("/etc", filepath.Join(dir, "escape")); e != nil {
		t.Fatal(e)
	}
	if trusted(filepath.Join(dir, "escape"), 0) == nil {
		t.Fatal("accepted symlink ancestor")
	}
	m := &manifest{Version: 1, Config: Config{Instance: "test"}, User: "fp-test", UID: 123, GID: 123}
	if purgeCheck(m) == nil {
		t.Fatal("accepted missing ownership manifest")
	}
}

func TestCancelAndUnknownRemoteSummary(t *testing.T) {
	m := &manifest{Config: Config{Instance: "test", Role: "panel", Data: t.TempDir()}}
	sentinel := filepath.Join(m.Config.Data, "keep")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	var out bytes.Buffer
	if e := uninstall(m, true, strings.NewReader("no\n"), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "未知") || !strings.Contains(out.String(), "已取消") {
		t.Fatal(out.String())
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "keep" {
		t.Fatal("cancel modified data")
	}
	if _, e := os.Stat(filepath.Join(m.Config.Data, "filepanel.db")); !os.IsNotExist(e) {
		t.Fatal("read-only report created DB")
	}
}
