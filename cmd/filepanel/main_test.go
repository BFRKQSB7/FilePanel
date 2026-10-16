package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filepanel/internal/files"
	"filepanel/internal/store"
)

func TestServeRejectsLockedDirectoryBeforeRecovery(t *testing.T) {
	dir := t.TempDir()
	lock, err := store.LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := files.New(s, dir, 1<<20, 0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "objects", "tmp", "active.part")
	if err := os.WriteFile(path, []byte("live upload"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO uploads(id,object_key,temp_key,display_name,state,created_at,updated_at) VALUES('active','active','active.part','active.txt','receiving',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid listen address also guarantees this test terminates if the guard
	// regresses; recovery must not run even when the second bind would fail.
	err = serve([]string{"--data-dir", dir, "--listen", "invalid-listen-address"})
	if err == nil || !strings.Contains(err.Error(), "独占锁") {
		t.Errorf("second serve did not fail at the directory lock: %v", err)
	}
	var state string
	if err := s.DB.QueryRow("SELECT state FROM uploads WHERE id='active'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "receiving" {
		t.Errorf("second startup modified live upload state: %s", state)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "live upload" {
		t.Errorf("second startup touched the live temporary object: %q %v", data, err)
	}
}

func TestServeReleasesDirectoryLockOnStartupFailure(t *testing.T) {
	dir := t.TempDir()
	if err := serve([]string{"--data-dir", dir, "--listen", "invalid-listen-address"}); err == nil {
		t.Fatal("invalid listener unexpectedly started")
	}
	lock, err := store.LockDataDir(dir)
	if err != nil {
		t.Fatal("failed startup leaked the directory lock:", err)
	}
	_ = lock.Close()
}
