package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type wikiRetryTransport func(*http.Request) (*http.Response, error)

func (f wikiRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type wikiTestTimeout struct{}

func (wikiTestTimeout) Error() string   { return "net/http: TLS handshake timeout" }
func (wikiTestTimeout) Timeout() bool   { return true }
func (wikiTestTimeout) Temporary() bool { return true }

func TestWikimediaRetriesTransientHandshakeTimeout(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: wikiRetryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, wikiTestTimeout{}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ready":true}`)), Request: r}, nil
	})}
	var value Object
	if err := getWikimediaJSON(context.Background(), c, "https://ja.wikipedia.org/w/api.php", url.Values{}, &value); err != nil || !flag(value["ready"]) || calls != 2 {
		t.Fatalf("handshake retry failed: calls=%d err=%v value=%#v", calls, err, value)
	}
}
func TestWikimediaRetriesRemainBoundedAndSkipPermanentErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		network bool
		want    int
	}{{"repeated timeout", 0, true, 2}, {"service unavailable", 503, false, 2}, {"access denied", 403, false, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &http.Client{Transport: wikiRetryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.network {
					return nil, wikiTestTimeout{}
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})}
			var value Object
			if err := getWikimediaJSON(context.Background(), c, "https://ja.wikipedia.org/w/api.php", url.Values{}, &value); err == nil || calls != tc.want {
				t.Fatalf("unexpected retry count %d want %d err=%v", calls, tc.want, err)
			}
		})
	}
}
func TestWikimediaCancelledRequestIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	c := &http.Client{Transport: wikiRetryTransport(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, context.Canceled })}
	var value Object
	err := getWikimediaJSON(ctx, c, "https://ja.wikipedia.org/w/api.php", url.Values{}, &value)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled request retried: %d %v", calls, err)
	}
}
