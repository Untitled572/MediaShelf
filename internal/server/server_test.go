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

var _ = http.MethodGet
