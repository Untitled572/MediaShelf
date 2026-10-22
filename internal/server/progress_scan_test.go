package server

import "testing"

func TestNewVersionDoesNotInheritVerification(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	scan := func(files []Object) {
		t.Helper()
		r := req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": "TEST-NEW", "files": files}}})
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
	scan([]Object{{"relative_path": "old.mp4"}})
	p := Object{"compression": "done", "cutting": "not_needed", "translation": "done", "verified": true}
	r := req(t, a, "PATCH", "/api/v1/works/code:TEST-NEW", login(t, a), Object{"progress": p})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	scan([]Object{{"relative_path": "old.mp4"}, {"relative_path": "new.mp4"}})
	w, _ := a.get("works", "code:TEST-NEW")
	if flag(toObject(w["progress"])["verified"]) {
		t.Fatal("new copy inherited verification")
	}
	fs := asObjects(w["files"])
	if !flag(toObject(fs[0]["progress"])["verified"]) || flag(toObject(fs[1]["progress"])["verified"]) {
		t.Fatal("existing/new file progress corrupted")
	}
	scan([]Object{{"relative_path": "old.mp4"}})
	w, _ = a.get("works", "code:TEST-NEW")
	if !flag(toObject(w["progress"])["verified"]) {
		t.Fatal("missing pending copy blocks available verified copy")
	}
	scan([]Object{})
	w, _ = a.get("works", "code:TEST-NEW")
	if flag(toObject(w["progress"])["verified"]) {
		t.Fatal("all missing work is complete")
	}
}
