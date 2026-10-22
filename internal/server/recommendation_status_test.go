package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRecommendationReadinessUsesCatalogNotLocalLibrary(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	_ = a.put("works", Object{"id": "code:LOCAL-1", "code": "LOCAL-1", "files": []Object{{"missing": false}}})
	w := req(t, a, "GET", "/api/v1/recommendation-status", token, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var status Object
	json.Unmarshal(w.Body.Bytes(), &status)
	if num(status["catalog_count"]) != 0 {
		t.Fatal("local library cannot substitute for external catalog")
	}
	_ = a.put("catalog", Object{"id": "javdb:NEW-1", "code": "NEW-1"})
	w = req(t, a, "GET", "/api/v1/recommendation-status", token, nil)
	json.Unmarshal(w.Body.Bytes(), &status)
	if num(status["catalog_count"]) != 1 {
		t.Fatal(status)
	}
	if req(t, a, "POST", "/api/v1/recommendation-status", token, nil).Code != http.StatusMethodNotAllowed {
		t.Fatal("status must be read-only")
	}
}
