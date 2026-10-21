package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"filepanel/internal/files"
	"filepanel/internal/profile"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalReplicaIncludedAndVerifiedInBackup(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	db, e := store.Open(data)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	f, e := files.New(db, data, 1<<20, 1)
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.Upload(context.Background(), "original.txt", 7, strings.NewReader("payload"))
	if e != nil {
		t.Fatal(e)
	}
	copy, e := f.UploadStaged(context.Background(), strings.Repeat("r", 32), "copy.txt", 7, strings.NewReader("payload"), nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec("INSERT INTO file_replicas(file_id,node_id,object_id,sha256,size) VALUES(?,'',?,?,?)", v.ID, copy.ID, copy.SHA256, copy.Size); e != nil {
		t.Fatal(e)
	}
	snapshot := filepath.Join(root, "snapshot")
	if _, e = Create(data, snapshot, 1); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(root, "restored")
	if _, e = RestoreNew(snapshot, restored, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(restored, "objects/data", copy.ID)); e != nil {
		t.Fatal("local replica missing from restored snapshot", e)
	}
	if e = os.Remove(filepath.Join(snapshot, "objects/data", copy.ID)); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(snapshot); e == nil {
		t.Fatal("backup accepted missing local replica")
	}
}

func TestWALSnapshotRestoresBytesIdentityAndPrivateState(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	db, e := store.Open(data)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.DB.Exec("PRAGMA wal_autocheckpoint=0"); e != nil {
		t.Fatal(e)
	}
	f, e := files.New(db, data, 1<<20, 0)
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.Upload(context.Background(), "notes.txt", 7, strings.NewReader("payload"))
	if e != nil {
		t.Fatal(e)
	}
	sh, e := share.New(db, f)
	if e != nil {
		t.Fatal(e)
	}
	original, e := sh.Ensure(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	var pngData bytes.Buffer
	if e = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 4, 4))); e != nil {
		t.Fatal(e)
	}
	asset, e := sh.AddAsset(v.ID, bytes.NewReader(pngData.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	prof, e := profile.New(db, data, f)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := prof.Compose(profile.Edit{Fields: profile.Fields{SiteName: "备份测试"}, Changes: map[profile.Role]profile.Change{profile.Logo: {Data: pngData.Bytes()}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prof.Commit(candidate); e != nil {
		t.Fatal(e)
	}
	if e = db.SetSetting("node_panel_key", "fixed-secret-identity"); e != nil {
		t.Fatal(e)
	}
	wal, e := os.Stat(filepath.Join(data, "filepanel.db-wal"))
	if e != nil || wal.Size() == 0 {
		t.Fatal("test did not create live WAL")
	}
	path := filepath.Join(dir, "snapshot")
	m, e := Create(data, path, 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	if m.RemoteFiles != 0 || m.Role != "panel" {
		t.Fatal(m)
	}
	if e = db.SetSetting("node_panel_key", "changed-after-snapshot"); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(dir, "restored")
	if _, e = RestoreNew(path, target, 16<<20); e != nil {
		t.Fatal(e)
	}
	r, e := store.Open(target)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if s, ok, e := r.Setting("node_panel_key"); e != nil || !ok || s != "fixed-secret-identity" {
		t.Fatalf("identity: %q %v", s, e)
	}
	rf, e := files.New(r, target, 1<<20, 0)
	if e != nil {
		t.Fatal(e)
	}
	_, h, e := rf.Open(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(h)
	h.Close()
	if e != nil || string(b) != "payload" {
		t.Fatal("download bytes", e)
	}
	rs, e := share.New(r, rf)
	if e != nil {
		t.Fatal(e)
	}
	got, e := rs.Ensure(v.ID)
	if e != nil || got.Slug != original.Slug || got.Enabled {
		t.Fatalf("private/share state: %+v %v", got, e)
	}
	_, imageBytes, e := rs.OpenAsset(asset.ID, false)
	if e != nil || !bytes.Equal(imageBytes, pngData.Bytes()) {
		t.Fatal("asset restore", e)
	}
	rp, e := profile.New(r, target, rf)
	if e != nil {
		t.Fatal(e)
	}
	if rp.View().SiteName != "备份测试" {
		t.Fatal("profile fields not restored")
	}
	_, imageBytes, e = rp.Asset(profile.Logo)
	if e != nil || !bytes.Equal(imageBytes, pngData.Bytes()) {
		t.Fatal("profile material restore", e)
	}
	if _, e = RestoreNew(path, target, 16<<20); e == nil {
		t.Fatal("overwrote existing data")
	}
	meta := filepath.Join(path, "snapshot.json")
	m.Entries = append(m.Entries, Entry{Path: "../escape", Size: 1, SHA256: strings.Repeat("a", 64)})
	b, _ = json.Marshal(m)
	if e = os.WriteFile(meta, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(path); e == nil {
		t.Fatal("accepted traversal")
	}
}
func TestBackupRejectsCorruptionAndLinks(t *testing.T) {
	for _, bad := range []string{"hash", "extra", "link", "size", "duplicate", "schema"} {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			db, e := store.Open(data)
			if e != nil {
				t.Fatal(e)
			}
			db.Close()
			path := filepath.Join(dir, "snapshot")
			m, e := Create(data, path, 16<<20)
			if e != nil {
				t.Fatal(e)
			}
			switch bad {
			case "hash":
				os.WriteFile(filepath.Join(path, "filepanel.db"), []byte("corrupt"), 0600)
			case "extra":
				os.WriteFile(filepath.Join(path, "extra"), []byte("unlisted"), 0600)
			case "link":
				if e = os.Symlink(filepath.Join(data, "filepanel.db"), filepath.Join(path, "escape")); e != nil {
					t.Skip(e)
				}
			case "size":
				m.Entries[0].Size = MaxBytes + 1
			case "duplicate":
				m.Entries = append(m.Entries, m.Entries[0])
			case "schema":
				m.Schema++
			}
			b, _ := json.Marshal(m)
			os.WriteFile(filepath.Join(path, "snapshot.json"), b, 0600)
			if _, e = Verify(path); e == nil {
				t.Fatal("accepted invalid snapshot")
			}
			if _, e = os.Stat(filepath.Join(data, "filepanel.db")); e != nil {
				t.Fatal("original damaged", e)
			}
		})
	}
}
