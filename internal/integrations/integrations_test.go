package integrations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/html"
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
		origin := "http://" + r.Host
		if r.Header.Get("Origin") != origin || r.Header.Get("Referer") != origin+"/" {
			t.Errorf("request origin headers do not match target origin")
		}
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

func TestQBittorrentLoginAcceptsEmptyBodyOnlyAfterAuthenticatedVersion(t *testing.T) {
	var authenticatedVersion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/api/v2/app/version":
			cookie, err := r.Cookie("SID")
			if err != nil || cookie.Value != "session" {
				http.Error(w, "login cookie missing", http.StatusUnauthorized)
				return
			}
			authenticatedVersion = true
			_, _ = io.WriteString(w, "5.3.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "acceptance-user", Password: "acceptance-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(context.Background()); err != nil {
		t.Fatalf("login with empty HTTP 200 body and valid session cookie failed: %v", err)
	}
	if !authenticatedVersion || q.version != "5.3.0" {
		t.Fatalf("authenticated version validation = %v, version %q", authenticatedVersion, q.version)
	}
}

func TestQBittorrentLoginRejectsInvalidNonemptyBody(t *testing.T) {
	var versionRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Fails.")
		case "/api/v2/app/version":
			versionRequests++
			_, _ = io.WriteString(w, "5.3.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "acceptance-user", Password: "acceptance-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "Fails.") {
		t.Fatalf("invalid login response error = %v, want rejection", err)
	}
	if versionRequests != 0 {
		t.Fatal("authenticated version check must not follow an explicit login failure")
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

func TestQBittorrentAllowsUnauthenticatedVersionEndpoint(t *testing.T) {
	var listed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "5.2.4.10")
		case "/api/v2/torrents/info":
			listed = true
			_, _ = io.WriteString(w, "[]")
		case "/api/v2/auth/login":
			t.Error("unauthenticated endpoint should not be forced through login")
			http.Error(w, "Fails.", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q, err := NewQBittorrent(QBConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = q.ListTorrents(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !listed {
		t.Fatal("torrent list endpoint was not called")
	}
}

func TestQBittorrentAnonymousProbeRequiresCredentialsOnFailure(t *testing.T) {
	var loginRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			http.Error(w, "authentication required", http.StatusUnauthorized)
		case "/api/v2/auth/login":
			loginRequests.Add(1)
			t.Error("anonymous probe failure must not submit an empty login")
			http.Error(w, "Fails.", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q, err := NewQBittorrent(QBConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = q.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("Login error = %v, want credentials-required error", err)
	}
	if loginRequests.Load() != 0 {
		t.Fatalf("empty login was submitted %d times", loginRequests.Load())
	}
}

func TestQBittorrentFailedCredentialsCoolDownAndChangedCredentialsRetry(t *testing.T) {
	var loginRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			loginRequests.Add(1)
			http.Error(w, "Fails.", http.StatusUnauthorized)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	defer srv.Close()

	q, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "account-a", Password: "secret-a"})
	if err != nil {
		t.Fatal(err)
	}
	first := q.Login(context.Background())
	if first == nil || !strings.Contains(first.Error(), "401") {
		t.Fatalf("first Login error = %v, want authentication failure", first)
	}
	second := q.Login(context.Background())
	if second == nil || !strings.Contains(second.Error(), "temporarily suppressed") {
		t.Fatalf("second Login error = %v, want cooldown", second)
	}
	if strings.Contains(first.Error(), "secret-a") || strings.Contains(second.Error(), "secret-a") {
		t.Fatal("Login error exposed a password")
	}
	if got := loginRequests.Load(); got != 1 {
		t.Fatalf("same credentials triggered %d auth requests, want 1", got)
	}

	changed, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "account-a", Password: "secret-b"})
	if err != nil {
		t.Fatal(err)
	}
	if err = changed.Login(context.Background()); err == nil {
		t.Fatal("changed invalid credentials unexpectedly authenticated")
	}
	if got := loginRequests.Load(); got != 2 {
		t.Fatalf("changed credentials triggered %d auth requests total, want 2", got)
	}
}

func TestQBittorrentTransientLoginFailuresRetryImmediately(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(http.ResponseWriter, *http.Request)
	}{
		{name: "network error", fail: func(w http.ResponseWriter, r *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support connection hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack login connection: %v", err)
			}
			_ = conn.Close()
		}},
		{name: "version HTTP 500", fail: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var loginRequests atomic.Int32
			var versionRequests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					if loginRequests.Add(1) == 1 && tc.name == "network error" {
						tc.fail(w, r)
						return
					}
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/app/version":
					if versionRequests.Add(1) == 1 && tc.name == "version HTTP 500" {
						tc.fail(w, r)
						return
					}
					_, _ = io.WriteString(w, "5.3.0")
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			q, err := NewQBittorrent(QBConfig{URL: srv.URL, Username: "transient-user", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Login(context.Background()); err == nil {
				t.Fatal("first Login unexpectedly succeeded")
			}
			if err := q.Login(context.Background()); err != nil {
				t.Fatalf("retry after transient failure was suppressed or failed: %v", err)
			}
			if got := loginRequests.Load(); got != 2 {
				t.Fatalf("auth/login requests = %d, want 2", got)
			}
			if tc.name == "version HTTP 500" && versionRequests.Load() != 2 {
				t.Fatalf("version requests = %d, want 2", versionRequests.Load())
			}
		})
	}
}

func TestQBittorrentListUsesCategoryQueryAndKeepsRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("category") != "vr" || r.URL.Query().Get("sort") != "hash" || r.URL.Query().Get("reverse") != "false" {
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

func TestQBittorrentListFilteredPaginatesAndMatchesExactTag(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v2/torrents/info" || q.Get("category") != "vr" || q.Get("tag") != "action" {
			http.Error(w, "missing category or tag filter", http.StatusBadRequest)
			return
		}
		offset := q.Get("offset")
		offsets = append(offsets, offset)
		switch offset {
		case "0":
			var b strings.Builder
			b.WriteByte('[')
			for i := 0; i < 250; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				tags := "other"
				if i == 2 {
					tags = " other, action , comedy "
				}
				if i == 3 {
					tags = "actionable"
				}
				fmt.Fprintf(&b, `{"hash":"first-%d","tags":%q}`, i, tags)
			}
			b.WriteByte(']')
			_, _ = io.WriteString(w, b.String())
		case "250":
			_, _ = io.WriteString(w, `[{"hash":"second","tags":"action"}]`)
		default:
			http.Error(w, "unexpected offset", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	client, err := NewQBittorrent(QBConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListTorrentsFiltered(context.Background(), "vr", " action ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(offsets, ",") != "0,250" {
		t.Fatalf("page offsets = %v, want [0 250]", offsets)
	}
	if len(got) != 2 || got[0].Hash != "first-2" || got[1].Hash != "second" {
		t.Fatalf("exact tag filtering returned %#v", got)
	}
}

func TestQBittorrentCreateTagsPostsTagsAndReturnsErrors(t *testing.T) {
	var gotForm url.Values
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/torrents/createTags" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = r.ParseForm()
		gotForm = r.Form
		w.WriteHeader(status)
	}))
	defer srv.Close()
	client, err := NewQBittorrent(QBConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.CreateTags(context.Background(), "action, comedy"); err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("tags") != "action, comedy" {
		t.Fatalf("posted tags = %q", gotForm.Get("tags"))
	}
	status = http.StatusInternalServerError
	if err = client.CreateTags(context.Background(), "broken"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("CreateTags error = %v, want HTTP 500", err)
	}
}

func TestQBittorrentCategoriesAndAutoTMMAdd(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/categories":
			_, _ = io.WriteString(w, `{"vr":{"name":"vr","savePath":"D:/VR"},"":{"name":"","savePath":"D:/Downloads"}}`)
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			gotForm = r.Form
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(QBConfig{URL: srv.URL})
	categories, err := q.ListCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if categories["vr"].SavePath != "D:/VR" || categories[""].SavePath != "D:/Downloads" {
		t.Fatalf("unexpected category map: %#v", categories)
	}
	auto := true
	if err = q.Add(context.Background(), AddTorrent{URL: "magnet:?xt=urn:btih:abc", SavePath: "D:/manual", Category: "vr", AutoTMM: &auto}); err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("autoTMM") != "true" || gotForm.Has("savepath") || gotForm.Get("category") != "vr" {
		t.Fatalf("auto-managed add should omit savepath: %#v", gotForm)
	}
	noAuto := false
	if err = q.Add(context.Background(), AddTorrent{URL: "magnet:?xt=urn:btih:def", SavePath: "D:/manual", AutoTMM: &noAuto}); err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("autoTMM") != "false" || gotForm.Get("savepath") != "D:/manual" {
		t.Fatalf("explicit manual add fields missing: %#v", gotForm)
	}
	if err = q.Add(context.Background(), AddTorrent{URL: "magnet:?xt=urn:btih:ghi", SavePath: "D:/legacy"}); err != nil {
		t.Fatal(err)
	}
	if gotForm.Has("autoTMM") || gotForm.Get("savepath") != "D:/legacy" {
		t.Fatalf("unset auto-management should preserve legacy behavior: %#v", gotForm)
	}
}

func TestQBittorrentAddValidatesBusinessResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{{"ok", "Ok.", false}, {"empty compatibility response", "", false}, {"business failure", "Fails.", true}, {"5.3 success", `{"success_count":1,"pending_count":0,"failure_count":0}`, false}, {"5.3 pending", `{"success_count":0,"pending_count":1,"failure_count":0}`, false}, {"5.3 failure", `{"success_count":0,"pending_count":0,"failure_count":1}`, true}, {"unrecognized json", `{}`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v2/torrents/add" {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			q, err := NewQBittorrent(QBConfig{URL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			err = q.Add(context.Background(), AddTorrent{URL: "magnet:?xt=urn:btih:abc"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Add error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestQBittorrentListPaginatesAt250(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" || r.URL.Query().Get("limit") != "250" {
			http.Error(w, "bad pagination query", http.StatusBadRequest)
			return
		}
		requests++
		wantOffset := "0"
		count := 250
		if requests == 2 {
			wantOffset = "250"
			count = 1
		}
		if requests > 2 || r.URL.Query().Get("offset") != wantOffset {
			http.Error(w, "unexpected offset", http.StatusBadRequest)
			return
		}
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < count; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"hash":"hash-%d","name":"torrent-%d"}`, i+requests*250, i+requests*250)
		}
		b.WriteByte(']')
		_, _ = io.WriteString(w, b.String())
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(QBConfig{URL: srv.URL})
	got, err := q.ListTorrents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(got) != 251 {
		t.Fatalf("expected two pages with 251 torrents, got requests=%d entries=%d", requests, len(got))
	}
}

func TestQBittorrentReducesOversizedPageAndRetriesSameOffset(t *testing.T) {
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q)
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if limit == 250 {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `[`+strings.Repeat(`{"hash":"oversize"},`, 220000)+`{"hash":"oversize"}]`)
			return
		}
		count := limit
		if offset == 125 {
			count = 1
		}
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < count; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"hash":"%d"}`, offset+i)
		}
		b.WriteByte(']')
		_, _ = io.WriteString(w, b.String())
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(QBConfig{URL: srv.URL})
	got, err := q.ListTorrents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 126 || len(queries) != 3 {
		t.Fatalf("expected 126 items from three requests, got items=%d requests=%d", len(got), len(queries))
	}
	if queries[0].Get("offset") != "0" || queries[1].Get("offset") != "0" || queries[0].Get("limit") != "250" || queries[1].Get("limit") != "125" || queries[2].Get("offset") != "125" {
		t.Fatalf("oversized response was not retried at the same offset: %#v", queries)
	}
}

