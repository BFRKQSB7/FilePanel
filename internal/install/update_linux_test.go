//go:build linux

package install

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"filepanel/internal/release"
	"filepanel/internal/store"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateDurableFailureAndCrashRecovery(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated root Linux fixture required")
	}
	oldService, oldReady, oldVersion := maintenanceService, maintenanceReady, maintenanceReadyVersion
	defer func() {
		maintenanceService = oldService
		maintenanceReady = oldReady
		maintenanceReadyVersion = oldVersion
		updateFault = nil
	}()
	maintenanceService = func(*manifest, string) error { return nil }
	maintenanceReady = func(Config) error { return nil }
	maintenanceReadyVersion = func(*manifest, string) error { return nil }
	for index, stage := range []string{"replace", "migrate", "health", "crash"} {
		t.Run(stage, func(t *testing.T) {
			id := fmt.Sprintf("t08ut%d-%d", os.Getpid(), index)
			root := base(id)
			parent := filepath.Join("/root", id)
			data := filepath.Join(parent, "filepanel-"+id)
			for _, p := range []string{root, parent} {
				if _, e := os.Lstat(p); !os.IsNotExist(e) {
					t.Fatal("fixture already exists", p)
				}
			}
			for _, p := range []string{root + "/config/staged", data, registry} {
				if e := os.MkdirAll(p, 0700); e != nil {
					t.Fatal(e)
				}
			}
			defer os.RemoveAll(root)
			defer os.RemoveAll(parent)
			defer os.Remove(manifestPath(id))
			defer os.Remove(journalPath(id))
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			m := &manifest{Version: 1, Config: Config{Instance: id, Role: "panel", Data: data, Listen: "127.0.0.1:19998", Reserve: 16 << 20, UpdateKey: base64.StdEncoding.EncodeToString(pub)}, State: "active", Sequence: 1, Files: map[string]string{}, Anchors: make([]anchor, 6), UID: 0, GID: 0}
			if e := os.WriteFile(root+"/filepanel", []byte("original-program"), 0755); e != nil {
				t.Fatal(e)
			}
			m.Hash, _, _ = digestFile(root + "/filepanel")
			m.Files[root+"/filepanel"] = m.Hash
			db, e := store.Open(data)
			if e != nil {
				t.Fatal(e)
			}
			if e = db.SetSetting("node_panel_key", "identity-before-update"); e != nil {
				t.Fatal(e)
			}
			db.Close()
			candidate := configPath(m, "staged/candidate")
			if e = os.WriteFile(candidate, []byte("new-program"), 0755); e != nil {
				t.Fatal(e)
			}
			h, n, _ := digestFile(candidate)
			r := release.Manifest{Product: "FilePanel", Version: "0.1.1", Sequence: 2, Source: strings.Repeat("a", 64), SchemaMin: release.MinSchema, SchemaMax: release.Schema, ProtocolMin: 1, ProtocolMax: 1, Artifacts: map[string]release.Artifact{"linux-" + runtime.GOARCH: {File: "candidate", SHA256: h, Size: n}}}
			b, _ := json.Marshal(r)
			env := release.Envelope{Manifest: b, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))}
			b, _ = json.Marshal(env)
			if e = os.WriteFile(configPath(m, "staged/release.json"), b, 0600); e != nil {
				t.Fatal(e)
			}
			fired := false
			updateFault = func(at string) error {
				if at == stage || stage == "crash" && at == "migrate" {
					fired = true
					db, e := store.Open(data)
					if e != nil {
						return e
					}
					e = db.SetSetting("node_panel_key", "partially-migrated")
					db.Close()
					if e != nil {
						return e
					}
					if stage == "crash" {
						if e = os.WriteFile(root+"/filepanel.next", []byte("interrupted copy"), 0600); e != nil {
							t.Fatal(e)
						}
						if e = os.WriteFile(manifestPath(id)+".tmp", []byte("old interrupted manifest"), 0600); e != nil {
							t.Fatal(e)
						}
						panic("simulated process crash")
					}
					return errors.New("injected " + stage)
				}
				return nil
			}
			if stage == "crash" {
				func() {
					defer func() {
						if recover() == nil {
							t.Fatal("crash did not fire")
						}
					}()
					_ = applyUpdate(m, io.Discard)
				}()
				if e = recoverUpdate(id, io.Discard); e != nil {
					t.Fatal(e)
				}
			} else if e = applyUpdate(m, io.Discard); e == nil {
				t.Fatal("failure accepted")
			}
			if !fired {
				t.Fatal("injection not reached")
			}
			if e = recoverUpdate(id, io.Discard); e != nil {
				t.Fatal("repeat recovery", e)
			}
			got, _, e := digestFile(root + "/filepanel")
			if e != nil || got != m.Hash {
				t.Fatal("program was not restored", e)
			}
			db, e = store.Open(data)
			if e != nil {
				t.Fatal(e)
			}
			s, ok, e := db.Setting("node_panel_key")
			db.Close()
			if e != nil || !ok || s != "identity-before-update" {
				t.Fatal("snapshot identity not restored", s, e)
			}
			if _, e = os.Lstat(configPath(m, "maintenance")); !os.IsNotExist(e) {
				t.Fatal("maintenance gate retained", e)
			}
		})
	}
}
