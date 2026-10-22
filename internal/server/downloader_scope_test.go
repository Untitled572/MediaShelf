package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTaggedDownloaderScopesReadsAddsAndActions(t *testing.T) {
	var mutations []string
	var added url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			w.Write([]byte("v5.3.0"))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("tag") != "MediaShelf" {
				t.Errorf("unscoped list: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode([]Object{{"hash": "abc", "tags": "Other, MediaShelf"}, {"hash": "foreign", "tags": "Other"}, {"hash": "prefix", "tags": "MediaShelf-Other"}})
		case "/api/v2/torrents/createTags":
			r.ParseForm()
			if r.Form.Get("tags") != "MediaShelf" {
				t.Errorf("wrong tag: %v", r.Form)
			}
			mutations = append(mutations, "create")
		case "/api/v2/torrents/add":
			r.ParseForm()
			added = r.Form
			mutations = append(mutations, "add")
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			r.ParseForm()
			if r.Form.Get("hashes") != "abc" || r.Form.Get("deleteFiles") != "false" {
				t.Errorf("unexpected delete: %v", r.Form)
			}
			mutations = append(mutations, "delete")
		default:
			t.Errorf("unexpected upstream: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	a := testApp(t)
	d := Object{"id": "remote", "url": upstream.URL, "management_mode": "uncategorized"}
	got, err := a.qbit(context.Background(), d, "torrents", Object{})
	if err != nil || len(asObjects(got)) != 1 {
		t.Fatalf("scope failed: %#v %v", got, err)
	}
	for _, hashes := range [][]string{{"foreign"}, {"abc", "foreign"}, {"all"}, {"prefix"}} {
		if _, err = a.qbit(context.Background(), d, "action", Object{"action": "delete", "hashes": hashes}); err == nil {
			t.Fatalf("out-of-scope action accepted: %v", hashes)
		}
	}
	if len(mutations) != 0 {
		t.Fatalf("blocked action mutated upstream: %v", mutations)
	}
	_, err = a.qbit(context.Background(), d, "action", Object{"action": "delete", "hashes": []string{"abc"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.qbit(context.Background(), d, "add", Object{"url": "magnet:?xt=urn:btih:new", "category": "unwanted", "tags": "Personal", "save_path": "/downloads"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Get("tags") != "Personal,MediaShelf" || added.Get("category") != "" || added.Get("autoTMM") != "false" || added.Get("savepath") != "/downloads" {
		t.Fatalf("wrong add: %v", added)
	}
	if strings.Join(mutations, ",") != "delete,create,add" {
		t.Fatalf("wrong operation order: %v", mutations)
	}
	if _, err = a.qbit(context.Background(), d, "categories", Object{}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedTagConfigValidation(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	for _, tag := range []string{"a,b", "a|b", "a\nb", strings.Repeat("x", 101)} {
		w := req(t, a, "POST", "/api/v1/downloaders", token, Object{"url": "http://localhost:8080", "management_mode": "uncategorized", "managed_tag": tag})
		if w.Code != 400 {
			t.Fatalf("accepted invalid tag %q: %d", tag, w.Code)
		}
	}
	w := req(t, a, "POST", "/api/v1/downloaders", token, Object{"id": "remote", "url": "http://localhost:8080", "management_mode": "uncategorized"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	d, _ := a.get("downloaders", "remote")
	if str(d["managed_tag"]) != "MediaShelf" {
		t.Fatal(d)
	}
	if downloaderTag(Object{"management_mode": "categorical"}) != "" {
		t.Fatal("local category mode must remain unscoped")
	}
}
