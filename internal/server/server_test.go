package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	a, e := New(t.TempDir(), "test-password-123", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}
func req(t *testing.T, a *App, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func login(t *testing.T, a *App) string {
	w := req(t, a, "POST", "/api/v1/login", "", Object{"password": "test-password-123"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var o Object
	json.Unmarshal(w.Body.Bytes(), &o)
	return str(o["token"])
}
func device(t *testing.T, a *App) (string, string) {
	t.Helper()
	token := "device-secret"
	did := "dev1"
	if e := a.put("devices", Object{"id": did, "token_hash": digest(token), "last_seen": now()}); e != nil {
		t.Fatal(e)
	}
	a.put("libraries", Object{"id": "lib1", "device_id": did, "path": "D:/media"})
	return did, token
}
func TestAuthAndRating(t *testing.T) {
	a := testApp(t)
	if w := req(t, a, "GET", "/api/v1/works", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	token := login(t, a)
	a.put("works", Object{"id": "x", "rating": nil})
	for _, n := range []any{-1, 5.5, 1.2, "5"} {
		w := req(t, a, "PATCH", "/api/v1/works/x", token, Object{"rating": n})
		if w.Code != 400 {
			t.Fatalf("accepted invalid rating %v: %d", n, w.Code)
		}
	}
	for _, n := range []any{0, 4.5, nil} {
		if w := req(t, a, "PATCH", "/api/v1/works/x", token, Object{"rating": n}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
}
func TestScanPreservesManualAndOffline(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	scan := Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": "TEST-001", "title": "Original", "actors": []string{"Actor"}, "files": []Object{{"relative_path": "TEST-001/part1.mp4", "size": 10}}}}}
	if w := req(t, a, "POST", "/api/v1/agent/scan", dt, scan); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	token := login(t, a)
	p := Object{"rating": 4.5, "title": "Manual", "progress": Object{"compression": "done", "cutting": "not_needed", "translation": "done", "verified": true}}
	if w := req(t, a, "PATCH", "/api/v1/works/code:TEST-001", token, p); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	req(t, a, "POST", "/api/v1/agent/scan", dt, scan)
	x, _ := a.get("works", "code:TEST-001")
	if num(x["rating"]) != 4.5 || str(x["title"]) != "Manual" || !flag(toObject(x["progress"])["verified"]) {
		t.Fatal(x)
	}
	req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "complete": false, "works": []Object{}})
	x, _ = a.get("works", "code:TEST-001")
	if flag(asObjects(x["files"])[0]["missing"]) {
		t.Fatal("partial scan marked missing")
	}
	req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "complete": true, "works": []Object{}})
	x, _ = a.get("works", "code:TEST-001")
	if !flag(asObjects(x["files"])[0]["missing"]) {
		t.Fatal("full scan did not mark missing")
	}
}
func TestScanRejectsTraversalAtomically(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	w := req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": "GOOD", "files": []Object{{"relative_path": "good.mp4"}}}, {"code": "BAD", "files": []Object{{"relative_path": "../bad.mp4"}}}}})
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	xs, _ := a.list("works")
	if len(xs) != 0 {
		t.Fatal("partial writes on invalid scan")
	}
}
func TestDeviceIsolationAndJobRedaction(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	a.put("jobs", Object{"id": "secret-job", "type": "qbit", "device_id": "dev1", "status": "pending", "created_at": now(), "payload": Object{"config": Object{"password": "qbit-secret"}}})
	w := req(t, a, "GET", "/api/v1/jobs", login(t, a), nil)
	if strings.Contains(w.Body.String(), "qbit-secret") {
		t.Fatal("password leaked")
	}
	a.put("libraries", Object{"id": "other", "device_id": "someone-else"})
	w = req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "other", "works": []Object{}})
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = req(t, a, "POST", "/api/v1/agent/poll", dt, Object{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "secret-job") {
		t.Fatal(w.Body.String())
	}
	w = req(t, a, "POST", "/api/v1/agent/poll", dt, Object{})
	if strings.Contains(w.Body.String(), "secret-job") {
		t.Fatal("job dispatched twice")
	}
}
func TestFileProgressCannotCompleteOtherSegments(t *testing.T) {
	a := testApp(t)
	a.put("works", Object{"id": "w", "files": []Object{{"id": "a", "progress": defaultProgress()}, {"id": "b", "progress": defaultProgress()}}})
	w := req(t, a, "PATCH", "/api/v1/works/w/files/a", login(t, a), Object{"progress": Object{"compression": "done", "cutting": "done", "translation": "done", "verified": true}})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	x, _ := a.get("works", "w")
	if flag(toObject(x["progress"])["verified"]) {
		t.Fatal("one segment completed whole work")
	}
}
func TestBackupRestoreAtomic(t *testing.T) {
	a := testApp(t)
	a.put("works", Object{"id": "one", "title": "Keep"})
	token := login(t, a)
	w := req(t, a, "GET", "/api/v1/backup", token, nil)
	var backup Object
	json.Unmarshal(w.Body.Bytes(), &backup)
	a2 := testApp(t)
	w = req(t, a2, "POST", "/api/v1/restore", login(t, a2), backup)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	x, e := a2.get("works", "one")
	if e != nil || str(x["title"]) != "Keep" {
		t.Fatal(x, e)
	}
	backup["tools"] = []any{Object{"invalid": true}}
	w = req(t, a2, "POST", "/api/v1/restore", login(t, a2), backup)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestNonImageCoverRejected(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	w := req(t, a, "POST", "/api/v1/agent/covers", dt, Object{"html": "x"})
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestScanMergesRichNFOWithBareFilesInEitherOrder(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	_ = a.put("libraries", Object{"id": "lib2", "device_id": "dev1", "path": "D:/staging"})
	rich := func(code, title, rel string) Object {
		return Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": code, "title": title, "year": 2024, "actors": []string{"NFO Actor"}, "tags": []string{"Drama"}, "source_rating": 8.7, "source_url": "https://catalog.example/item", "cover_url": "/covers/rich.jpg", "kind": "2D", "has_nfo": true, "files": []Object{{"relative_path": rel, "size": 10}}}}}
	}
	bare := func(code, title, rel string, includeMarker bool) Object {
		work := Object{"code": code, "title": title, "actors": []string{}, "tags": []string{}, "source_rating": nil, "source_url": "", "kind": "VR", "files": []Object{{"relative_path": rel, "size": 11}}}
		if includeMarker {
			work["has_nfo"] = false
		}
		return Object{"library_id": "lib2", "complete": true, "works": []Object{work}}
	}
	scan := func(payload Object) {
		t.Helper()
		if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, payload); w.Code != http.StatusOK {
			t.Fatalf("scan failed: %d %s", w.Code, w.Body.String())
		}
	}

	// Rich NFO first, then a later staging-library filename-only scan.
	scan(rich("MIX-001", "NFO Original Title", "2D/MIX-001/MIX-001.mkv"))
	scan(bare("MIX-001", "MIX-001 VR Disk", "VR/MIX-001/MIX-001.mkv", false)) // pre-marker agent payload
	merged, err := a.get("works", "code:MIX-001")
	if err != nil {
		t.Fatal(err)
	}
	assertMergedScan(t, merged, "NFO Original Title", 2)
	if str(merged["kind"]) != "VR" {
		t.Fatalf("scan kind should continue to reflect the latest library classification: %q", merged["kind"])
	}

	// Reverse order: NFO metadata should enrich the prior filename fallback.
	scan(bare("MIX-002", "MIX-002 disk", "VR/MIX-002/MIX-002.mkv", true))
	scan(rich("MIX-002", "NFO Enriched Title", "2D/MIX-002/MIX-002.mkv"))
	merged, err = a.get("works", "code:MIX-002")
	if err != nil {
		t.Fatal(err)
	}
	assertMergedScan(t, merged, "NFO Enriched Title", 2)
}

func assertMergedScan(t *testing.T, work Object, title string, fileCount int) {
	t.Helper()
	if str(work["title"]) != title || num(work["year"]) != 2024 || num(work["source_rating"]) != 8.7 || str(work["source_url"]) != "https://catalog.example/item" || str(work["cover_url"]) != "/covers/rich.jpg" {
		t.Fatalf("rich NFO metadata was lost: %#v", work)
	}
	if !contains(work["actors"], "NFO Actor") || !contains(work["tags"], "Drama") || len(asObjects(work["files"])) != fileCount {
		t.Fatalf("actors/tags/files were not preserved and merged: %#v", work)
	}
}

func TestScanNFOMergeRespectsManualFields(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	_ = a.put("libraries", Object{"id": "lib2", "device_id": "dev1", "path": "D:/staging"})
	token := login(t, a)
	rich := Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": "MAN-001", "title": "NFO Title", "year": 2022, "actors": []string{"NFO Actor"}, "tags": []string{"Drama"}, "source_rating": 9.0, "kind": "2D", "has_nfo": true, "files": []Object{{"relative_path": "2D/MAN-001.mkv"}}}}}
	if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, rich); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	patch := Object{"title": "My Title", "actors": []string{"My Actor"}, "tags": []string{"My Tag"}, "rating": 4.5}
	if w := req(t, a, http.MethodPatch, "/api/v1/works/code:MAN-001", token, patch); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	bare := Object{"library_id": "lib2", "complete": true, "works": []Object{{"code": "MAN-001", "title": "MAN-001 disk", "year": nil, "actors": []string{}, "tags": []string{}, "source_rating": nil, "source_url": "", "kind": "VR", "has_nfo": false, "files": []Object{{"relative_path": "VR/MAN-001.mkv"}}}}}
	if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, bare); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	work, _ := a.get("works", "code:MAN-001")
	if str(work["title"]) != "My Title" || !contains(work["actors"], "My Actor") || contains(work["actors"], "NFO Actor") || !contains(work["tags"], "My Tag") || num(work["rating"]) != 4.5 || num(work["source_rating"]) != 9.0 || len(asObjects(work["files"])) != 2 {
		t.Fatalf("manual fields or files were not preserved: %#v", work)
	}
}

