package server

import (
	"encoding/json"
	"testing"
)

func TestScanAvatarMergeAndActorPolicyUpdates(t *testing.T) {
	a := testApp(t)
	_, dt := device(t, a)
	token := login(t, a)
	scan := func(thumbs Object) {
		t.Helper()
		r := req(t, a, "POST", "/api/v1/agent/scan", dt, Object{"library_id": "lib1", "complete": true, "works": []Object{{"code": "TEST-AVATAR", "actors": []string{"Example Actor"}, "actor_thumbs": thumbs, "files": []Object{{"relative_path": "test.mp4"}}}}})
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
	scan(Object{"Example Actor": "https://images.example.test/actor.jpg", "Not In Cast": "https://images.example.test/other.jpg"})
	scan(Object{})
	w, _ := a.get("works", "code:TEST-AVATAR")
	thumbs := toObject(w["actor_thumbs"])
	if str(thumbs["Example Actor"]) == "" || thumbs["Not In Cast"] != nil {
		t.Fatal(thumbs)
	}
	r := req(t, a, "GET", "/api/v1/actors", token, nil)
	var actors []Object
	json.Unmarshal(r.Body.Bytes(), &actors)
	if len(actors) != 1 || str(actors[0]["avatar_url"]) != "https://images.example.test/actor.jpg" {
		t.Fatal(r.Body.String())
	}
	a.put("actor_policies", Object{"id": "Example Actor", "name": "Example Actor", "policy": "selective", "target": 1, "aliases": []string{"Alias"}})
	r = req(t, a, "PATCH", "/api/v1/actors/Example%20Actor", token, Object{"avatar_url": "https://images.example.test/manual.jpg"})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	p, _ := a.get("actor_policies", "Example Actor")
	if str(p["policy"]) != "selective" || len(stringSlice(p["aliases"])) != 1 {
		t.Fatal(p)
	}
	r = req(t, a, "PATCH", "/api/v1/actors/Example%20Actor", token, Object{"policy": "follow"})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	p, _ = a.get("actor_policies", "Example Actor")
	if str(p["avatar_url"]) != "https://images.example.test/manual.jpg" {
		t.Fatal(p)
	}
	for _, raw := range []string{"javascript:alert(1)", "file:///private/photo.jpg", "https://user:pass@example.test/photo.jpg", "/covers/../secret.jpg"} {
		r = req(t, a, "PATCH", "/api/v1/actors/Example%20Actor", token, Object{"avatar_url": raw})
		if r.Code != 400 {
			t.Fatalf("accepted unsafe avatar %q", raw)
		}
	}
}
