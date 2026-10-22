package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanLibraryKeepsRemoteActorThumbsAndMergesSameCode(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"ABC-123 part1.mkv": `<movie><actor><name>Alice</name><thumb>https://img.example/alice.jpg</thumb></actor></movie>`,
		"ABC-123 part2.mkv": `<movie><actor><name>Bob</name><thumb>http://img.example/bob.png</thumb></actor></movie>`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("video"), 0600); err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if err := os.WriteFile(filepath.Join(root, stem+".nfo"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got := ScanLibrary("lib", root)
	if !got.Complete || len(got.Works) != 1 {
		t.Fatalf("complete=%v works=%d err=%v", got.Complete, len(got.Works), got.Err)
	}
	thumbs := got.Works[0].ActorThumbs
	if thumbs["Alice"] != "https://img.example/alice.jpg" || thumbs["Bob"] != "http://img.example/bob.png" {
		t.Fatalf("merged actor thumbs = %#v", thumbs)
	}
}

func TestResolveActorThumbAcceptsOnlySafeURLsAndRootLocalFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "movie")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "alice.png")
	writeTestPNG(t, local)
	if path, isLocal, ok := resolveActorThumb(root, dir, "alice.png", "Alice"); !ok || !isLocal || path != local {
		t.Fatalf("local relative thumb = %q %v %v", path, isLocal, ok)
	}
	for _, raw := range []string{"../outside.jpg", filepath.Join(t.TempDir(), "absolute.png"), "javascript:alert(1)", "https://user:pass@img.example/a.jpg", "file:///C:/private/a.jpg"} {
		if value, _, ok := resolveActorThumb(root, dir, raw, "Alice"); ok {
			t.Errorf("unsafe thumb %q was accepted as %q", raw, value)
		}
	}
	for _, raw := range []string{"https://img.example/a.jpg", "http://img.example/a.png?size=small"} {
		if value, isLocal, ok := resolveActorThumb(root, dir, raw, "Alice"); !ok || isLocal || value != raw {
			t.Errorf("safe remote thumb %q => %q local=%v ok=%v", raw, value, isLocal, ok)
		}
	}
	if value, _, ok := resolveActorThumb(root, dir, "", "../Alice"); ok {
		t.Errorf("unsafe actor name generated thumb %q", value)
	}
}

func TestResolveActorThumbFindsEmbyActorsImageAndRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "movie")
	actors := filepath.Join(dir, ".actors")
	if err := os.MkdirAll(actors, 0700); err != nil {
		t.Fatal(err)
	}
	webp, err := decodeTestWebP()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(actors, "Alice.webp")
	if err := os.WriteFile(path, webp, 0600); err != nil {
		t.Fatal(err)
	}
	if got, local, ok := resolveActorThumb(root, dir, "", "Alice"); !ok || !local || got != path {
		t.Fatalf("Emby thumb = %q local=%v ok=%v", got, local, ok)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeTestPNG(t, outside)
	link := filepath.Join(dir, "escaped.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, ok := validateLibraryImagePath(root, link); ok {
		t.Fatalf("symlink target outside library accepted: %q", got)
	}
	// Alice has a valid in-library Emby image, so rejecting the explicit
	// symlink may safely fall back to that image rather than returning empty.
	if got, local, ok := resolveActorThumb(root, dir, "escaped.png", "Alice"); !ok || !local || got != path {
		t.Fatalf("safe avatar fallback = %q local=%v ok=%v", got, local, ok)
	}
	if got, _, ok := resolveActorThumb(root, dir, "escaped.png", "NoFallbackActor"); ok {
		t.Fatalf("symlink escape without safe fallback accepted: %q", got)
	}
}

func TestScanUploadsLocalActorThumbAndSendsServerURL(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "ABC-123.mkv")
	if err := os.WriteFile(video, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(root, "alice.png")
	writeTestPNG(t, thumb)
	if err := os.WriteFile(filepath.Join(root, "ABC-123.nfo"), []byte(`<movie><actor><name>Alice</name><thumb>alice.png</thumb></actor></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	var uploaded, scanned bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/covers":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "image/jpeg" {
				t.Errorf("unexpected upload request %s %s", r.Method, r.Header.Get("Content-Type"))
			}
			if _, _, err := image.DecodeConfig(r.Body); err != nil {
				t.Errorf("uploaded image is not decodable JPEG: %v", err)
			}
			uploaded = true
			_, _ = w.Write([]byte(`{"url":"/covers/alice.jpg"}`))
		case "/api/v1/agent/scan":
			var payload struct {
				Works []Work `json:"works"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			if len(payload.Works) != 1 || payload.Works[0].ActorThumbs["Alice"] != "/covers/alice.jpg" {
				t.Errorf("scan actor thumbs = %#v", payload.Works)
			}
			scanned = true
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	runner := NewRunner(Config{ServerURL: srv.URL, Token: "test", Libraries: map[string]string{"lib": root}}, nil)
	journal, err := OpenResultJournal(filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	runner.Journal = journal
	rc := &RemoteConfig{Libraries: []RemoteLibrary{{ID: "lib"}}}
	if err := runner.scanLibrary(context.Background(), "lib", &rc); err != nil {
		t.Fatal(err)
	}
	if !uploaded || !scanned {
		t.Fatalf("upload=%v scan=%v", uploaded, scanned)
	}
	if cached, ok := journal.CachedCover(thumb, mustStat(t, thumb).Size(), mustStat(t, thumb).ModTime()); !ok || cached != "/covers/alice.jpg" {
		t.Fatalf("avatar upload was not cached: %q %v", cached, ok)
	}
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func decodeTestWebP() ([]byte, error) {
	// A 1x1 lossless WebP fixture.
	return base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEAAUAmJaQAA3AA/v89WAAAAA==")
}
