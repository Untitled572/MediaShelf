package server

import (
	"net/http"
	"testing"
)

func TestActorFavoritePreservesIndependentCollectionPolicy(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	_ = a.put("actor_policies", Object{"id": "Example", "name": "Example", "policy": "complete", "target": 9, "aliases": []string{"Alias"}})
	w := req(t, a, http.MethodPatch, "/api/v1/actors/Example", token, Object{"favorite": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, _ := a.get("actor_policies", "Example")
	if !flag(saved["favorite"]) || str(saved["policy"]) != "complete" || num(saved["target"]) != 9 || !contains(saved["aliases"], "Alias") {
		t.Fatalf("favorite patch damaged independent fields: %#v", saved)
	}
	w = req(t, a, http.MethodPatch, "/api/v1/actors/Example", token, Object{"favorite": "yes"})
	if w.Code != 400 {
		t.Fatalf("nonboolean favorite accepted: %d", w.Code)
	}
}