func TestQBittorrentLiveReadOnly(t *testing.T) {
	base := os.Getenv("MEDIASHELF_LIVE_QBIT_URL")
	if base == "" {
		t.Skip("set MEDIASHELF_LIVE_QBIT_URL to run the read-only local qBittorrent smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	q, err := NewQBittorrent(QBConfig{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := q.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	torrents, err := q.ListTorrents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read-only qBittorrent probe succeeded: version=%s torrents=%d", version, len(torrents))
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

func TestProviderRatingNormalizationPreservesSourceScale(t *testing.T) {
	javdb, err := html.Parse(strings.NewReader(`<h2><strong class="current-title">IPX-177 Sample</strong></h2><span class="score-stars"></span>4.25分 (1,234)<strong>演員:</strong><span><a>Actor Name</a><strong>♀</strong></span>`))
	if err != nil {
		t.Fatal(err)
	}
	a := parseDetail(javdb, "IPX-177", "javdb", "https://javdb.example/movie")
	if a.Rating == nil || *a.Rating != 4.25 || a.SourceRating == nil || *a.SourceRating != 4.25 || a.RatingScale != "0-5" || a.RatingCount != 1234 || a.RatingRaw != "4.25分 (1,234)" {
		t.Fatalf("JavDB rating was not preserved/normalized: %#v", a)
	}
	if len(a.Actors) != 1 || a.Actors[0] != "Actor Name" {
		t.Fatalf("JavDB actor links were not parsed: %#v", a.Actors)
	}
	javlib, err := html.Parse(strings.NewReader(`<span class="score">(8.60)</span>`))
	if err != nil {
		t.Fatal(err)
	}
	b := parseDetail(javlib, "ABC-123", "javlibrary", "https://javlibrary.example/movie")
	if b.Rating == nil || *b.Rating != 4.3 || b.SourceRating == nil || *b.SourceRating != 8.6 || b.RatingScale != "0-10" || b.RatingCount != 0 {
		t.Fatalf("JavLibrary rating scale was inconsistent: %#v", b)
	}
	fanza, err := html.Parse(strings.NewReader(`<table><tr><td>平均評価：</td><td><img src="/img/40.gif"/></td></tr></table>`))
	if err != nil {
		t.Fatal(err)
	}
	c := parseDetail(fanza, "IPX00177", "fanza", "https://fanza.example/movie")
	if c.Rating == nil || *c.Rating != 4 || c.SourceRating == nil || *c.SourceRating != 40 || c.RatingScale != "0-50" || c.RatingRaw != "40" {
		t.Fatalf("FANZA rating scale was inconsistent: %#v", c)
	}
}

func TestProviderRedirectDoesNotForwardCookiesAcrossOrigins(t *testing.T) {
	from, _ := url.Parse("https://provider.example/path")
	to, _ := url.Parse("https://other.example/path")
	req, _ := http.NewRequest(http.MethodGet, to.String(), nil)
	req.Header.Set("Cookie", "session=secret")
	if err := providerRedirectPolicy(req, []*http.Request{{URL: from}}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Cookie") != "" {
		t.Fatal("provider cookie crossed an origin boundary")
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
		if tc.more && p.NextURL != "https://provider.example/actors/77?page=3" {
			t.Fatalf("next page URL was not resolved: %q", p.NextURL)
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

func TestCatalogFetchDistinguishesBrowserChallengeFromCloudflareCDN(t *testing.T) {
	newClient := func(body string) *CatalogClient {
		return &CatalogClient{
			source: "javdb",
			base:   "https://provider.example",
			client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    r,
				}, nil
			})},
		}
	}

	challenge := `<html><head><script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1"></script></head><body><div id="cf-challenge-running"></div></body></html>`
	if _, _, err := newClient(challenge).fetch(context.Background(), "https://provider.example/search?q=ABF-243"); err == nil || !strings.Contains(err.Error(), "browser verification") || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("HTTP 200 challenge was not reported as restricted: %v", err)
	}

	normal := `<html><head><script src="https://cdnjs.cloudflare.com/ajax/libs/example/1.0/app.js"></script><script src="https://static.cloudflareinsights.com/beacon.min.js"></script></head><body><a class="box" href="/movie/1">ABF-243</a></body></html>`
	if _, _, err := newClient(normal).fetch(context.Background(), "https://provider.example/search?q=ABF-243"); err != nil {
		t.Fatalf("ordinary Cloudflare CDN references were mistaken for a challenge: %v", err)
	}
}

func TestQBittorrentVersionPrefix(t *testing.T) {
	for version, want := range map[string]int{"v5.3.0": 503, "v5.2.4.10": 502, "4.6.7": 406, "v4.6.7": 406} {
		q := &QBittorrent{version: version}
		got, err := q.apiVersion(context.Background())
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", version, got, err)
		}
	}
}
