package store

import (
	"encoding/json"
	"testing"
)

func TestAssetLibraryMigrationPreservesExistingGallery(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`ALTER TABLE shares DROP COLUMN gallery_assets;
ALTER TABLE assets DROP COLUMN name;
DELETE FROM schema_migrations WHERE version=7;
INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) VALUES('f','f','old',1,'sha','available',1,1);
INSERT INTO shares(file_id,slug,cover_asset_id,created_at,updated_at) VALUES('f','old','cover',1,1);
INSERT INTO assets(id,file_id,object_key,mime,size,created_at) VALUES('cover','f','cover','image/png',1,1),('shot','f','shot','image/png',1,2);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw, cover string
	if err = db.DB.QueryRow("SELECT gallery_assets,cover_asset_id FROM shares WHERE file_id='f'").Scan(&raw, &cover); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil || len(ids) != 1 || ids[0] != "shot" || cover != "cover" {
		t.Fatalf("old gallery lost: %q %s", raw, cover)
	}
	if _, err = db.DB.Exec("UPDATE shares SET gallery_assets='[]' WHERE file_id='f'"); err != nil {
		t.Fatal(err)
	}
	if err = applyMigrations(db.DB); err != nil {
		t.Fatal(err)
	}
	if err = db.DB.QueryRow("SELECT gallery_assets FROM shares WHERE file_id='f'").Scan(&raw); err != nil || raw != "[]" {
		t.Fatal("migration replayed and restored removed references")
	}
}

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
