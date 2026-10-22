package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mediashelf/internal/integrations"
)

func TestRunQBitListsOnlyConfiguredCategories(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.0.0"))
		case "/api/v2/torrents/info":
			category := r.URL.Query().Get("category")
			if category == "" {
				t.Error("unfiltered torrent query is forbidden")
			}
			queries = append(queries, category)
			_ = json.NewEncoder(w).Encode([]integrations.Torrent{{Hash: category, Category: category}})
		default:
			t.Errorf("unexpected qBittorrent request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	runner := NewRunner(Config{Downloaders: map[string]Downloader{"local": {URL: srv.URL}}}, nil)
	payload := qbitPayload{DownloaderID: "local", Operation: "torrents"}
	payload.Config.URL = srv.URL
	payload.Params = json.RawMessage(`{"categories":["2D","VR"]}`)
	result, err := runner.runQBit(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	rows := result.([]integrations.Torrent)
	if len(queries) != 2 || queries[0] != "2D" || queries[1] != "VR" || len(rows) != 2 {
		t.Fatalf("queries=%v results=%#v", queries, rows)
	}
}
