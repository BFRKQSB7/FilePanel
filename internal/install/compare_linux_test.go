//go:build linux

package install

import (
	"path/filepath"
	"testing"

	"filepanel/internal/store"
)

func TestCompareDBAllowsAddedColumnButProtectsExistingValues(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	x, err := store.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	y, err := store.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer y.Close()
	if _, err = y.DB.Exec("ALTER TABLE shares ADD COLUMN future_template TEXT NOT NULL DEFAULT 'standard'"); err != nil {
		t.Fatal(err)
	}
	if err = compareDB(filepath.Join(a, "filepanel.db"), filepath.Join(b, "filepanel.db")); err != nil {
		t.Fatal(err)
	}
	if err = y.SetSetting("sentinel", "changed"); err != nil {
		t.Fatal(err)
	}
	if err = compareDB(filepath.Join(a, "filepanel.db"), filepath.Join(b, "filepanel.db")); err == nil {
		t.Fatal("existing metadata change was accepted")
	}
}
