package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteImageCacheServesOfflineAfterFirstFetch(t *testing.T) {
	a := testApp(t)
	calls := 0
	fetch := func(_ context.Context, raw, proxy string) ([]byte, string, error) {
		calls++
		if raw != "https://images.example/cover.jpg" || proxy != "http://127.0.0.1:7890" {
			t.Fatalf("unexpected fetch args: %q via %q", raw, proxy)
		}
		return []byte("raster bytes"), "image/jpeg", nil
	}
	path := "/api/v1/images?url=https%3A%2F%2Fimages.example%2Fcover.jpg"
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		a.serveRemoteImageWith(w, r, fetch)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), []byte("raster bytes")) {
			t.Fatalf("unexpected response %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("expected one upstream fetch, got %d", calls)
	}
	files, err := filepath.Glob(filepath.Join(a.dataDir, "online-covers", "*"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one cached file, files=%v err=%v", files, err)
	}
}

func TestRemoteImageRejectsUnsupportedTypeAndOversize(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		mime string
	}{
		{name: "svg", body: []byte("<svg/>"), mime: "image/svg+xml"},
		{name: "oversize", body: bytes.Repeat([]byte{'x'}, (5<<20)+1), mime: "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testApp(t)
			fetch := func(context.Context, string, string) ([]byte, string, error) { return tt.body, tt.mime, nil }
			r := httptest.NewRequest(http.MethodGet, "/api/v1/images?url=https%3A%2F%2Fimages.example%2Fimg", nil)
			w := httptest.NewRecorder()
			a.serveRemoteImageWith(w, r, fetch)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("expected 502 rejection, got %d: %s", w.Code, w.Body.String())
			}
			files, err := filepath.Glob(filepath.Join(a.dataDir, "online-covers", "*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("rejected image was cached: %v, %v", files, err)
			}
		})
	}
}

func TestRemoteImageCacheExpiryFetchesAgain(t *testing.T) {
	a := testApp(t)
	calls := 0
	fetch := func(context.Context, string, string) ([]byte, string, error) {
		calls++
		return []byte(fmt.Sprintf("image %d", calls)), "image/png", nil
	}
	path := "/api/v1/images?url=https%3A%2F%2Fimages.example%2Fexpired.png"
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	a.serveRemoteImageWith(w, r, fetch)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	files, _ := filepath.Glob(filepath.Join(a.dataDir, "online-covers", "*.png"))
	if len(files) != 1 {
		t.Fatalf("cache file missing: %v", files)
	}
	expired := time.Now().Add(-onlineImageCacheTTL - time.Minute)
	if err := os.Chtimes(files[0], expired, expired); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodGet, path, nil)
	w = httptest.NewRecorder()
	a.serveRemoteImageWith(w, r, fetch)
	if w.Code != 200 || calls != 2 {
		t.Fatalf("expired cache was not refetched, code=%d calls=%d", w.Code, calls)
	}
}

func TestImageQueryTokenAuthenticationIsNarrowlyScoped(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/images?token=" + token, 400},
		{"/api/v1/images?token=invalid", 401},
		{"/api/v1/works?token=" + token, 401},
	} {
		w := req(t, a, http.MethodGet, tc.path, "", nil)
		if w.Code != tc.want {
			t.Fatalf("path auth status = %d want %d", w.Code, tc.want)
		}
	}
}

func TestRemoteImageDirectRedirectDoesNotFetchOrCache(t *testing.T) {
	a := testApp(t)
	target := "https://8.8.8.8/cover.jpg?size=small"
	r := httptest.NewRequest(http.MethodGet, "/api/v1/images?redirect=1&url="+url.QueryEscape(target), nil)
	w := httptest.NewRecorder()
	a.serveRemoteImageWith(w, r, func(context.Context, string, string) ([]byte, string, error) {
		t.Fatal("redirect fetched image body")
		return nil, "", nil
	})
	if w.Code != 302 || w.Header().Get("Location") != target || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal(w.Code, w.Header())
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "online-covers")); !os.IsNotExist(err) {
		t.Fatal("redirect created cache")
	}
	for _, target := range []string{"http://127.0.0.1/private", "http://192.168.1.2/img", "file:///secret"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/images?redirect=1&url="+url.QueryEscape(target), nil)
		w := httptest.NewRecorder()
		a.serveRemoteImageWith(w, r, nil)
		if w.Code != 400 {
			t.Fatalf("redirected unsafe target %s: %d", target, w.Code)
		}
	}
}
func TestEncryptedRemoteImageKeepsDecodingPath(t *testing.T) {
	a := testApp(t)
	calls := 0
	r := httptest.NewRequest(http.MethodGet, "/api/v1/images?redirect=1&url=https%3A%2F%2Ftp.spfcas.com%2Fcover.jpg", nil)
	w := httptest.NewRecorder()
	a.serveRemoteImageWith(w, r, func(context.Context, string, string) ([]byte, string, error) {
		calls++
		return []byte("decoded raster"), "image/jpeg", nil
	})
	if w.Code != 200 || calls != 1 || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header())
	}
}
