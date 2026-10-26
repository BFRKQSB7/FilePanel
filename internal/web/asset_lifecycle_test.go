package web

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if rec := e.get("/a/"+asset.ID, nil, false); rec.Code != 404 {
		t.Fatal("unused image exposed by sharing its file")
	}
	if _, _, err := e.deps.Shares.Update(item.ID, share.Input{Title: "image lifecycle", Enabled: true, GalleryAssets: []string{asset.ID}}); err != nil {
		t.Fatal(err)
	}
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
	if err := e.deps.Shares.RemoveAsset(item.ID, asset.ID); !errors.Is(err, share.ErrAssetInUse) {
		t.Fatalf("referenced image deletion: %v", err)
	}
	if _, _, err := e.deps.Shares.Update(item.ID, share.Input{Title: "image lifecycle", Enabled: true, GalleryAssets: []string{}}); err != nil {
		t.Fatal(err)
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

func TestAssetLibraryClassifiesReferencesAndProtectsDeletion(t *testing.T) {
	e := newEnv(t)
	f := e.upload("library.txt", []byte("library"))
	var a share.Asset
	rec := e.postAsset(f.ID, "cover.png", tinyPNG)
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatal("upload failed")
	}
	get := func(status string) share.LibraryPage {
		t.Helper()
		rec := e.get("/api/assets?status="+status, nil, true)
		var page share.LibraryPage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
		}
		return page
	}
	if e.get("/api/assets", nil, false).Code != 401 {
		t.Fatal("library allows anonymous access")
	}
	if p := get("unused"); p.Total != 1 || p.Used != 0 || len(p.Items) != 1 || p.Items[0].Name != "cover.png" || p.Bytes != int64(len(tinyPNG)) {
		t.Fatalf("wrong unused state: %+v", p)
	}
	md := "![图片](asset:" + a.ID + ")"
	input := share.Input{CoverAsset: a.ID, Markdown: md, GalleryAssets: []string{a.ID}, Enabled: true}
	if _, _, err := e.deps.Shares.Update(f.ID, input); err != nil {
		t.Fatal(err)
	}
	if p := get("used"); p.Used != 1 || len(p.Items) != 1 || len(p.Items[0].Uses) != 3 {
		t.Fatalf("missing usage: %+v", p)
	}
	if p := get("unused"); len(p.Items) != 0 {
		t.Fatal("used image in unused filter")
	}
	if err := e.deps.Shares.Revoke(f.ID); err != nil {
		t.Fatal(err)
	}
	if p := get("used"); p.Used != 1 || p.Items[0].Enabled {
		t.Fatal("cancellation erased usage")
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/files/"+f.ID+"/assets/"+a.ID, nil)
	req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
	req.Header.Set("X-CSRF-Token", e.csrf)
	res := httptest.NewRecorder()
	e.handler.ServeHTTP(res, req)
	if res.Code != 409 {
		t.Fatalf("deletion must be blocked: %d", res.Code)
	}
	// References inside code blocks / inline code are examples, not rendered images.
	input = share.Input{Markdown: "`" + md + "`\n\n```\n" + md + "\n```", GalleryAssets: []string{}}
	if _, _, err := e.deps.Shares.Update(f.ID, input); err != nil {
		t.Fatal(err)
	}
	if p := get("unused"); p.Used != 0 || len(p.Items) != 1 {
		t.Fatal("code example counted as usage")
	}
	if err := e.deps.Shares.RemoveAsset(f.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.deps.Shares.Update(f.ID, share.Input{Markdown: md}); err == nil {
		t.Fatal("stale editor resurrected deleted reference")
	}
	if p := get("all"); p.Total != 0 || p.Bytes != 0 {
		t.Fatal("deleted image still counted")
	}
}
