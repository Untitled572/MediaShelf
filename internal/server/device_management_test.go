package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceEditPreservesIdentityAndAgentToken(t *testing.T) {
	a := testApp(t)
	did, dt := device(t, a)
	token := login(t, a)
	w := req(t, a, http.MethodPatch, "/api/v1/devices/"+did, token, Object{"name": "本机", "host": "UNTD572.EU.ORG"})
	if w.Code != 200 || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatalf("edit failed or leaked token: %d %s", w.Code, w.Body.String())
	}
	stored, _ := a.get("devices", did)
	if str(stored["host"]) != "untd572.eu.org" || str(stored["token_hash"]) != digest(dt) {
		t.Fatal(stored)
	}
	if r := req(t, a, http.MethodPost, "/api/v1/agent/heartbeat", dt, Object{}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	stored, _ = a.get("devices", did)
	if str(stored["host"]) != "untd572.eu.org" || str(stored["name"]) != "本机" {
		t.Fatal(stored)
	}
	for _, host := range []string{"https://example.com", "host:123", "user@host", "../host", "host/path"} {
		if r := req(t, a, http.MethodPatch, "/api/v1/devices/"+did, token, Object{"host": host}); r.Code != 400 {
			t.Fatalf("accepted %q", host)
		}
	}
	if r := req(t, a, http.MethodPatch, "/api/v1/devices/"+did, token, Object{"token_hash": "changed"}); r.Code != 400 {
		t.Fatal("allowed token overwrite")
	}
}
func TestDeviceDeletionCascadesOnlyItsRecordsAndKeepsPhysicalFiles(t *testing.T) {
	a := testApp(t)
	did, dt := device(t, a)
	token := login(t, a)
	physical := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(physical, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = a.put("devices", Object{"id": "other", "name": "Other", "token_hash": digest("other-token")})
	for _, kind := range []string{"tools", "downloaders", "jobs"} {
		_ = a.put(kind, Object{"id": "mine", "device_id": did})
		_ = a.put(kind, Object{"id": "other", "device_id": "other"})
	}
	_ = a.put("candidates", Object{"id": "submission", "downloader_id": "mine"})
	_ = a.put("works", Object{"id": "mine", "files": []Object{{"device_id": did, "library_id": "lib1", "relative_path": physical, "scoped_out": true}}})
	_ = a.put("works", Object{"id": "legacy", "device_id": did})
	_ = a.put("works", Object{"id": "shared", "rating": 4.5, "files": []Object{{"device_id": did, "library_id": "lib1"}, {"device_id": "other", "progress": Object{"compression": "done", "cutting": "done", "translation": "done", "verified": true}}}})
	_ = a.put("catalog", Object{"id": "public", "code": "A-1"})
	preview := req(t, a, http.MethodGet, "/api/v1/devices/"+did+"/deletion-preview", token, nil)
	var p Object
	_ = json.Unmarshal(preview.Body.Bytes(), &p)
	counts := toObject(p["counts"])
	if preview.Code != 200 || num(counts["works_deleted"]) != 2 || num(counts["works_updated"]) != 1 || num(counts["files"]) != 2 || num(counts["libraries"]) != 1 {
		t.Fatal(preview.Body.String())
	}
	if _, err := a.get("devices", did); err != nil {
		t.Fatal("preview mutated records")
	}
	removed := req(t, a, http.MethodDelete, "/api/v1/devices/"+did, token, nil)
	if removed.Code != 200 {
		t.Fatal(removed.Body.String())
	}
	for _, entry := range [][2]string{{"devices", did}, {"works", "mine"}, {"works", "legacy"}, {"libraries", "lib1"}, {"tools", "mine"}, {"downloaders", "mine"}, {"jobs", "mine"}, {"candidates", "submission"}} {
		if _, err := a.get(entry[0], entry[1]); err == nil {
			t.Fatalf("left record %v", entry)
		}
	}
	shared, err := a.get("works", "shared")
	if err != nil || len(asObjects(shared["files"])) != 1 || num(shared["rating"]) != 4.5 || !flag(toObject(shared["progress"])["verified"]) {
		t.Fatal(shared, err)
	}
	for _, kind := range []string{"devices", "tools", "downloaders", "jobs"} {
		if _, err := a.get(kind, "other"); err != nil {
			t.Fatalf("deleted other device %s", kind)
		}
	}
	if _, err := os.Stat(physical); err != nil {
		t.Fatal("deleted physical media")
	}
	if w := req(t, a, http.MethodPost, "/api/v1/agent/heartbeat", dt, Object{}); w.Code != 401 {
		t.Fatal("old device token still accepted")
	}
	if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{"library_id": "lib1"}); w.Code != 401 {
		t.Fatal("deleted device could recreate scans")
	}
	if _, err := a.get("catalog", "public"); err != nil {
		t.Fatal("removed global source data")
	}
}
func TestDeviceDeletionFailureRollsBackAllRecords(t *testing.T) {
	a := testApp(t)
	did, _ := device(t, a)
	token := login(t, a)
	_ = a.put("jobs", Object{"id": "bad-job", "device_id": did})
	if _, err := a.db.Exec(`CREATE TRIGGER fail_device_job_delete BEFORE DELETE ON documents WHEN old.kind='jobs' BEGIN SELECT RAISE(ABORT,'injected failure');END`); err != nil {
		t.Fatal(err)
	}
	w := req(t, a, http.MethodDelete, "/api/v1/devices/"+did, token, nil)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, entry := range [][2]string{{"devices", did}, {"libraries", "lib1"}, {"jobs", "bad-job"}} {
		if _, err := a.get(entry[0], entry[1]); err != nil {
			t.Fatalf("non atomic delete %v", entry)
		}
	}
}
