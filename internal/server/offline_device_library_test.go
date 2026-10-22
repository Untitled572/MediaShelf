package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestOfflineDeviceLibraryRemainsBrowsable(t *testing.T) {
	a := testApp(t)
	did, dt := device(t, a)
	if w := req(t, a, http.MethodPost, "/api/v1/agent/scan", dt, Object{
		"library_id": "lib1",
		"complete":  true,
		"works": []Object{{
			"code":   "OFFLINE-001",
			"title":  "Saved work",
			"actors": []string{"Saved Actor"},
			"files":  []Object{{"relative_path": "OFFLINE-001/movie.mkv", "size": 1234}},
		}},
	}); w.Code != http.StatusOK {
		t.Fatalf("initial scan failed: %d %s", w.Code, w.Body.String())
	}
	userToken := login(t, a)
	progress := Object{"compression": "done", "cutting": "not_needed", "translation": "done", "verified": true}
	if w := req(t, a, http.MethodPatch, "/api/v1/works/code:OFFLINE-001", userToken, Object{"rating": 4.5, "progress": progress}); w.Code != http.StatusOK {
		t.Fatalf("metadata update failed: %d %s", w.Code, w.Body.String())
	}
	deviceRecord, err := a.get("devices", did)
	if err != nil {
		t.Fatal(err)
	}
	deviceRecord["last_seen"] = time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	if err := a.put("devices", deviceRecord); err != nil {
		t.Fatal(err)
	}

	devices := req(t, a, http.MethodGet, "/api/v1/devices", userToken, nil)
	var listedDevices []Object
	if err := json.Unmarshal(devices.Body.Bytes(), &listedDevices); err != nil || devices.Code != http.StatusOK || len(listedDevices) != 1 || flag(listedDevices[0]["online"]) {
		t.Fatalf("device should be offline: status=%d body=%s err=%v", devices.Code, devices.Body.String(), err)
	}

	works := req(t, a, http.MethodGet, "/api/v1/works", userToken, nil)
	var listedWorks []Object
	if err := json.Unmarshal(works.Body.Bytes(), &listedWorks); err != nil || works.Code != http.StatusOK || len(listedWorks) != 1 {
		t.Fatalf("offline device work disappeared from list: status=%d body=%s err=%v", works.Code, works.Body.String(), err)
	}
	detail := req(t, a, http.MethodGet, "/api/v1/works/code:OFFLINE-001", userToken, nil)
	var work Object
	if err := json.Unmarshal(detail.Body.Bytes(), &work); err != nil || detail.Code != http.StatusOK {
		t.Fatalf("offline work metadata GET failed: status=%d body=%s err=%v", detail.Code, detail.Body.String(), err)
	}
	files := asObjects(work["files"])
	if str(work["title"]) != "Saved work" || num(work["rating"]) != 4.5 || !flag(toObject(work["progress"])["verified"]) || len(files) != 1 || flag(files[0]["missing"]) {
		t.Fatalf("offline metadata or file state was lost: %#v", work)
	}

	actors := req(t, a, http.MethodGet, "/api/v1/actors", userToken, nil)
	var listedActors []Object
	if err := json.Unmarshal(actors.Body.Bytes(), &listedActors); err != nil || actors.Code != http.StatusOK || len(listedActors) != 1 || str(listedActors[0]["name"]) != "Saved Actor" || int(num(listedActors[0]["count"])) != 1 {
		t.Fatalf("offline actor disappeared from list: status=%d body=%s err=%v", actors.Code, actors.Body.String(), err)
	}

	selection := callDiscovery(t, a, http.MethodGet, []string{"actors", "Saved Actor", "selection"}, nil)
	var selected Object
	if err := json.Unmarshal(selection.Body.Bytes(), &selected); err != nil || selection.Code != http.StatusOK || len(asObjects(selected["local"])) != 1 {
		t.Fatalf("offline local work disappeared from actor selection: status=%d body=%s err=%v", selection.Code, selection.Body.String(), err)
	}

	// Browsing is still available offline, while requesting a device action is not.
	if w := req(t, a, http.MethodPost, "/api/v1/libraries/lib1/scan", userToken, nil); w.Code != http.StatusConflict {
		t.Fatalf("offline scan enqueue should be rejected, got %d: %s", w.Code, w.Body.String())
	}
}
