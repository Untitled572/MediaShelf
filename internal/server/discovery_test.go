package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func callDiscovery(t *testing.T, a *App, method string, pathParts []string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	parts := make([]string, len(pathParts))
	for i, part := range pathParts {
		parts[i] = url.PathEscape(part)
	}
	r := httptest.NewRequest(method, "/api/v1/"+strings.Join(parts, "/"), bytes.NewReader(encoded))
	r.Header.Set("Authorization", "Bearer "+login(t, a))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func responseObject(t *testing.T, w *httptest.ResponseRecorder) Object {
	t.Helper()
	o := Object{}
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestActorSelectionSeparatesRegularAndCompilationAndHonorsTarget(t *testing.T) {
	a := testApp(t)
	_ = a.put("actor_policies", Object{"id": "Mika", "name": "Mika", "aliases": []string{"Mika Tan"}, "policy": "selective", "target": 1})
	_ = a.put("works", Object{"id": "code:OWN-1", "code": "OWN-1", "actors": []string{"Mika Tan"}, "files": []Object{{"missing": false}}})
	_ = a.put("catalog", Object{"id": "javdb:OWN-1", "source": "javdb", "code": "OWN-1", "title": "Owned work", "actors": []string{"Mika"}, "rating": 4.2})
	_ = a.put("catalog", Object{"id": "javdb:REG-2", "source": "javdb", "code": "REG-2", "title": "Regular work", "actors": []string{"Mika"}, "rating": 4.8})
	_ = a.put("catalog", Object{"id": "javdb:BEST-3", "source": "javdb", "code": "BEST-3", "title": "Best Of Mika Collection", "actors": []string{"Mika"}, "tags": []string{"合集"}, "rating": 5.0})
	_ = a.put("catalog_syncs", Object{"id": "javdb:Mika", "actor": "Mika", "source": "javdb", "complete": true})
	w := callDiscovery(t, a, http.MethodGet, []string{"actors", "Mika Tan", "selection"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("selection failed: %d %s", w.Code, w.Body.String())
	}
	o := responseObject(t, w)
	if str(o["policy"]) != "selective" || int(num(o["target"])) != 1 || len(asObjects(o["local"])) != 1 || len(asObjects(o["suggested"])) != 2 {
		t.Fatalf("unexpected selection payload: %#v", o)
	}
	items := asObjects(o["suggested"])
	if str(items[0]["code"]) != "REG-2" || !flag(items[0]["recommended"]) || flag(items[0]["is_compilation"]) || str(items[1]["code"]) != "BEST-3" || !flag(items[1]["is_compilation"]) || flag(items[1]["recommended"]) {
		t.Fatalf("suggestion order/flags were wrong: %#v", items)
	}
	cov := toObject(o["coverage"])
	if int(num(cov["owned"])) != 1 || int(num(cov["total"])) != 3 || !flag(cov["partial"]) {
		t.Fatalf("unexpected actor coverage: %#v", cov)
	}
	if !flag(cov["catalog_complete"]) || flag(cov["complete"]) {
		t.Fatalf("catalog sync completion must not claim local collection completion while gaps remain: %#v", cov)
	}
}

func TestAliasMergeUpdatesWorksAndCatalogAtomically(t *testing.T) {
	a := testApp(t)
	_ = a.put("works", Object{"id": "code:AAA-1", "code": "AAA-1", "actors": []string{"Old Name", "Other"}})
	_ = a.put("catalog", Object{"id": "javdb:AAA-1", "code": "AAA-1", "actors": []string{"Old Name"}, "catalog_actor": "Old Name"})
	w := callDiscovery(t, a, http.MethodPost, []string{"actors", "Canonical", "aliases"}, Object{"aliases": []string{"Old Name"}})
	if w.Code != http.StatusOK {
		t.Fatalf("alias merge failed: %d %s", w.Code, w.Body.String())
	}
	work, _ := a.get("works", "code:AAA-1")
	cat, _ := a.get("catalog", "javdb:AAA-1")
	if !contains(work["actors"], "Canonical") || contains(work["actors"], "Old Name") || !contains(cat["actors"], "Canonical") || str(cat["catalog_actor"]) != "Canonical" {
		t.Fatalf("alias references were not merged: work=%#v catalog=%#v", work, cat)
	}
	policy, _ := a.get("actor_policies", "Canonical")
	if !contains(policy["aliases"], "Old Name") {
		t.Fatalf("alias was not saved with policy: %#v", policy)
	}
}

func TestRecommendationsUseNormalizedRatingsSelectiveTargetAndCoactors(t *testing.T) {
	a := testApp(t)
	_ = a.put("actor_policies", Object{"id": "Fav", "name": "Fav", "aliases": []string{"F. A."}, "policy": "selective", "target": 1})
	_ = a.put("actor_profiles", Object{"id": "New Actor", "name": "New Actor", "gender": "female", "gender_source": "wikidata"})
	_ = a.put("works", Object{"id": "code:OWN-1", "code": "OWN-1", "actors": []string{"F. A."}, "tags": []string{"Solo"}, "rating": 5.0, "files": []Object{{"missing": false}}})
	_ = a.put("catalog", Object{"id": "javdb:ONE-1", "source": "javdb", "code": "ONE-1", "actors": []string{"Fav", "New Actor"}, "tags": []string{"Solo"}, "rating": 4.8, "source_rating": 4.8})
	_ = a.put("catalog", Object{"id": "fanza:TWO-2", "source": "fanza", "code": "TWO-2", "actors": []string{"Fav", "New Actor"}, "tags": []string{"Solo"}, "rating": 4.5, "source_rating": 45.0})
	_ = a.put("catalog", Object{"id": "javdb:SKIP-3", "source": "javdb", "code": "SKIP-3", "actors": []string{"Fav", "New Actor"}, "tags": []string{"Solo"}, "rating": 4.9})
	_ = a.put("catalog", Object{"id": "javdb:UNRELATED-4", "source": "javdb", "code": "UNRELATED-4", "actors": []string{"Unrelated Actor"}, "tags": []string{"Drama"}, "rating": 4.9})
	_ = a.put("candidates", Object{"id": "candidate", "code": "SKIP-3", "status": "ignored"})
	w := callDiscovery(t, a, http.MethodGet, []string{"recommendations"}, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var recommendations []Object
	if err := json.Unmarshal(w.Body.Bytes(), &recommendations); err != nil {
		t.Fatal(err)
	}
	var favCodes []string
	for _, recommendation := range recommendations {
		work := toObject(recommendation["work"])
		if contains(work["actors"], "Fav") {
			favCodes = append(favCodes, str(work["code"]))
		}
		if str(work["code"]) == "SKIP-3" {
			t.Fatal("ignored catalog work was recommended")
		}
	}
	if len(favCodes) != 1 || favCodes[0] != "ONE-1" {
		t.Fatalf("selective target or ignored filter failed: %#v", recommendations)
	}
	actorResponse := callDiscovery(t, a, http.MethodGet, []string{"actor-recommendations"}, nil)
	var actors []Object
	if err := json.Unmarshal(actorResponse.Body.Bytes(), &actors); err != nil {
		t.Fatal(err)
	}
	if len(actors) != 1 || str(actors[0]["name"]) != "New Actor" || int(num(actors[0]["shared_works"])) != 3 {
		t.Fatalf("unexpected co-actor recommendation: %#v", actors)
	}
}

func TestRecommendationsExcludeOwnedDuplicatesButKeepMissingFiles(t *testing.T) {
	a := testApp(t)
	_ = a.put("works", Object{"id": "code:OWN-1", "code": "OWN-1", "actors": []string{"Fav"}, "files": []Object{{"missing": false}}})
	_ = a.put("works", Object{"id": "code:MISSING-2", "code": "MISSING-2", "actors": []string{"Fav"}, "files": []Object{{"missing": true}}})
	_ = a.put("catalog", Object{"id": "javdb:OWN-1", "source": "javdb", "code": "OWN-1", "actors": []string{"Fav"}, "rating": 4.5})
	_ = a.put("catalog", Object{"id": "fanza:OWN-1", "source": "fanza", "code": "OWN-1", "actors": []string{"Fav"}, "rating": 4.0})
	_ = a.put("catalog", Object{"id": "javdb:MISSING-2", "source": "javdb", "code": "MISSING-2", "actors": []string{"Fav"}, "rating": 4.0})

	w := callDiscovery(t, a, http.MethodGet, []string{"recommendations"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("recommendation request failed: %d %s", w.Code, w.Body.String())
	}
	var recommendations []Object
	if err := json.Unmarshal(w.Body.Bytes(), &recommendations); err != nil {
		t.Fatal(err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("recommendations = %#v, want only the catalog entry with unavailable local files", recommendations)
	}
	work := toObject(recommendations[0]["work"])
	if str(work["code"]) != "MISSING-2" {
		t.Fatalf("recommended work = %#v, want MISSING-2", work)
	}
}

func TestActorRecommendationsRequireConfirmedFemaleAndSupplementToThree(t *testing.T) {
	a := testApp(t)
	_ = a.put("works", Object{"id": "code:OWN-1", "code": "OWN-1", "actors": []string{"Fav"}, "files": []Object{{"missing": false}}})
	_ = a.put("actor_policies", Object{"id": "Fav", "name": "Fav", "policy": "follow"})
	_ = a.put("actor_policies", Object{"id": "Canonical Woman", "name": "Canonical Woman", "aliases": []string{"Woman Alias"}})
	_ = a.put("actor_profiles", Object{"id": "Canonical Woman", "name": "Canonical Woman", "gender": "female", "gender_source": "wikidata"})
	_ = a.put("actor_profiles", Object{"id": "Confirmed Male", "name": "Confirmed Male", "gender": "male", "gender_source": "wikidata"})
	_ = a.put("actor_profiles", Object{"id": "Unknown Actor", "name": "Unknown Actor", "gender": "unknown", "gender_source": "wikidata"})
	_ = a.put("actor_profiles", Object{"id": "Site Woman 2", "name": "Site Woman 2", "gender": "female", "gender_source": "wikidata"})
	_ = a.put("actor_profiles", Object{"id": "Site Woman 3", "name": "Site Woman 3", "gender": "female", "gender_source": "wikidata"})
	_ = a.put("catalog", Object{"id": "javdb:REC-1", "code": "REC-1", "actors": []string{"Fav", "Woman Alias", "Confirmed Male", "Unknown Actor"}})
	_ = a.put("provider_actor_recommendations", Object{"id": "javdbui", "actors": []Object{
		{"name": "Woman Alias", "gender": "female", "gender_source": "javdbui", "reason": "站点推荐"},
		{"name": "Site Woman 2", "gender": "female", "gender_source": "wikidata", "reason": "站点推荐"},
		{"name": "Site Woman 3", "gender": "female", "gender_source": "wikidata", "reason": "站点推荐"},
		{"name": "Confirmed Male", "gender": "male", "gender_source": "wikidata"},
		{"name": "Old Cache Missing Gender"},
	}})

	w := callDiscovery(t, a, http.MethodGet, []string{"actor-recommendations"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("actor recommendations failed: %d %s", w.Code, w.Body.String())
	}
	var recommendations []Object
	if err := json.Unmarshal(w.Body.Bytes(), &recommendations); err != nil {
		t.Fatal(err)
	}
	if len(recommendations) != 3 {
		t.Fatalf("wanted three confirmed female actor suggestions, got %#v", recommendations)
	}
	names := map[string]bool{}
	for _, actor := range recommendations {
		name := str(actor["name"])
		names[name] = true
		if name == "Confirmed Male" || name == "Unknown Actor" || name == "Old Cache Missing Gender" {
			t.Fatalf("male or unknown actor was recommended: %#v", recommendations)
		}
	}
	if !names["Canonical Woman"] || !names["Site Woman 2"] || !names["Site Woman 3"] {
		t.Fatalf("alias resolution or site supplementation failed: %#v", recommendations)
	}
}

func TestProviderGenderZeroIsFemaleButOtherOrMissingValuesStayUnknown(t *testing.T) {
	zero, one := 0, 1
	if javDBUIActorGender(&zero) != "female" || javDBUIActorGender(&one) != "unknown" || javDBUIActorGender(nil) != "unknown" {
		t.Fatalf("provider gender mapping guessed an unverified enum: zero=%q one=%q missing=%q", javDBUIActorGender(&zero), javDBUIActorGender(&one), javDBUIActorGender(nil))
	}
}
