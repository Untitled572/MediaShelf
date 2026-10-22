package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mediashelf/internal/integrations"
)

func TestRunQBitAllowsUnauthenticatedAllowlistedServer(t *testing.T) {
	var versionRequests, torrentRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			versionRequests.Add(1)
			_, _ = w.Write([]byte("v4.6.7"))
		case "/api/v2/torrents/info":
			torrentRequests.Add(1)
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			t.Errorf("unexpected qBittorrent request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	runner := NewRunner(Config{Downloaders: map[string]Downloader{"local": {URL: srv.URL}}}, nil)
	payload := qbitPayload{DownloaderID: "local", Operation: "list"}
	payload.Config.URL = srv.URL
	result, err := runner.runQBit(context.Background(), payload)
	if err != nil {
		t.Fatalf("allowlisted unauthenticated qBittorrent request failed: %v", err)
	}
	torrents, ok := result.([]integrations.Torrent)
	if !ok || len(torrents) != 0 {
		t.Fatalf("torrent result = %#v, want empty torrent list", result)
	}
	if versionRequests.Load() != 1 || torrentRequests.Load() != 1 {
		t.Fatalf("version/list requests = %d/%d, want 1/1", versionRequests.Load(), torrentRequests.Load())
	}
}

func TestRunQBitStillReliesOnLoginForAuthentication(t *testing.T) {
	var loginRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case "/api/v2/auth/login":
			loginRequests.Add(1)
			t.Error("agent must not submit an empty login after anonymous probe failure")
			http.Error(w, "Fails.", http.StatusUnauthorized)
		default:
			t.Errorf("unexpected qBittorrent request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	runner := NewRunner(Config{Downloaders: map[string]Downloader{"local": {URL: srv.URL}}}, nil)
	payload := qbitPayload{DownloaderID: "local", Operation: "list"}
	payload.Config.URL = srv.URL
	if _, err := runner.runQBit(context.Background(), payload); err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("authenticated endpoint error = %v, want credentials-required error", err)
	}
	if loginRequests.Load() != 0 {
		t.Fatalf("Login made %d empty auth attempts, want 0", loginRequests.Load())
	}
}

func TestConfigRequiresAbsoluteLocalAllowlist(t *testing.T) {
	tmp := t.TempDir()
	c := Config{ServerURL: "http://127.0.0.1:8765", Token: "secret", Libraries: map[string]string{"lib": tmp}, Tools: map[string]Tool{"player": {Path: filepath.Join(tmp, "player.exe")}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid local config rejected: %v", err)
	}
	if _, ok := c.LibraryRoot("server-added"); ok {
		t.Fatal("server-only library must not be locally allowlisted")
	}
	if _, ok := c.LocalTool("server-added"); ok {
		t.Fatal("server-only tool must not be locally allowlisted")
	}
	c.Libraries["bad"] = "relative/path"
	if err := c.Validate(); err == nil {
		t.Fatal("relative library root accepted")
	}
}

func TestConfigRejectsNonHTTPServerAndUnsafeScriptPath(t *testing.T) {
	tmp := t.TempDir()
	c := Config{ServerURL: "file:///etc/passwd", Token: "x", Libraries: map[string]string{"lib": tmp}}
	if err := c.Validate(); err == nil {
		t.Fatal("non-http server URL accepted")
	}
	c.ServerURL = "https://example.test"
	c.Tools = map[string]Tool{"script": {Path: filepath.Join(tmp, "unsafe%PATH%.cmd")}}
	if err := c.Validate(); err == nil {
		t.Fatal("command expansion characters accepted in tool path")
	}
}

func TestDownloaderURLAllowlistCannotBeExpandedByJob(t *testing.T) {
	c := Config{ServerURL: "http://127.0.0.1:8765", Token: "secret", Libraries: map[string]string{"lib": t.TempDir()}, Downloaders: map[string]Downloader{"qb": {URL: "http://127.0.0.1:8080/qb"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(c, nil)
	p := qbitPayload{DownloaderID: "qb", Operation: "list"}
	p.Config.URL = "http://127.0.0.1:8081/qb"
	if _, err := r.runQBit(nil, p); err == nil {
		t.Fatal("job reached a URL outside the local allowlist")
	}
	p.DownloaderID = "server-added"
	if _, err := r.runQBit(nil, p); err == nil {
		t.Fatal("server-only downloader ID was accepted")
	}
	if sameHTTPURL(c.Downloaders["qb"].URL, "http://127.0.0.1:8080/other") {
		t.Fatal("different URL path matched allowlist")
	}
	if sameHTTPURL(c.Downloaders["qb"].URL, "http://user:pass@127.0.0.1:8080/qb") {
		t.Fatal("URL credentials matched allowlist")
	}
}
