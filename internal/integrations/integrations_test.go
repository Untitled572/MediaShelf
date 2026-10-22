package integrations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseFeedAtomRulesAndCode(t *testing.T) {
	xml := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>New IPX-177 release</title><link href="https://feed.example/item/1"/><published>2026-10-21T10:00:00Z</published></entry><entry><title>ABP-123 trailer</title><link href="https://feed.example/item/2"/></entry><entry><title>FC2-PPV-123456</title><link href="https://feed.example/item/3"/></entry></feed>`
	got, err := ParseFeed([]byte(xml), FeedRules{Include: []string{`(?i)IPX|FC2`}, Exclude: []string{`trailer`}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Code != "IPX-177" || got[1].Code != "FC2-PPV-123456" {
		t.Fatalf("unexpected feed results: %#v", got)
	}
	if got[0].PublishedAt.IsZero() {
		t.Fatal("Atom published timestamp was not parsed")
	}
}

func TestParseFeedRSS(t *testing.T) {
	xml := `<rss version="2.0"><channel><item><title>ABC_123</title><link>https://feed.example/ABC_123</link><pubDate>Wed, 21 Oct 2026 10:00:00 GMT</pubDate></item></channel></rss>`
	got, err := ParseFeed([]byte(xml), FeedRules{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != "ABC-123" || got[0].Source != "feed.example" {
		t.Fatalf("unexpected RSS result: %#v", got)
	}
}

func TestQBittorrentLoginVersionAndVersionAwareStart(t *testing.T) {
	var action string
	var cookieSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, err := r.Cookie("SID")
			cookieSeen = err == nil
			_, _ = io.WriteString(w, "5.0.0")
		case "/api/v2/torrents/start", "/api/v2/torrents/resume":
			action = r.URL.Path
			_, _ = io.WriteString(w, "Ok.")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cookieSeen {
		t.Fatal("login response cookie was not retained")
	}
	if err = q.SetRunning(context.Background(), []string{"0123456789abcdef"}, true); err != nil {
		t.Fatal(err)
	}
	if action != "/api/v2/torrents/start" {
		t.Fatalf("version 5 should use start endpoint, got %q", action)
	}
}

func TestQBittorrentUsesLegacyPauseResume(t *testing.T) {
	var action string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/app/version" {
			_, _ = io.WriteString(w, "4.5.2")
			return
		}
		action = r.URL.Path
		_, _ = io.WriteString(w, "Ok.")
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(QBConfig{URL: srv.URL})
	if err := q.SetRunning(context.Background(), []string{"0123456789abcdef"}, false); err != nil {
		t.Fatal(err)
	}
	if action != "/api/v2/torrents/pause" {
		t.Fatalf("legacy qBittorrent should use pause endpoint, got %q", action)
	}
}

func TestQBittorrentListUsesCategoryQueryAndKeepsRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("category") != "vr" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `[{"hash":"abc","name":"sample","dlspeed":42,"upspeed":7}]`)
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(QBConfig{URL: srv.URL})
	got, err := q.ListTorrents(context.Background(), "vr")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DLSpeed != 42 || got[0].UPSpeed != 7 {
		t.Fatalf("rates were not decoded: %#v", got)
	}
}

func TestCatalogActorSearchJavDBFixture(t *testing.T) {
	c := &CatalogClient{source: "javdb", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<div class="box actor-box"><a href="/actors/77"><strong>Example Actress</strong></a></div>`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	p, err := c.SearchActor(context.Background(), "Example Actress")
	if err != nil {
		t.Fatal(err)
	}
	if p.Actor != "/actors/77" {
		t.Fatalf("unexpected actor URL: %#v", p)
	}
}

func TestCatalogJavLibraryActorPageMarkup(t *testing.T) {
	body := `<div class="video" id="v1"><a href="/vldvdinfo.php?dvd=1"><div class="id">ABC-123</div><div class="title">Sample title</div><img src="/cover.jpg"></a></div><a rel="next" href="?page=2">Next</a>`
	c := &CatalogClient{source: "javlibrary", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	p, err := c.Catalog(context.Background(), "/cn/vl_star.php?s=88", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Works) != 1 || p.Works[0].Code != "ABC-123" || !p.HasMore || p.Complete {
		t.Fatalf("unexpected JavLibrary actor page: %#v", p)
	}
}

func TestCatalogFanzaActorPageMarkup(t *testing.T) {
	body := `<a href="/digital/videoa/-/detail/=/cid=sample123/"><span>SSNI-456 Sample scene</span><img src="/cover.jpg"></a><a class="next" href="?page=2">Next</a>`
	c := &CatalogClient{source: "fanza", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	p, err := c.Catalog(context.Background(), "/digital/videoa/actress/id=9", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Works) != 1 || p.Works[0].Code != "SSNI-456" || !p.HasMore || p.Complete {
		t.Fatalf("unexpected FANZA actor page: %#v", p)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogJavDBActorPaginationCompleteness(t *testing.T) {
	for _, tc := range []struct {
		body, code     string
		more, complete bool
	}{{`<a class="box" href="/v/IPX-177"><div class="video-title"><strong>IPX-177</strong></div></a><a rel="next" href="?page=3">next</a>`, "IPX-177", true, false}, {`<a class="box" href="/v/ABC-123"><div class="video-title"><strong>ABC-123</strong></div></a>`, "ABC-123", false, true}} {
		c := &CatalogClient{source: "javdb", base: "https://provider.example", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "provider.example" {
				return nil, fmt.Errorf("unexpected host")
			}
			resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}
			return resp, nil
		})}}
		p, err := c.Catalog(context.Background(), "/actors/77", 2)
		if err != nil {
			t.Fatal(err)
		}
		if p.HasMore != tc.more || p.Complete != tc.complete {
			t.Fatalf("pagination flags mismatch: %#v", p)
		}
		if len(p.Works) != 1 || p.Works[0].Code != tc.code {
			t.Fatalf("unexpected catalog work: %#v", p.Works)
		}
	}
}

func TestProviderSSRFRejectsLocalAddresses(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/x", "http://localhost/x", "https://192.168.1.2/x", "file:///etc/passwd"} {
		if err := validateExternalURL(parseURL(raw)); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
	if _, err := FetchFeed(context.Background(), "http://127.0.0.1/feed.xml", FeedRules{}); err == nil {
		t.Fatal("feed fetch accepted a loopback URL")
	}
}

func parseURL(s string) *url.URL { u, _ := url.Parse(s); return u }
