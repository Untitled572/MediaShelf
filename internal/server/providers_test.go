package server

import (
	"encoding/json"
	"testing"
)

func TestProviderSourcesExposeCapabilitiesAndHideCookie(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	_ = a.put("sources", Object{"id": "javdbui", "source": "javdbui", "cookie": "private-cookie", "proxy_url": "http://127.0.0.1:7890"})
	w := req(t, a, "GET", "/api/v1/sources", token, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var sources []Object
	json.Unmarshal(w.Body.Bytes(), &sources)
	if len(sources) != 6 {
		t.Fatal(len(sources))
	}
	for _, s := range sources {
		if _, exists := s["cookie"]; exists {
			t.Fatal("cookie disclosed")
		}
		if s["source"] == "javdbui" && (!flag(s["supports_recommendations"]) || flag(s["supports_actor_catalog"])) {
			t.Fatal(s)
		}
		if s["source"] == "jav321" && flag(s["supports_actor_catalog"]) {
			t.Fatal("lookup-only source advertises actor catalog")
		}
	}
	if req(t, a, "GET", "/api/v1/catalog/refresh", token, nil).Code != 405 {
		t.Fatal("GET must not import metadata")
	}
	if req(t, a, "POST", "/api/v1/catalog/refresh", token, Object{"source": "javbus"}).Code != 400 {
		t.Fatal("unsupported aggregate source accepted")
	}
}
func TestProviderActorRecommendationsExcludeKnownAliases(t *testing.T) {
	a := testApp(t)
	_ = a.put("works", Object{"id": "code:OWN-1", "actors": []string{"Known"}})
	_ = a.put("actor_policies", Object{"id": "Known", "name": "Known", "aliases": []string{"Alias"}})
	_ = a.put("provider_actor_recommendations", Object{"id": "javdbui", "actors": []Object{{"name": "Known"}, {"name": "Alias"}, {"name": "New", "reason": "JavDB UI · 站点推荐演员"}}})
	got := a.providerActorRecommendations()
	if len(got) != 1 || str(got[0]["name"]) != "New" {
		t.Fatalf("known actors leaked into site recommendations: %#v", got)
	}
}
