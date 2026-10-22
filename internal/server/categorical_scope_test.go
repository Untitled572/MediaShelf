package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCategoricalDownloaderScopesListCategoriesAddAndActions(t *testing.T) {
	var added url.Values
	var mutationCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.0.0"))
		case "/api/v2/torrents/info":
			category := r.URL.Query().Get("category")
			if category == "" {
				t.Error("unfiltered torrent query is forbidden")
			}
			_ = json.NewEncoder(w).Encode([]Object{{"hash": category, "category": category}, {"hash": "foreign", "category": "other"}})
		case "/api/v2/torrents/categories":
			_ = json.NewEncoder(w).Encode(map[string]Object{"2D": {}, "VR": {}, "other": {}})
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			added = r.Form
			mutationCount++
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			mutationCount++
		default:
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	a := testApp(t)
	d := Object{"id": "local", "url": upstream.URL, "management_mode": "categorical", "managed_categories": []string{"2D", "VR"}}
	listed, err := a.qbit(context.Background(), d, "torrents", Object{})
	if err != nil || len(asObjects(listed)) != 2 {
		t.Fatalf("scoped list %#v: %v", listed, err)
	}
	categories, err := a.qbit(context.Background(), d, "categories", Object{})
	if err != nil || len(toObject(categories)) != 2 {
		t.Fatalf("scoped categories %#v: %v", categories, err)
	}
	for _, hashes := range [][]string{{"foreign"}, {"2D", "foreign"}} {
		if _, err := a.qbit(context.Background(), d, "action", Object{"action": "delete", "hashes": hashes}); err == nil {
			t.Fatalf("accepted out-of-scope hashes: %v", hashes)
		}
	}
	if mutationCount != 0 {
		t.Fatalf("blocked actions mutated upstream: %d", mutationCount)
	}
	if _, err := a.qbit(context.Background(), d, "add", Object{"url": "magnet:?xt=urn:btih:x", "category": "other", "save_path": "/downloads"}); err == nil {
		t.Fatal("accepted unconfigured category")
	}
	if _, err := a.qbit(context.Background(), d, "add", Object{"url": "magnet:?xt=urn:btih:x", "category": "VR", "save_path": "/downloads"}); err != nil {
		t.Fatal(err)
	}
	if added.Get("category") != "VR" || added.Get("autoTMM") != "true" || added.Get("savepath") != "" {
		t.Fatalf("categorical add did not use category/TMM: %v", added)
	}
}

func TestCategoricalDownloaderDefaultsAndRejectsInvalidCategories(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	w := req(t, a, http.MethodPost, "/api/v1/downloaders", token, Object{"url": "http://localhost:8080", "management_mode": "categorical"})
	if w.Code != http.StatusOK {
		t.Fatalf("default categories rejected: %d %s", w.Code, w.Body.String())
	}
	for _, categories := range [][]string{{}, {"2D", ""}, {"2D", "2D"}, {"bad,name"}, {"  "}} {
		w = req(t, a, http.MethodPost, "/api/v1/downloaders", token, Object{"url": "http://localhost:8080", "management_mode": "categorical", "managed_categories": categories})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid categories %q: %d", categories, w.Code)
		}
	}
	w = req(t, a, http.MethodPost, "/api/v1/downloaders", token, Object{"url": "http://localhost:8080", "management_mode": "categorical", "managed_categories": []any{"2D", 2}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("accepted non-string category: %d", w.Code)
	}
}
