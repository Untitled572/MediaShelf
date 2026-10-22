package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSameLibraryRootNormalizesWindowsSeparatorsAndCase(t *testing.T) {
	if !sameLibraryRoot(`F:\Media\2D\`, "f:/media/2d") {
		t.Fatal("equivalent Windows roots should compare equal on every host OS")
	}
	if sameLibraryRoot("/media/2D", "/media/2d") {
		t.Fatal("Unix roots should remain case-sensitive")
	}
}

func TestLibraryRootChangeScopesOutOldFilesAndPreservesWorkMetadata(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	postScan := func(works []Object) {
		t.Helper()
		lib, _ := a.get("libraries", "lib1")
		w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "root": lib["path"], "complete": true, "works": works})
		if w.Code != http.StatusOK {
			t.Fatalf("scan failed: %d %s", w.Code, w.Body.String())
		}
	}
	postScan([]Object{
		{"code": "OLD-ONLY", "title": "Original", "files": []Object{{"relative_path": "Anime/OLD-ONLY.mkv"}}},
	})
	token := login(t, a)
	if w := req(t, a, http.MethodPatch, "/api/v1/works/code:OLD-ONLY", token, Object{"rating": 4.5, "title": "Manual title"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}

	if err := a.put("libraries", Object{"id": "lib2", "device_id": "dev1", "path": "D:/other"}); err != nil {
		t.Fatal(err)
	}
	if err := a.put("works", Object{"id": "code:STILL-MISSING", "code": "STILL-MISSING", "title": "Missing", "files": []Object{{"id": "missing-file", "device_id": "dev1", "library_id": "lib2", "relative_path": "missing.mkv", "missing": true}}}); err != nil {
		t.Fatal(err)
	}
	if w := req(t, a, http.MethodPatch, "/api/v1/libraries/lib1", token, Object{"device_id": "dev1", "name": "Media", "path": "D:/media/2D"}); w.Code != http.StatusOK {
		t.Fatalf("library path update failed: %s", w.Body.String())
	}
	if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "root": "D:/media", "complete": true, "works": []Object{}}); w.Code != http.StatusConflict {
		t.Fatalf("mismatched scan root should be rejected, got %d: %s", w.Code, w.Body.String())
	}
	postScan([]Object{{"code": "IN-SCOPE", "files": []Object{{"relative_path": "IN-SCOPE.mkv"}}}})

	old, _ := a.get("works", "code:OLD-ONLY")
	if num(old["rating"]) != 4.5 || str(old["title"]) != "Manual title" {
		t.Fatalf("manual metadata was lost: %#v", old)
	}
	oldFiles := asObjects(old["files"])
	if len(oldFiles) != 1 || !flag(oldFiles[0]["scoped_out"]) || flag(oldFiles[0]["missing"]) {
		t.Fatalf("old-root file state = %#v", oldFiles)
	}
	missing, _ := a.get("works", "code:STILL-MISSING")
	if flag(asObjects(missing["files"])[0]["scoped_out"]) || !flag(asObjects(missing["files"])[0]["missing"]) {
		t.Fatalf("ordinary missing work should remain a missing record: %#v", missing)
	}
	if !exclusivelyScopedOut(old) || exclusivelyScopedOut(missing) {
		t.Fatal("scope filtering confused scoped-out files with ordinary missing files")
	}
	list := req(t, a, http.MethodGet, "/api/v1/works", token, nil)
	var listed []Object
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, x := range listed {
		if str(x["id"]) == "code:OLD-ONLY" {
			t.Fatal("work exclusively outside the current library scope remained listed")
		}
		if str(x["id"]) == "code:STILL-MISSING" {
			return
		}
	}
	t.Fatal("ordinary missing work was hidden from the library list")
}

func TestLibraryRootChangeReactivatesFileWhenItReturnsToScope(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	scan := func(root string, works []Object) {
		t.Helper()
		lib, _ := a.get("libraries", "lib1")
		lib["path"] = root
		if err := a.put("libraries", lib); err != nil {
			t.Fatal(err)
		}
		w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "root": root, "complete": true, "works": works})
		if w.Code != http.StatusOK {
			t.Fatalf("scan failed: %d %s", w.Code, w.Body.String())
		}
	}
	scan("D:/media", []Object{{"code": "BACK", "files": []Object{{"relative_path": "BACK.mkv"}}}})
	scan("D:/media/Anime", []Object{})
	x, _ := a.get("works", "code:BACK")
	if !flag(asObjects(x["files"])[0]["scoped_out"]) {
		t.Fatal("changed root did not scope out previous file")
	}
	scan("D:/media", []Object{{"code": "BACK", "files": []Object{{"relative_path": "BACK.mkv"}}}})
	x, _ = a.get("works", "code:BACK")
	active := 0
	for _, f := range asObjects(x["files"]) {
		if !flag(f["scoped_out"]) {
			active++
			if flag(f["missing"]) {
				t.Fatalf("returned file was still missing: %#v", f)
			}
		}
	}
	if active != 1 {
		t.Fatalf("expected one current-root file, got %#v", x["files"])
	}
}

func TestRootChangeDoesNotReuseProgressForSameRelativePath(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	scan := func(root string) {
		t.Helper()
		lib, _ := a.get("libraries", "lib1")
		lib["path"] = root
		if err := a.put("libraries", lib); err != nil {
			t.Fatal(err)
		}
		w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "root": root, "complete": true, "works": []Object{{"code": "SAME-PATH", "files": []Object{{"relative_path": "same.mkv"}}}}})
		if w.Code != http.StatusOK {
			t.Fatalf("scan failed: %d %s", w.Code, w.Body.String())
		}
	}
	scan("D:/media")
	work, _ := a.get("works", "code:SAME-PATH")
	files := asObjects(work["files"])
	files[0]["progress"] = Object{"compression": "done", "cutting": "done", "translation": "done", "verified": true}
	work["files"] = files
	if err := a.put("works", work); err != nil {
		t.Fatal(err)
	}
	scan("D:/different-root")
	work, _ = a.get("works", "code:SAME-PATH")
	active := []Object{}
	archived := []Object{}
	for _, f := range asObjects(work["files"]) {
		if flag(f["scoped_out"]) {
			archived = append(archived, f)
		} else {
			active = append(active, f)
		}
	}
	if len(archived) != 1 || len(active) != 1 {
		t.Fatalf("same relative path should produce an archived and current file record: %#v", work["files"])
	}
	if !flag(toObject(archived[0]["progress"])["verified"]) {
		t.Fatal("archiving removed prior file progress")
	}
	if flag(toObject(active[0]["progress"])["verified"]) {
		t.Fatal("new root file inherited old file verification")
	}
	scan("D:/different-root")
	work, _ = a.get("works", "code:SAME-PATH")
	if len(asObjects(work["files"])) != 2 {
		t.Fatalf("repeated scan created another file record: %#v", work["files"])
	}
}