var _ = http.MethodGet

func TestWorkCodeNormalizationForOwnedAndQueueDuplicates(t *testing.T) {
	a := testApp(t)
	if err := a.put("works", Object{
		"id": "code:IPX-177", "code": "IPX-177",
		"files": []Object{{"relative_path": "IPX-177.mkv", "missing": false}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"IPX177", "IPX-177", "IPX_177"} {
		if !a.owned(code) {
			t.Errorf("owned(%q) did not match existing IPX-177", code)
		}
	}
	if err := a.put("works", Object{
		"id": "legacy-custom-id", "code": "FC2 PPV 12345678",
		"files": []Object{{"relative_path": "fc2.mkv", "missing": false}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"FC2PPV12345678", "FC2-PPV-12345678", "FC2 PPV 12345678"} {
		if !a.owned(code) {
			t.Errorf("owned(%q) did not match FC2 PPV variant", code)
		}
	}
	if sameWorkCode("CUSTOM#001", "CUSTOM001") {
		t.Fatal("unknown code punctuation was discarded")
	}
	if sameWorkCode("", "") {
		t.Fatal("empty codes must not match")
	}
	queued := []Object{{"name": "IPX-177 sample", "hash": "aa"}, {"name": "FC2-PPV-12345678 sample", "hash": "bb"}}
	if !queueDuplicate(queued, "https://example.test/other.torrent", "IPX177") {
		t.Fatal("queue duplicate missed compact IPX code")
	}
	if !queueDuplicate(queued, "https://example.test/other.torrent", "FC2PPV12345678") {
		t.Fatal("queue duplicate missed compact FC2 code")
	}
	if queueDuplicate(queued, "https://example.test/other.torrent", "CUSTOM001") {
		t.Fatal("unknown code was spuriously merged")
	}
	if a.owned("CUSTOM001") {
		t.Fatal("unknown code was spuriously treated as owned")
	}
	if _, err := a.get("works", "code:IPX177"); err == nil {
		t.Fatal("normalization must not migrate or create alternate work IDs")
	}
}
