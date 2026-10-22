package integrations

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type javDBUIRoundTripFunc func(*http.Request) (*http.Response, error)

func (f javDBUIRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func javDBUITestClient(t *testing.T, fn javDBUIRoundTripFunc) *JavDBUIClient {
	t.Helper()
	c, err := NewJavDBUIClient(CatalogConfig{BaseURL: "https://api.example/api"})
	if err != nil {
		t.Fatal(err)
	}
	c.http = &http.Client{Transport: fn}
	return c
}

func javDBUIJSONResponse(req *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func TestJavDBUIRecommendationsUsePublicSignatureAndMapMovies(t *testing.T) {
	calls := 0
	client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/api/v1/movies/recommend" {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		if got := req.Header.Get("jdsignature"); !strings.Contains(got, ".lpw6vgqzsp.") {
			t.Fatalf("missing public signature: %q", got)
		}
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("public API request unexpectedly used account credentials")
		}
		return javDBUIJSONResponse(req, http.StatusOK, `{"success":1,"data":{"period":7,"movies":[{"id":"nxym","number":"ABP-001","title":"Sample","cover_url":"https://img.example/cover.jpg","score":"4.46","release_date":"2012-10-26"}]}}`)
	})

	got, err := client.Recommendations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(got) != 1 {
		t.Fatalf("expected one mapped recommendation, calls=%d got=%#v", calls, got)
	}
	if got[0].Code != "ABP-001" || got[0].ProviderID != "nxym" || got[0].Title != "Sample" || got[0].Year != 2012 || got[0].Source != "javdbui" || got[0].SourceURL != defaultJavDBUIWebBase || got[0].Rating == nil || *got[0].Rating != 4.46 {
		t.Fatalf("unexpected mapped recommendation: %#v", got[0])
	}
}

func TestJavDBUILookupSearchesExactCodeAndEnriches(t *testing.T) {
	calls := 0
	client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		switch req.URL.Path {
		case "/api/v2/search":
			if req.URL.Query().Get("q") != "ABP-001" || req.URL.Query().Get("type") != "movie" {
				t.Fatalf("unexpected search query: %s", req.URL.RawQuery)
			}
			return javDBUIJSONResponse(req, http.StatusOK, `{"success":1,"data":{"current_page":1,"movies":[{"id":"nxym","number":"ABP-001","title":"Search title","cover_url":"https://img.example/a.jpg"},{"id":"other","number":"ABP-0010","title":"Wrong match"}]}}`)
		case "/api/v4/movies/nxym":
			return javDBUIJSONResponse(req, http.StatusOK, `{"success":1,"data":{"movie":{"id":"nxym","number":"ABP-001","title":"Detail title","release_date":"2012-10-26","score":"4.5","actors":[{"name":"Actor A"},{"name":"Actor B"}],"tags":[{"name":"Drama"}]}}}`)
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	got, err := client.Lookup(context.Background(), " abp-001 ")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || got.Code != "ABP-001" || got.Title != "Detail title" || got.Year != 2012 || len(got.Actors) != 2 || got.Actors[0] != "Actor A" || len(got.Tags) != 1 || got.Tags[0] != "Drama" {
		t.Fatalf("unexpected enriched result: calls=%d metadata=%#v", calls, got)
	}
}

func TestJavDBUISeparatesThumbnailAndFullCoverDuringEnrichment(t *testing.T) {
	list := javDBUIMetadata(javDBUIMovie{
		Number:   "IPX-177",
		CoverURL: "https://img.example/full.jpg",
		ThumbURL: "https://img.example/thumb.jpg",
	})
	if list.CoverURL != "https://img.example/full.jpg" || list.ThumbnailURL != "https://img.example/thumb.jpg" {
		t.Fatalf("JavDB UI list images were not separated: %#v", list)
	}

	detail := javDBUIMetadata(javDBUIMovie{
		Number:   "IPX-177",
		CoverURL: "https://img.example/full-detail.jpg",
	})
	merged := mergeJavDBUIMetadata(list, detail)
	if merged.CoverURL != "https://img.example/full-detail.jpg" || merged.ThumbnailURL != "https://img.example/thumb.jpg" {
		t.Fatalf("detail cover should enrich without replacing the list thumbnail: %#v", merged)
	}
}

func TestJavDBUILatestAndActorRecommendationsPreserveProviderLists(t *testing.T) {
	client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/v1/movies/latest":
			if req.URL.Query().Get("page") != "2" || req.URL.Query().Get("filter_by") != "all" {
				t.Fatalf("unexpected latest query: %s", req.URL.RawQuery)
			}
			return javDBUIJSONResponse(req, http.StatusOK, `{"success":1,"data":{"current_page":2,"movies":[]}}`)
		case "/api/v1/actors/recommend":
			return javDBUIJSONResponse(req, http.StatusOK, `{"success":1,"data":{"new_actors":[{"id":"a1","name":"New"}],"monthly_actors":[],"recommend_actors":[{"id":"a2","name":"Suggested"}]}}`)
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	latest, err := client.Latest(context.Background(), 2)
	if err != nil || latest == nil || len(latest) != 0 {
		t.Fatalf("expected provider's empty latest page, got %#v, err=%v", latest, err)
	}
	actors, err := client.RecommendedActors(context.Background())
	if err != nil || len(actors.NewActors) != 1 || len(actors.MonthlyActors) != 0 || len(actors.RecommendActors) != 1 {
		t.Fatalf("unexpected actor recommendations: %#v, err=%v", actors, err)
	}
}

func TestJavDBUIRejectsBusinessFailureAndMissingList(t *testing.T) {
	for name, body := range map[string]string{
		"business failure": `{"success":0,"action":"ParameterInvalid","message":"bad signature","data":null}`,
		"missing movies":   `{"success":1,"data":{"current_page":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
				return javDBUIJSONResponse(req, http.StatusOK, body)
			})
			if _, err := client.Latest(context.Background(), 1); err == nil {
				t.Fatal("expected malformed or unsuccessful response to fail")
			}
		})
	}
}

func TestJavDBUIActorRecommendationAcceptsNumericType(t *testing.T) {
	client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
		return javDBUIJSONResponse(req, 200, `{"success":1,"data":{"new_actors":[{"id":"A1","type":1,"name":"Actor"}],"monthly_actors":[],"recommend_actors":[]}}`)
	})
	result, err := client.RecommendedActors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NewActors) != 1 || result.NewActors[0].Name != "Actor" {
		t.Fatal(result)
	}
}

func TestJavDBUIActorGenderPreservesMissingAndZeroValues(t *testing.T) {
	client := javDBUITestClient(t, func(req *http.Request) (*http.Response, error) {
		return javDBUIJSONResponse(req, 200, `{"success":1,"data":{"new_actors":[{"id":"female","name":"Female","gender":0},{"id":"male","name":"Male","gender":1},{"id":"null","name":"Null","gender":null},{"id":"missing","name":"Missing"}],"monthly_actors":[],"recommend_actors":[]}}`)
	})
	result, err := client.RecommendedActors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actors := result.NewActors
	if len(actors) != 4 || actors[0].Gender == nil || *actors[0].Gender != 0 || actors[1].Gender == nil || *actors[1].Gender != 1 || actors[2].Gender != nil || actors[3].Gender != nil {
		t.Fatalf("gender presence was not preserved: %#v", actors)
	}
}
