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
