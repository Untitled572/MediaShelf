package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActorProfileRefreshUsesExactCanonicalNameAndCachesForOfflineGET(t *testing.T) {
	var wikipediaRequests, wikidataRequests int
	var failWikidata bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wikipedia-ja", "/wikipedia-en":
			wikipediaRequests++
			if got := r.URL.Query().Get("titles"); got != "Alice Example" {
				t.Errorf("Wikipedia title = %q, want exact canonical actor name", got)
			}
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Alice Example","extract":"日本のAV女優。Public biography text.","pageprops":{"wikibase_item":"Q100"},"thumbnail":{"source":"https://upload.wikimedia.org/example.jpg"}}]}}`))
		case "/wikidata":
			wikidataRequests++
			if failWikidata {
				http.Error(w, "temporary error", http.StatusServiceUnavailable)
				return
			}
			if strings.Contains(r.URL.Query().Get("props"), "claims") {
				_, _ = w.Write([]byte(`{"entities":{"Q100":{"labels":{"en":{"value":"Alice Example"}},"descriptions":{"en":{"value":"Japanese adult film actress"}},"claims":{"P106":[{"mainsnak":{"datavalue":{"value":{"id":"Q488111"}}}}],"P569":[{"mainsnak":{"datavalue":{"value":{"time":"+1990-04-05T00:00:00Z"}}}}],"P19":[{"mainsnak":{"datavalue":{"value":{"id":"Q10"}}}}],"P27":[{"mainsnak":{"datavalue":{"value":{"id":"Q20"}}}}],"P2002":[{"mainsnak":{"datavalue":{"value":"alice_example"}}}],"P2003":[{"mainsnak":{"datavalue":{"value":"alice.ig"}}}],"P2397":[{"mainsnak":{"datavalue":{"value":"UCchannel123"}}}],"P856":[{"mainsnak":{"datavalue":{"value":"https://alice.example/"}}}]}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"entities":{"Q100":{"labels":{"en":{"value":"Alice Example"}}},"Q10":{"labels":{"en":{"value":"Tokyo"}}},"Q20":{"labels":{"en":{"value":"Japan"}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.actorProfileAPIBase = srv.URL
	_ = a.put("actor_policies", Object{"id": "Alice Example", "name": "Alice Example", "aliases": []string{"A. Example"}, "policy": "follow"})
	token := login(t, a)

	uncached := req(t, a, http.MethodGet, "/api/v1/actors/A.%20Example/profile", token, nil)
	if uncached.Code != http.StatusOK {
		t.Fatalf("uncached GET status %d: %s", uncached.Code, uncached.Body.String())
	}
	var profile Object
	if err := json.Unmarshal(uncached.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if str(profile["status"]) != "not_fetched" || wikipediaRequests != 0 || wikidataRequests != 0 {
		t.Fatalf("uncached GET fetched remotely or had wrong status: profile=%#v requests=%d/%d", profile, wikipediaRequests, wikidataRequests)
	}

	refreshed := req(t, a, http.MethodPost, "/api/v1/actors/A.%20Example/profile", token, nil)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status %d: %s", refreshed.Code, refreshed.Body.String())
	}
	if err := json.Unmarshal(refreshed.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if str(profile["name"]) != "Alice Example" || str(profile["description"]) != "Japanese adult film actress" || !strings.Contains(str(profile["biography"]), "Public biography text.") || str(profile["birth_date"]) != "1990-04-05" || str(profile["birth_place"]) != "Tokyo" || !contains(profile["nationality"], "Japan") || !contains(profile["aliases"], "A. Example") || str(profile["status"]) != "found" {
		t.Fatalf("profile fields not decoded: %#v", profile)
	}
	links := asObjects(profile["links"])
	for _, label := range []string{"X", "Instagram", "YouTube", "Official website"} {
		found := false
		for _, link := range links {
			if str(link["label"]) == label && safeOfficialURL(str(link["url"])) != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing verified %s link in %#v", label, links)
		}
	}
	if str(profile["avatar_url"]) != "https://upload.wikimedia.org/example.jpg" || len(asObjects(profile["sources"])) != 2 {
		t.Fatalf("source or avatar missing: %#v", profile)
	}

	requestsAfterRefresh := wikipediaRequests + wikidataRequests
	cached := req(t, a, http.MethodGet, "/api/v1/actors/Alice%20Example/profile", token, nil)
	if cached.Code != http.StatusOK || wikipediaRequests+wikidataRequests != requestsAfterRefresh {
		t.Fatalf("cached GET made outbound requests: status=%d requests=%d", cached.Code, wikipediaRequests+wikidataRequests)
	}

	failWikidata = true
	failedRefresh := req(t, a, http.MethodPost, "/api/v1/actors/Alice%20Example/profile", token, nil)
	if failedRefresh.Code != http.StatusBadGateway {
		t.Fatalf("failed refresh status = %d, want 502: %s", failedRefresh.Code, failedRefresh.Body.String())
	}
	cached = req(t, a, http.MethodGet, "/api/v1/actors/Alice%20Example/profile", token, nil)
	if err := json.Unmarshal(cached.Body.Bytes(), &profile); err != nil || str(profile["description"]) != "Japanese adult film actress" {
		t.Fatalf("failed refresh damaged old cache: profile=%#v err=%v", profile, err)
	}
}

func TestActorProfileRefreshCachesExplicitNotFoundAndRejectsDifferentTitle(t *testing.T) {
	var title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		title = r.URL.Query().Get("titles")
		if title == "Unknown Person" {
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Unknown Person","missing":true}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Other Person","extract":"wrong identity","pageprops":{"wikibase_item":"Q9"}}]}}`))
	}))
	defer srv.Close()
	a := testApp(t)
	a.actorProfileAPIBase = srv.URL
	token := login(t, a)

	missing := req(t, a, http.MethodPost, "/api/v1/actors/Unknown%20Person/profile", token, nil)
	if missing.Code != http.StatusOK {
		t.Fatalf("not found refresh status %d: %s", missing.Code, missing.Body.String())
	}
	var profile Object
	if err := json.Unmarshal(missing.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if str(profile["status"]) != "not_found" || len(asObjects(profile["links"])) != 0 {
		t.Fatalf("not-found profile should have no verified links: %#v", profile)
	}
	badIdentity := req(t, a, http.MethodPost, "/api/v1/actors/Expected%20Name/profile", token, nil)
	if badIdentity.Code != http.StatusBadGateway || title != "Expected Name" {
		t.Fatalf("mismatched identity was accepted: status=%d title=%q body=%s", badIdentity.Code, title, badIdentity.Body.String())
	}
	uncached := req(t, a, http.MethodGet, "/api/v1/actors/Expected%20Name/profile", token, nil)
	if err := json.Unmarshal(uncached.Body.Bytes(), &profile); err != nil || str(profile["status"]) != "not_fetched" {
		t.Fatalf("mismatched page polluted cache: profile=%#v err=%v", profile, err)
	}
}

func TestActorProfileURLsAndClaimParsingStayBounded(t *testing.T) {
	if safeOfficialURL("file:///etc/passwd") != "" || safeOfficialURL("https://127.0.0.1/admin") != "" || safeOfficialURL("https://user:pass@example.test") != "" {
		t.Fatal("unsafe official link was accepted")
	}
	if safeWikimediaThumbnail("https://example.test/image.jpg") != "" || safeWikimediaThumbnail("http://upload.wikimedia.org/image.jpg") != "" {
		t.Fatal("thumbnail outside HTTPS upload.wikimedia.org was accepted")
	}
	if !actorProfileNameMatch("鈴村あいり", "鈴村 あいり") || actorProfileNameMatch("Ann A", "Anna") {
		t.Fatal("Japanese title spacing normalization accepted or rejected the wrong identity")
	}
}
