package server

import (
	"testing"

	"mediashelf/internal/integrations"
)

func TestCatalogMergePreservesThumbnailAndFullCoverSeparately(t *testing.T) {
	a := testApp(t)
	const recordID = "javdbui:IPX-177"
	if err := a.mergeCatalog(Object{
		"id":            recordID,
		"source":        "javdbui",
		"code":          "IPX-177",
		"cover_url":     "https://img.example/legacy-list-image.jpg",
		"thumbnail_url": "https://img.example/list-thumb.jpg",
	}); err != nil {
		t.Fatal(err)
	}

	// A detail response replaces a legacy list image held in cover_url while
	// retaining the separately stored thumbnail.
	prior, err := a.get("catalog", recordID)
	if err != nil {
		t.Fatal(err)
	}
	enriched := mergeEnrichedCatalogMetadata(prior, integrations.Metadata{CoverURL: "https://img.example/detail-cover.jpg"})
	if err := a.mergeCatalog(enriched); err != nil {
		t.Fatal(err)
	}
	got, err := a.get("catalog", recordID)
	if err != nil {
		t.Fatal(err)
	}
	if str(got["thumbnail_url"]) != "https://img.example/list-thumb.jpg" || str(got["cover_url"]) != "https://img.example/detail-cover.jpg" {
		t.Fatalf("catalog merge conflated or lost thumbnail/full cover: %#v", got)
	}

	// A later list refresh may update the thumbnail without discarding full detail.
	if err := a.mergeCatalog(Object{
		"id":            recordID,
		"source":        "javdbui",
		"code":          "IPX-177",
		"thumbnail_url": "https://img.example/list-thumb-new.jpg",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = a.get("catalog", recordID)
	if err != nil {
		t.Fatal(err)
	}
	if str(got["thumbnail_url"]) != "https://img.example/list-thumb-new.jpg" || str(got["cover_url"]) != "https://img.example/detail-cover.jpg" {
		t.Fatalf("list refresh should update thumbnail and retain full cover: %#v", got)
	}
}
