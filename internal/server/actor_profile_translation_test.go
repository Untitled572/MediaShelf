package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cachedActorForTranslation(t *testing.T, a *App, extras Object) {
	t.Helper()
	profile := Object{
		"id": "alice example", "name": "Alice Example", "status": "found",
		"biography": "Public biography sentence.", "description": "Japanese adult film actress",
		"sources": []Object{{"label": "Wikipedia", "url": "https://en.wikipedia.org/wiki/Alice_Example"}, {"label": "Wikidata", "url": "https://www.wikidata.org/wiki/Q100"}},
	}
	for k, v := range extras {
		profile[k] = v
	}
	if err := a.put("actor_profiles", profile); err != nil {
		t.Fatal(err)
	}
}

func TestActorProfileTranslationPrefersSameQIDChineseWikipediaAndCaches(t *testing.T) {
	var wikiDataRequests, wikiRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wikidata-zh":
			wikiDataRequests++
			if r.URL.Query().Get("ids") != "Q100" || r.URL.Query().Get("props") != "sitelinks|descriptions" {
				t.Errorf("unexpected Wikidata query %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"entities":{"Q100":{"descriptions":{"zh-hans":{"value":"中文演员描述"}},"sitelinks":{"zhwiki":{"title":"演员条目"}}}}}`))
		case "/wikipedia-zh":
			wikiRequests++
			if r.URL.Query().Get("titles") != "演员条目" || r.URL.Query().Get("variant") != "zh-cn" {
				t.Errorf("wrong same-QID sitelink query: %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"演员条目","extract":"中文百科简介。"}]}}`))
		case "/mymemory":
			t.Error("MyMemory should not be used when same-QID Chinese Wikipedia exists")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := testApp(t)
	a.actorProfileAPIBase = srv.URL
	cachedActorForTranslation(t, a, nil)
	token := login(t, a)
	path := "/api/v1/actors/Alice%20Example/profile/translate"
	for i := 0; i < 2; i++ {
		response := req(t, a, http.MethodPost, path, token, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("translation request %d: %d %s", i, response.Code, response.Body.String())
		}
		var profile Object
		if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
			t.Fatal(err)
		}
		if str(profile["biography_zh"]) != "中文百科简介。" || str(profile["description_zh"]) != "中文演员描述" || str(profile["translation_provider"]) != "wikipedia_zh" || !strings.HasPrefix(str(profile["translation_source_url"]), "https://zh.wikipedia.org/wiki/") {
			t.Fatalf("Chinese Wikipedia translation fields missing: %#v", profile)
		}
	}
	if wikiDataRequests != 1 || wikiRequests != 1 {
		t.Fatalf("cached repeat made requests: Wikidata=%d Wikipedia=%d", wikiDataRequests, wikiRequests)
	}
}

func TestActorProfileTranslationFallsBackAndDoesNotDamageCacheOnFailure(t *testing.T) {
	var myMemoryRequests int
	malformed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wikidata-zh":
			_, _ = w.Write([]byte(`{"entities":{"Q100":{"descriptions":{"zh":{"value":""}},"sitelinks":{}}}}`))
		case "/mymemory":
			myMemoryRequests++
			if r.URL.Query().Get("langpair") != "en|zh-CN" || r.URL.Query().Get("q") != "Public biography sentence." {
				t.Errorf("unexpected MyMemory query: %v", r.URL.Query())
			}
			if malformed {
				_, _ = w.Write([]byte(`{"responseStatus":200,"quotaFinished":true,"responseData":{"translatedText":"配额已用完"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"responseStatus":200,"quotaFinished":false,"responseData":{"translatedText":"中文译文"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := testApp(t)
	a.actorProfileAPIBase = srv.URL
	cachedActorForTranslation(t, a, nil)
	token := login(t, a)
	path := "/api/v1/actors/Alice%20Example/profile/translate"
	response := req(t, a, http.MethodPost, path, token, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("fallback translation failed: %d %s", response.Code, response.Body.String())
	}
	var profile Object
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if str(profile["biography_zh"]) != "中文译文" || str(profile["translation_provider"]) != "mymemory" || str(profile["translation_source_url"]) != myMemoryEndpoint {
		t.Fatalf("fallback translation fields missing: %#v", profile)
	}
	profile["biography_zh"] = "先前的翻译"
	profile["translation_source_hash"] = "stale"
	if err := a.put("actor_profiles", profile); err != nil {
		t.Fatal(err)
	}
	malformed = true
	response = req(t, a, http.MethodPost, path, token, Object{"force": true})
	if response.Code != http.StatusBadGateway {
		t.Fatalf("malformed translation status = %d: %s", response.Code, response.Body.String())
	}
	response = req(t, a, http.MethodGet, "/api/v1/actors/Alice%20Example/profile", token, nil)
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil || str(profile["biography_zh"]) != "先前的翻译" {
		t.Fatalf("failed translation changed cached content: %#v err=%v", profile, err)
	}
	if myMemoryRequests != 2 {
		t.Fatalf("MyMemory fallback requests = %d, want 2", myMemoryRequests)
	}
}

func TestActorProfileTranslationRequiresCachedBiography(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	response := req(t, a, http.MethodPost, "/api/v1/actors/Uncached/profile/translate", token, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("uncached translation status=%d: %s", response.Code, response.Body.String())
	}
}

func TestSplitTranslationTextPreservesTextAndCapsUTF8Chunks(t *testing.T) {
	text := strings.Repeat("日本語の文章です。", 100)
	chunks := splitTranslationText(text, 500)
	if len(chunks) < 2 || strings.Join(chunks, "") != text {
		t.Fatalf("chunking lost text or did not split: chunks=%d", len(chunks))
	}
	for i, chunk := range chunks {
		if len([]byte(chunk)) > 500 {
			t.Fatalf("chunk %d is %d bytes", i, len([]byte(chunk)))
		}
	}
}

func TestParseMyMemoryTranslationRejectsBadQuotaAndEchoResponses(t *testing.T) {
	for _, raw := range []string{
		`{"responseStatus":429,"quotaFinished":false,"responseData":{"translatedText":"译文"}}`,
		`{"responseStatus":200,"quotaFinished":true,"responseData":{"translatedText":"译文"}}`,
		`{"responseStatus":200,"responseData":{"translatedText":"译文"}}`,
		`{"responseStatus":200,"quotaFinished":false,"responseData":{"translatedText":"配额已用完"}}`,
		`{"responseStatus":200,"quotaFinished":false,"responseData":{"translatedText":"Original text"}}`,
	} {
		if _, err := parseMyMemoryTranslation([]byte(raw), "Original text"); err == nil {
			t.Fatalf("accepted invalid MyMemory response: %s", raw)
		}
	}
	if got, err := parseMyMemoryTranslation([]byte(`{"responseStatus":200,"quotaFinished":false,"responseData":{"translatedText":"你好"}}`), "こんにちは"); err != nil || got != "你好" {
		t.Fatalf("valid MyMemory translation rejected: got=%q err=%v", got, err)
	}
}
