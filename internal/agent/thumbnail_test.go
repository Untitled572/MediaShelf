package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMakeCoverThumbnailScalesAndDoesNotModifySource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cover.png")
	src := image.NewRGBA(image.Rect(0, 0, 1200, 1800))
	src.Set(20, 20, color.RGBA{R: 255, A: 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := MakeCoverThumbnail(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width > 400 || cfg.Height > 600 || cfg.Width < 1 || cfg.Height < 1 {
		t.Fatalf("thumbnail %s %dx%d", format, cfg.Width, cfg.Height)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("source cover was modified")
	}
}

func TestCoverCachePersistsAndInvalidatesByFileMetadata(t *testing.T) {
	p := filepath.Join(t.TempDir(), "journal.json")
	j, err := OpenResultJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 123)
	if err = j.CacheCover("C:/Media/poster.jpg", 12, stamp, "/covers/a.jpg"); err != nil {
		t.Fatal(err)
	}
	j, err = OpenResultJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := j.CachedCover("C:/Media/poster.jpg", 12, stamp); !ok || u != "/covers/a.jpg" {
		t.Fatalf("cached cover %q %v", u, ok)
	}
	if _, ok := j.CachedCover("C:/Media/poster.jpg", 13, stamp); ok {
		t.Fatal("size change did not invalidate cache")
	}
	if _, ok := j.CachedCover("C:/Media/poster.jpg", 12, stamp.Add(time.Second)); ok {
		t.Fatal("mtime change did not invalidate cache")
	}
}
