package integrations

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestCatalogExtraDefaultsAndOperations(t *testing.T) {
	for source, base := range map[string]string{"javbus": "https://www.javbus.com", "jav321": "https://www.jav321.com"} {
		c, err := NewCatalogClient(CatalogConfig{Source: source})
		if err != nil || c.base != base {
			t.Fatalf("%s default: base=%q err=%v", source, c.base, err)
		}
	}
	if _, err := (&CatalogClient{source: "jav321"}).Catalog(context.Background(), "/star/1", 1); err == nil || !strings.Contains(err.Error(), "code lookup only") {
		t.Fatalf("Jav321 should clearly reject actor catalog: %v", err)
	}
}

func TestCatalogJavBusDetailFixture(t *testing.T) {
	body := `<div class="container"><h3>IPX-177 Example title</h3></div><a class="bigImage"><img src="/cover.jpg"></a><div class="info"><p><span>發行日期:</span> 2024-03-01</p></div><span class="genre"><label><a href="/genre/1">Drama</a></label></span><a class="avatar-box"><img title="Actor One" src="/actor.jpg"></a>`
	c := &CatalogClient{source: "javbus", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/IPX-177" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	m, err := c.Lookup(context.Background(), "ipx-177")
	if err != nil {
		t.Fatal(err)
	}
	if m.Code != "IPX-177" || m.Title != "Example title" || m.Year != 2024 || m.CoverURL != "https://provider.example/cover.jpg" || len(m.Actors) != 1 || m.Actors[0] != "Actor One" || len(m.Tags) != 1 || m.Tags[0] != "Drama" {
		t.Fatalf("JavBus fixture fields not parsed: %#v", m)
	}
}

func TestCatalogJavBusVerificationIsExplicit(t *testing.T) {
	c := &CatalogClient{source: "javbus", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<html><title>Age Verification</title></html>`)), Request: r}, nil
	})}}
	if _, err := c.Lookup(context.Background(), "IPX-177"); err == nil || !strings.Contains(err.Error(), "verification") || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("JavBus verification page should stay explicit: %v", err)
	}
}

func TestCatalogJavBusActorSearchAndCatalogFixtures(t *testing.T) {
	client := &CatalogClient{source: "javbus", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<a href="/star/77">Example Actress</a><a class="movie-box" href="/IPX-177"><span class="photo-info">IPX-177 Sample work</span><img src="/cover.jpg"></a><a rel="next" href="?page=2">Next</a>`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	actor, err := client.SearchActor(context.Background(), "Example Actress")
	if err != nil || actor.Actor != "/star/77" {
		t.Fatalf("unexpected JavBus actor search: %#v err=%v", actor, err)
	}
	page, err := client.Catalog(context.Background(), actor.Actor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Works) != 1 || page.Works[0].Code != "IPX-177" || page.Works[0].ThumbnailURL != "https://provider.example/cover.jpg" || page.Works[0].CoverURL != "" || !page.HasMore || page.Complete {
		t.Fatalf("unexpected JavBus actor catalog: %#v", page)
	}
}

func TestActorCatalogImagesAreStoredAsThumbnails(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		parse func(*html.Node, string, *ActorPage)
	}{
		{"javdb", `<a class="box" href="/v/1"><div class="video-title"><strong>IPX-177</strong></div><img src="/list-small.jpg"></a>`, parseJavDBCatalog},
		{"javlibrary", `<div class="video"><a href="/v/1"><div class="id">IPX-177</div><img src="/list-small.jpg"></a></div>`, parseJavLibraryCatalog},
		{"fanza", `<a href="/detail/?cid=ipx177">IPX-177 Sample<img src="/list-small.jpg"></a>`, parseFanzaCatalog},
		{"javbus", `<a class="movie-box" href="/IPX-177"><span class="photo-info">IPX-177 Sample</span><img src="/list-small.jpg"></a>`, parseJavBusCatalog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := html.Parse(strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			page := ActorPage{}
			tc.parse(root, "https://provider.example/actor", &page)
			if len(page.Works) != 1 || page.Works[0].ThumbnailURL != "https://provider.example/list-small.jpg" || page.Works[0].CoverURL != "" {
				t.Fatalf("list image should be a thumbnail only: %#v", page.Works)
			}
		})
	}
}

func TestCatalogJav321POSTDetailFixtureAndFailures(t *testing.T) {
	body := `<ul class="dropdown-menu"><li><a href="/video/123">IPX-177</a></li></ul><div class="panel-heading"><h3>Example title</h3></div><div class="col-md-9"><b>品番</b>: ipx-177 <b>配信開始日</b>: 2024-03-01 <a href="/genre/1">Drama</a></div><div class="thumbnail"><a href="/star/1"><img alt="Actor One" src="/actor.jpg"></a></div><div class="panel"><img alt="Cover title" src="/banner.jpg"></div><div class="thumbnail"><a href="/video/other"><img alt="Other movie title"></a></div><div class="col-md-3"><img class="img-responsive" src="/cover.jpg"></div>`
	var called bool
	c := &CatalogClient{source: "jav321", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		form, _ := url.ParseQuery(mustReadBody(t, r))
		if form.Get("sn") != "IPX-177" {
			t.Fatalf("unexpected search form: %#v", form)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	m, err := c.Lookup(context.Background(), "ipx-177")
	if err != nil {
		t.Fatal(err)
	}
	if !called || m.Code != "IPX-177" || m.Title != "Example title" || m.Year != 2024 || m.CoverURL != "https://provider.example/cover.jpg" || len(m.Actors) != 1 || m.Actors[0] != "Actor One" || len(m.Tags) != 1 || m.Tags[0] != "Drama" {
		t.Fatalf("Jav321 fixture fields not parsed: %#v", m)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"not found", `<html><body>no result</body></html>`, "not found"},
		{"verification challenge", `<html><script src="/cdn-cgi/challenge-platform/x"></script></html>`, "browser verification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &CatalogClient{source: "jav321", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}}
			_, err := client.Lookup(context.Background(), "IPX-177")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func mustReadBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
