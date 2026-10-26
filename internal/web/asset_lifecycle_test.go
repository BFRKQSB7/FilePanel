package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"filepanel/internal/share"
)

func TestShareImagePersistsBeforeSaveAndAfterCancellation(t *testing.T) {
	e := newEnv(t)
	item := e.upload("image-lifecycle.txt", []byte("image lifecycle"))
	upload := func() share.Asset {
		t.Helper()
		rec := e.postAsset(item.ID, "image.png", tinyPNG)
		if rec.Code != http.StatusCreated {
			t.Fatalf("image upload: %d %s", rec.Code, rec.Body.String())
		}
		var asset share.Asset
		if err := json.Unmarshal(rec.Body.Bytes(), &asset); err != nil {
			t.Fatal(err)
		}
		return asset
	}
	remove := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
		req.Header.Set("X-CSRF-Token", e.csrf)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delete %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	asset := upload()
	imagePath := filepath.Join(e.files.DataDir(), "assets", asset.ID)
	assertStored := func() {
		t.Helper()
		data, err := os.ReadFile(imagePath)
		if err != nil || !bytes.Equal(data, tinyPNG) {
			t.Fatalf("uploaded image not retained on disk: %v", err)
		}
		if rec := e.get("/api/assets/"+asset.ID, nil, true); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), tinyPNG) {
			t.Fatal("administrator cannot retrieve retained image")
		}
	}
	// Upload itself commits the asset, even without saving share settings.
	assertStored()
	if rec := e.get("/a/"+asset.ID, nil, false); rec.Code != 404 {
		t.Fatal("unshared image accessible publicly")
	}
	e.publish(item.ID, "image lifecycle", true)
	if rec := e.get("/a/"+asset.ID, nil, false); rec.Code != 200 {
		t.Fatal("enabled share image inaccessible")
	}
	remove("/api/files/" + item.ID + "/share")
	assertStored()
	if rec := e.get("/a/"+asset.ID, nil, false); rec.Code != 404 {
		t.Fatal("cancelled share image still accessible publicly")
	}
	e.publish(item.ID, "image lifecycle", true)
	if rec := e.get("/a/"+asset.ID, nil, false); rec.Code != 200 {
		t.Fatal("reopening share lost its retained image")
	}
	remove("/api/files/" + item.ID + "/assets/" + asset.ID)
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		t.Fatalf("explicit image deletion retained its disk object: %v", err)
	}
	if rec := e.get("/api/assets/"+asset.ID, nil, true); rec.Code != 404 {
		t.Fatal("deleted image remains available to administrator")
	}
	// Deleting the owning file also removes remaining uploaded images.
	other := upload()
	remove("/api/files/" + item.ID)
	if _, err := os.Stat(filepath.Join(e.files.DataDir(), "assets", other.ID)); !os.IsNotExist(err) {
		t.Fatalf("file deletion retained its image: %v", err)
	}
}
