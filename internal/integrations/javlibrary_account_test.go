package integrations

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type javLibraryAccountRoundTripFunc func(*http.Request) (*http.Response, error)

func (f javLibraryAccountRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func javLibraryAccountResponse(req *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func javLibraryAccountTestClient(t *testing.T, cookie string, roundTrip javLibraryAccountRoundTripFunc) *JavLibraryAccountClient {
	t.Helper()
	client, err := NewJavLibraryAccountClient(CatalogConfig{Source: "javlibrary", BaseURL: "https://www.javlibrary.example", Cookie: cookie})
	if err != nil {
		t.Fatal(err)
	}
	client.client = &http.Client{Transport: roundTrip}
	return client
}

func TestJavLibraryAccountImportMoviesFollowsSameOriginNextAndDeduplicates(t *testing.T) {
	var paths []string
	client := javLibraryAccountTestClient(t, "session=secret", func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.RequestURI())
		if got := req.Header.Get("Cookie"); got != "session=secret" {
			t.Fatalf("cookie was not sent to configured origin: %q", got)
		}
		if len(paths) == 1 {
			return javLibraryAccountResponse(req, 200, `<div class="video"><a href="/cn/vldvd.php?v=1"><div class="id">ABC-123</div><div class="title">First</div></a></div><a rel="next" href="?page=2">Next</a>`)
		}
		return javLibraryAccountResponse(req, 200, `<div class="video"><a href="/cn/vldvd.php?v=1"><div class="id">ABC-123</div><div class="title">Duplicate</div></a></div><div class="video"><a href="/cn/vldvd.php?v=2"><div class="id">DEF-456</div><div class="title">Second</div></a></div>`)
	})

	got, err := client.ImportMovies(context.Background(), "https://www.javlibrary.example/cn/userwanted.php?list&u=provided", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || len(got) != 2 || got[0].Code != "ABC-123" || got[1].Code != "DEF-456" {
		t.Fatalf("unexpected imported entries: paths=%v got=%#v", paths, got)
	}
}

func TestJavLibraryAccountImportFavoriteActorsUsesObservedActorMarkup(t *testing.T) {
	client := javLibraryAccountTestClient(t, "", func(req *http.Request) (*http.Response, error) {
		return javLibraryAccountResponse(req, 200, `<div class="star"><a href="/cn/vl_star.php?s=1">Actor A</a></div><div id="performer"><a href="/cn/vl_star.php?s=2">Actor B</a></div>`)
	})
	got, err := client.ImportFavoriteActors(context.Background(), "https://www.javlibrary.example/cn/user_favorites.php?u=provided", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Actor A" || got[1] != "Actor B" {
		t.Fatalf("unexpected actor list: %#v", got)
	}
}

func TestJavLibraryAccountRejectsUnconfiguredOriginAndEmptyResults(t *testing.T) {
	client := javLibraryAccountTestClient(t, "", func(req *http.Request) (*http.Response, error) {
		return javLibraryAccountResponse(req, 200, `<html><body>nothing recognizable</body></html>`)
	})
	if _, err := client.ImportMovies(context.Background(), "https://elsewhere.example/cn/userwanted.php?list&u=x", 1); err == nil {
		t.Fatal("expected a list URL outside the configured origin to be rejected")
	}
	if _, err := client.ImportMovies(context.Background(), "https://www.javlibrary.example/cn/userwanted.php?list&u=x", 1); err == nil || !strings.Contains(err.Error(), "no recognizable") {
		t.Fatalf("expected an empty parse to fail explicitly, got %v", err)
	}
}

func TestJavLibraryAccountReportsExpiredOrBlockedSession(t *testing.T) {
	client := javLibraryAccountTestClient(t, "", func(req *http.Request) (*http.Response, error) {
		return javLibraryAccountResponse(req, http.StatusForbidden, `blocked`)
	})
	if _, err := client.ImportMovies(context.Background(), "https://www.javlibrary.example/cn/userwanted.php?list&u=x", 1); err == nil || !strings.Contains(err.Error(), "session may be invalid") {
		t.Fatalf("expected explicit denied/session error, got %v", err)
	}
}
