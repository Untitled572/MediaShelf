package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListTorrentsInCategoriesScopesEachQueryAndDeduplicates(t *testing.T) {
	var categories []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		category := r.URL.Query().Get("category")
		if category == "" {
			t.Error("unfiltered torrent query is forbidden")
		}
		categories = append(categories, category)
		rows := []Torrent{{Hash: "same", Category: category}, {Hash: "outside", Category: "foreign"}}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	q, err := NewQBittorrent(QBConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.ListTorrentsInCategories(context.Background(), []string{"2D", "VR", "2D"})
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 2 || categories[0] != "2D" || categories[1] != "VR" {
		t.Fatalf("queries = %v, want one filtered query per distinct category", categories)
	}
	if len(got) != 1 || got[0].Hash != "same" || got[0].Category != "2D" {
		t.Fatalf("unexpected scoped results: %#v", got)
	}
}
