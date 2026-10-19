package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filepanel/internal/store"
)

func testManager(t *testing.T) (*Manager, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(s, dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return m, s, dir
}

func TestUploadDownloadRenameAndDelete(t *testing.T) {
	m, _, _ := testManager(t)
	content := []byte("complete file contents")
	item, err := m.Upload(context.Background(), "../notes.txt", int64(len(content)), strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	if item.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("hash mismatch: %s", item.SHA256)
	}
	if item.Name != "notes.txt" {
		t.Fatalf("path component not normalized: %q", item.Name)
	}
	gotItem, handle, err := m.Open(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	_ = handle.Close()
	if string(got) != string(content) || gotItem.Size != int64(len(content)) {
		t.Fatal("downloaded content mismatch")
	}
	if err := m.Rename(item.ID, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil || len(list) != 1 || list[0].Name != "renamed.txt" {
		t.Fatalf("unexpected listing: %#v %v", list, err)
	}
	if err := m.Remove(item.ID); err != nil {
		t.Fatal(err)
	}
	if list, err = m.List(); err != nil || len(list) != 0 {
		t.Fatalf("deleted item still listed: %#v %v", list, err)
	}
}

func TestUploadRejectsSizeMismatchAndOversizedUnknownLength(t *testing.T) {
	m, s, dir := testManager(t)
	if _, err := m.Upload(context.Background(), "bad.bin", 10, strings.NewReader("short")); err == nil {
		t.Fatal("size mismatch accepted")
	}
	m.maxUpload = 4
	if _, err := m.Upload(context.Background(), "large.bin", -1, strings.NewReader("12345")); err == nil {
		t.Fatal("unknown-length oversized file accepted")
	}
	list, err := m.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("failed upload became visible: %#v %v", list, err)
	}
	var partial int
	if err := s.DB.QueryRow("SELECT count(*) FROM uploads WHERE state='receiving'").Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("unfinished upload not marked failed: %d %v", partial, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "objects", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary objects remain: %v %v", entries, err)
	}
}

func TestRecoverInterruptedUploadAndMissingAvailableObject(t *testing.T) {
	m, s, dir := testManager(t)
	id, tempKey, objectKey := "crash-id", "crash-id.part", "crash-id"
	if err := os.WriteFile(filepath.Join(dir, "objects", "tmp", tempKey), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec(`INSERT INTO uploads(id,object_key,temp_key,display_name,expected_size,received_size,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'receiving',?,?)`, id, objectKey, tempKey, "partial.bin", 20, 7, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("partial upload exposed after recovery: %#v %v", list, err)
	}
	var state string
	if err := s.DB.QueryRow("SELECT state FROM uploads WHERE id=?", id).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("upload state=%q err=%v", state, err)
	}

	content := "recover me"
	item, err := m.Upload(context.Background(), "lost.bin", int64(len(content)), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := s.DB.QueryRow("SELECT object_key FROM files WHERE id=?", item.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "objects", "data", key)); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(item.ID); err == nil {
		t.Fatal("missing object still downloadable")
	}
}

func TestRecoverVerifiedUploadAfterObjectCommit(t *testing.T) {
	m, s, dir := testManager(t)
	content := []byte("verified before restart")
	id, key, tempKey := "verified-id", "verified-id", "verified-id.part"
	sha := sha256.Sum256(content)
	if err := os.WriteFile(filepath.Join(dir, "objects", "data", key), content, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec(`INSERT INTO uploads(id,object_key,temp_key,display_name,expected_size,received_size,sha256,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'verified',?,?)`, id, key, tempKey, "recovered.txt", len(content), len(content), hex.EncodeToString(sha[:]), time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	item, handle, err := m.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(handle)
	_ = handle.Close()
	if err != nil || string(got) != string(content) || item.SHA256 != hex.EncodeToString(sha[:]) {
		t.Fatalf("recovered object mismatch: %q %v", got, err)
	}
}

func TestOpenRejectsSymlinkObject(t *testing.T) {
	m, s, dir := testManager(t)
	target := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	id, key := "link-id", "link-id"
	if err := os.Symlink(target, filepath.Join(dir, "objects", "data", key)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := s.DB.Exec(`INSERT INTO uploads(id,object_key,temp_key,display_name,received_size,sha256,state,created_at,updated_at) VALUES(?,?,?, ?,0,?,'available',?,?)`, id, key, id+".part", "link.txt", strings.Repeat("0", 64), time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) VALUES(?,?,?,0,?,'available',?,?)`, id, key, "link.txt", strings.Repeat("0", 64), time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(id); err == nil {
		t.Fatal("symlink object was opened")
	}
}

func TestRecoverInterruptedDelete(t *testing.T) {
	m, s, dir := testManager(t)
	item, err := m.Upload(context.Background(), "delete.txt", 4, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := s.DB.QueryRow("SELECT object_key FROM files WHERE id=?", item.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE files SET state='deleting' WHERE id=?", item.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "objects", "data", key)); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(item.ID); err == nil {
		t.Fatal("deleted object still available after recovery")
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM files WHERE id=?", item.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleting record not recovered: count=%d err=%v", count, err)
	}
}

func TestSpaceReservationSharesConcurrentBudgetAndRejectsLowSpace(t *testing.T) {
	m, _, dir := testManager(t)
	// Building other packages may change real free bytes between checks.
	// This test controls capacity, not the development machine's disk activity.
	free := uint64(64 << 20)
	m.diskInfo = func(string) (uint64, uint64, uint64, error) { return 1 << 30, free, (1 << 30) - free, nil }
	m.reserve = free - 32<<10
	if err := m.reserveChunk(32 << 10); err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if err := m.reserveChunk(1); err == nil {
		t.Fatal("concurrent reservation exceeded shared free-space budget")
	}
	m.releaseChunk(32 << 10)
	m.reserve = free
	if _, err := m.Upload(context.Background(), "low-space.bin", 4, strings.NewReader("data")); err == nil {
		t.Fatal("upload accepted without space beyond reserved floor")
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "objects", "tmp")); err != nil || len(entries) != 0 {
		t.Fatalf("low-space failure left temporary objects: %v %v", entries, err)
	}
}

func TestDeleteWaitsForOpenReadLease(t *testing.T) {
	m, _, _ := testManager(t)
	item, err := m.Upload(context.Background(), "lease.txt", 4, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	_, handle, err := m.Open(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Remove(item.ID) }()
	select {
	case err := <-done:
		t.Fatalf("delete completed before read lease closed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_ = handle.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete remained blocked after lease close")
	}
}

func TestUploadValidationFailureCannotBeRecoveredAsFile(t *testing.T) {
	m, s, dir := testManager(t)
	rejected := errors.New("enclosing request rejected")
	_, err := m.UploadChecked(context.Background(), "rejected.txt", 4, strings.NewReader("data"), func() error {
		var state string
		if err := s.DB.QueryRow("SELECT state FROM uploads LIMIT 1").Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "receiving" {
			t.Fatalf("upload became recoverable before validation: %s", state)
		}
		return rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("validation error lost: %v", err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if items, err := m.List(); err != nil || len(items) != 0 {
		t.Fatalf("rejected request became available after recovery: %v %v", items, err)
	}
	for _, folder := range []string{"tmp", "data"} {
		if entries, err := os.ReadDir(filepath.Join(dir, "objects", folder)); err != nil || len(entries) != 0 {
			t.Fatalf("rejected request left object bytes in %s: %v %v", folder, entries, err)
		}
	}
}
