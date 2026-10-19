package store

import (
	"testing"
)

func TestNewerSchemaRefusesStartupAndReleasedMigrationsAreNotReplayed(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec("INSERT INTO schema_migrations(version) VALUES(999)"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if newer, e := Open(dir); e == nil {
		newer.Close()
		t.Fatal("older binary opened newer schema")
	}
}
