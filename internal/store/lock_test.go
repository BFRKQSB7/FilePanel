package store

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDataDirLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	lock, err := LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	runChild := func() ([]byte, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDataDirLockHelper$")
		cmd.Env = append(os.Environ(), "FILEPANEL_TEST_LOCK_DIR="+dir)
		return cmd.CombinedOutput()
	}
	if out, err := runChild(); err == nil || !strings.Contains(string(out), "独占锁") {
		t.Fatalf("child should be denied the live lock: %v %s", err, out)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := runChild(); err != nil {
		t.Fatalf("child cannot acquire released lock (file remains): %v %s", err, out)
	}
}

func TestDataDirLockHelper(t *testing.T) {
	dir := os.Getenv("FILEPANEL_TEST_LOCK_DIR")
	if dir == "" {
		return
	}
	lock, err := LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
