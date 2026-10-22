package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanLibraryReadsMetadataGroupsFolderAndKeepsRelativePaths(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "VR", "ABP-123")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"disc1.mkv", "disc2.mp4", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("media"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(folder, "movie.nfo"), []byte(`<movie><title>Example title</title><year>2022</year><uniqueid>abp_123</uniqueid><actor><name>Alice</name></actor><genre>Drama</genre><genre>Drama</genre></movie>`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "poster.jpg"), []byte("cover"), 0644); err != nil {
		t.Fatal(err)
	}
	r := ScanLibrary("lib", root)
	if !r.Complete {
		t.Fatalf("scan incomplete: %v", r.Err)
	}
	if len(r.Works) != 1 {
		t.Fatalf("got %d works, want one grouped folder work", len(r.Works))
	}
	w := r.Works[0]
	if w.Code != "ABP-123" || w.Title != "Example title" || w.Year == nil || *w.Year != 2022 {
		t.Fatalf("unexpected NFO mapping: %+v", w)
	}
	if w.Kind != "VR" || len(w.Actors) != 1 || w.Actors[0] != "Alice" || len(w.Tags) != 1 || w.Tags[0] != "Drama" {
		t.Fatalf("unexpected classification: %+v", w)
	}
	if !w.HasNFO {
		t.Fatal("NFO presence marker was not set")
	}
	if len(w.Files) != 2 {
		t.Fatalf("got %d video files, want 2", len(w.Files))
	}
	if w.coverPath != filepath.Join(folder, "poster.jpg") {
		t.Fatalf("cover not discovered: %q", w.coverPath)
	}
	for _, f := range w.Files {
		if filepath.IsAbs(f.RelativePath) || f.LibraryID != "lib" || f.Size != 5 || f.ModifiedAt == "" {
			t.Fatalf("invalid relative file info: %+v", f)
		}
		if f.MediaKind != "segment" {
			t.Fatalf("disc marker should identify a segment conservatively: %+v", f)
		}
	}
}

func TestScanLibraryMergesLaterNFOIntoFilenameFallbackGroup(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ABC-123 part1.mkv", "ABC-123 part2.mkv"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("v"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	nfo := `<movie><title>Rich title from NFO</title><year>2021</year><actor><name>Known Actor</name></actor><genre>Drama</genre><rating>8.2</rating></movie>`
	if err := os.WriteFile(filepath.Join(root, "ABC-123 part2.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatal(err)
	}
	r := ScanLibrary("lib", root)
	if !r.Complete || len(r.Works) != 1 {
		t.Fatalf("scan complete=%v works=%d err=%v", r.Complete, len(r.Works), r.Err)
	}
	w := r.Works[0]
	if !w.HasNFO || w.Title != "Rich title from NFO" || w.Year == nil || *w.Year != 2021 || w.SourceRating == nil || *w.SourceRating != 8.2 || len(w.Actors) != 1 || w.Actors[0] != "Known Actor" || len(w.Tags) != 1 || w.Tags[0] != "Drama" || len(w.Files) != 2 {
		t.Fatalf("later NFO did not enrich the fallback group: %+v", w)
	}
}

func TestInferMediaHintsKeepsUnmarkedFilesUnknown(t *testing.T) {
	version, kind := inferMediaHints("ABP-123_001_1080p.mkv", "ABP-123")
	if version != "1080p+part-001" || kind != "segment" {
		t.Fatalf("segment hints = %q/%q", version, kind)
	}
	version, kind = inferMediaHints("ABP-123 Full 4K.mkv", "ABP-123")
	if version != "4k" || kind != "full" {
		t.Fatalf("explicit full hints = %q/%q", version, kind)
	}
	version, kind = inferMediaHints("ABP-123.mkv", "ABP-123")
	if version != "" || kind != "unknown" {
		t.Fatalf("unmarked hints = %q/%q; want unknown", version, kind)
	}
}

func TestScanLibraryGroupsSameRecognizedCodeAcrossSiblingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"XYZ-88 part 1.avi", "xyz_88 part 2.webm", "loose.mov"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("v"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	r := ScanLibrary("lib", root)
	if !r.Complete {
		t.Fatal(r.Err)
	}
	if len(r.Works) != 2 {
		t.Fatalf("got %d works, want code group plus loose file", len(r.Works))
	}
	var found bool
	for _, w := range r.Works {
		if w.Code == "XYZ-88" {
			found = true
			if len(w.Files) != 2 {
				t.Fatalf("recognized code group has %d files", len(w.Files))
			}
		}
	}
	if !found {
		t.Fatal("normalized code group missing")
	}
}

func TestScanLibraryPrefersTypedCodeAndStemNFOAndFindsJavSPPoster(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "VR", "release-folder")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	video := "FC2-PPV-1234567.mkv"
	for _, name := range []string{video, "part2.mkv"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("v"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// JavSP-style files can carry several external unique IDs. Only a typed
	// video number is a code; URL and scraper IDs must not become work codes.
	stemNFO := `<movie><title>Stem title</title><premiered>2024-05-22</premiered><uniqueid type="imdb" default="true">https://www.imdb.com/title/tt1234567/</uniqueid><uniqueid type="cid">99</uniqueid><uniqueid type="num">FC2-PPV-1234567</uniqueid><rating>8.5</rating><url>https://example.invalid/item</url><actor><name>Actor</name><thumb>https://example.invalid/actor.jpg</thumb></actor></movie>`
	folderNFO := `<movie><title>Folder fallback title</title><uniqueid type="dvdid">ABP-111</uniqueid></movie>`
	if err := os.WriteFile(filepath.Join(folder, "movie.nfo"), []byte(folderNFO), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "FC2-PPV-1234567.nfo"), []byte(stemNFO), 0644); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(folder, "FC2-PPV-1234567-poster.jpg")
	if err := os.WriteFile(poster, []byte("cover"), 0644); err != nil {
		t.Fatal(err)
	}
	r := ScanLibrary("lib", root)
	if !r.Complete || len(r.Works) != 1 {
		t.Fatalf("scan result complete=%v works=%d err=%v", r.Complete, len(r.Works), r.Err)
	}
	w := r.Works[0]
	if w.Code != "FC2-PPV-1234567" || w.Title != "Stem title" {
		t.Fatalf("typed code or stem NFO was not preferred: code=%q title=%q", w.Code, w.Title)
	}
	if w.Year == nil || *w.Year != 2024 {
		t.Fatalf("premiered year not mapped: %v", w.Year)
	}
	if w.SourceRating == nil || *w.SourceRating != 8.5 {
		t.Fatalf("rating not mapped: %v", w.SourceRating)
	}
	if w.SourceURL != "https://example.invalid/item" {
		t.Fatalf("source URL not mapped: %q", w.SourceURL)
	}
	if w.coverPath != poster {
		t.Fatalf("stem-poster cover not found: %q", w.coverPath)
	}
}
