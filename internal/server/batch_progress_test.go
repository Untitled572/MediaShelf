package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBatchProgressUpdatesOnlySpecifiedFileSteps(t *testing.T) {
	a := testApp(t)
	first := Object{
		"id":                         "work-one",
		"title":                      "First",
		"collection_status":          "owned",
		"legacy_collection_complete": true,
		"files": []Object{
			{"id": "one-a", "relative_path": "a.mkv", "progress": Object{"compression": "done", "cutting": "pending", "translation": "pending", "verified": false}},
			{"id": "one-b", "relative_path": "b.mkv", "progress": Object{"compression": "in_progress", "cutting": "not_needed", "translation": "pending", "verified": false}},
		},
	}
	second := Object{
		"id":    "work-two",
		"title": "Second",
		"files": []Object{
			{"id": "two-a", "relative_path": "c.mkv", "progress": Object{"compression": "not_needed", "cutting": "done", "translation": "pending", "verified": false}},
		},
	}
	if err := a.put("works", first); err != nil {
		t.Fatal(err)
	}
	if err := a.put("works", second); err != nil {
		t.Fatal(err)
	}

	response := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", login(t, a), Object{
		"ids":      []string{"work-one", "work-two"},
		"progress": Object{"translation": "done"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("batch update failed: %d %s", response.Code, response.Body.String())
	}
	var result Object
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if int(num(result["updated_works"])) != 2 || int(num(result["updated_files"])) != 3 {
		t.Fatalf("unexpected update counts: %#v", result)
	}

	updatedFirst, _ := a.get("works", "work-one")
	if str(updatedFirst["title"]) != "First" || str(updatedFirst["collection_status"]) != "owned" || !flag(updatedFirst["legacy_collection_complete"]) {
		t.Fatalf("batch progress dropped unrelated or legacy work fields: %#v", updatedFirst)
	}
	files := asObjects(updatedFirst["files"])
	if str(toObject(files[0]["progress"])["translation"]) != "done" || str(toObject(files[0]["progress"])["compression"]) != "done" || str(toObject(files[0]["progress"])["cutting"]) != "pending" {
		t.Fatalf("first file unrelated progress was overwritten: %#v", files[0])
	}
	if str(toObject(files[1]["progress"])["translation"]) != "done" || str(toObject(files[1]["progress"])["compression"]) != "in_progress" || str(toObject(files[1]["progress"])["cutting"]) != "not_needed" {
		t.Fatalf("second file unrelated progress was overwritten: %#v", files[1])
	}
	workProgress := toObject(updatedFirst["progress"])
	if str(workProgress["compression"]) != "in_progress" || str(workProgress["cutting"]) != "pending" || str(workProgress["translation"]) != "done" {
		t.Fatalf("work summary was not aggregated from its files: %#v", workProgress)
	}
	updatedSecond, _ := a.get("works", "work-two")
	secondFile := asObjects(updatedSecond["files"])[0]
	if str(toObject(secondFile["progress"])["translation"]) != "done" || str(toObject(secondFile["progress"])["compression"]) != "not_needed" || str(toObject(secondFile["progress"])["cutting"]) != "done" {
		t.Fatalf("second work unrelated progress was overwritten: %#v", secondFile)
	}
}

func TestBatchProgressRejectsInvalidBatchWithoutPartialWrites(t *testing.T) {
	a := testApp(t)
	seed := Object{"id": "existing", "progress": Object{"compression": "pending", "cutting": "done", "translation": "pending", "verified": false}, "files": []Object{{"id": "f1", "progress": Object{"compression": "pending", "cutting": "done", "translation": "pending", "verified": false}}}}
	if err := a.put("works", seed); err != nil {
		t.Fatal(err)
	}
	token := login(t, a)

	unknown := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", token, Object{
		"ids":      []string{"existing", "missing"},
		"progress": Object{"translation": "done"},
	})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown work should reject the batch: %d %s", unknown.Code, unknown.Body.String())
	}
	assertWorkUnchanged(t, a, seed)

	invalid := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", token, Object{
		"ids":      []string{"existing"},
		"progress": Object{"translation": "finished"},
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid step should reject the batch: %d %s", invalid.Code, invalid.Body.String())
	}
	assertWorkUnchanged(t, a, seed)
}

func TestBatchProgressRejectsScopedOutWorkWithoutPartialWrites(t *testing.T) {
	a := testApp(t)
	seed := Object{"id": "in-scope", "files": []Object{{"id": "active", "progress": Object{"compression": "pending", "cutting": "pending", "translation": "pending", "verified": false}}}}
	scopedOut := Object{"id": "outside", "files": []Object{{"id": "archived", "scoped_out": true, "progress": Object{"compression": "pending", "cutting": "pending", "translation": "pending", "verified": false}}}}
	if err := a.put("works", seed); err != nil {
		t.Fatal(err)
	}
	if err := a.put("works", scopedOut); err != nil {
		t.Fatal(err)
	}
	response := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", login(t, a), Object{
		"ids":      []string{"in-scope", "outside"},
		"progress": Object{"translation": "done"},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("scoped-out work should reject the batch: %d %s", response.Code, response.Body.String())
	}
	assertWorkUnchanged(t, a, seed)
	assertWorkUnchanged(t, a, scopedOut)
}

func TestBatchProgressRequiresAuthenticationAndCapsWorkCount(t *testing.T) {
	a := testApp(t)
	if response := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", "", Object{"ids": []string{"x"}, "progress": Object{"translation": "done"}}); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated batch update status = %d", response.Code)
	}
	ids := make([]string, maxBatchProgressWorks+1)
	for i := range ids {
		ids[i] = "work"
	}
	response := req(t, a, http.MethodPost, "/api/v1/works/batch-progress", login(t, a), Object{"ids": ids, "progress": Object{"translation": "done"}})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("batch over limit should be rejected, got %d: %s", response.Code, response.Body.String())
	}
}

func assertWorkUnchanged(t *testing.T, a *App, want Object) {
	t.Helper()
	got, err := a.get("works", str(want["id"]))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("failed batch partially changed work %q: got %s want %s", str(want["id"]), gotJSON, wantJSON)
	}
}
