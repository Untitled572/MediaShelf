package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMainSettingsDefaultMergeAndGlobalProxyPrecedence(t *testing.T) {
	a := testApp(t)
	defaults, err := a.mainSettings()
	if err != nil {
		t.Fatal(err)
	}
	if defaults["proxy_enabled"] != true || str(defaults["proxy_url"]) != "http://127.0.0.1:7890" {
		t.Fatalf("proxy defaults = %#v", defaults)
	}
	_ = a.put("sources", Object{"id": "javdbui", "proxy_url": "http://127.0.0.1:7891"})
	updated, err := a.patchMainSettings(Object{"ui_density": "compact", "proxy_url": "http://127.0.0.1:7892"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.outboundProxy(); err != nil || got != "http://127.0.0.1:7892" || updated["ui_density"] != "compact" || updated["proxy_enabled"] != true {
		t.Fatalf("global proxy did not override legacy source setting or merge: proxy=%q settings=%#v err=%v", got, updated, err)
	}
	updated, err = a.patchMainSettings(Object{"proxy_enabled": false, "proxy_url": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.outboundProxy(); err != nil || got != "" || updated["ui_density"] != "compact" {
		t.Fatalf("disabled global proxy did not stay direct or preserve other settings: proxy=%q settings=%#v err=%v", got, updated, err)
	}
}

func TestSettingsAPIExposesDefaultsAndMergesPartialPatch(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	get := req(t, a, http.MethodGet, "/api/v1/settings", token, nil)
	var settings Object
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &settings) != nil || settings["proxy_enabled"] != true || str(settings["proxy_url"]) != "http://127.0.0.1:7890" {
		t.Fatalf("settings GET did not expose defaults: %d %s", get.Code, get.Body.String())
	}
	patch := req(t, a, http.MethodPatch, "/api/v1/settings", token, Object{"custom_setting": "keep", "proxy_url": "http://127.0.0.1:7891"})
	if patch.Code != http.StatusOK || json.Unmarshal(patch.Body.Bytes(), &settings) != nil || settings["proxy_enabled"] != true || settings["custom_setting"] != "keep" {
		t.Fatalf("partial settings PATCH lost defaults or fields: %d %s", patch.Code, patch.Body.String())
	}
	patch = req(t, a, http.MethodPatch, "/api/v1/settings", token, Object{"proxy_enabled": false, "proxy_url": ""})
	if patch.Code != http.StatusOK || json.Unmarshal(patch.Body.Bytes(), &settings) != nil || settings["custom_setting"] != "keep" || settings["proxy_enabled"] != false {
		t.Fatalf("settings PATCH failed to merge disable: %d %s", patch.Code, patch.Body.String())
	}
}

func TestMainSettingsRejectsMalformedProxyWithoutChangingSavedSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body Object
	}{
		{"enabled-empty", Object{"proxy_enabled": true, "proxy_url": ""}},
		{"relative", Object{"proxy_url": "127.0.0.1:7890"}},
		{"ftp", Object{"proxy_url": "ftp://proxy.example:21"}},
		{"query", Object{"proxy_url": "http://127.0.0.1:7890?token=x"}},
		{"fragment", Object{"proxy_url": "http://127.0.0.1:7890#x"}},
		{"credentials", Object{"proxy_url": "http://user:pass@127.0.0.1:7890"}},
		{"bad-port", Object{"proxy_url": "http://127.0.0.1:99999"}},
		{"wrong-enabled-type", Object{"proxy_enabled": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			if _, err := a.patchMainSettings(tc.body); err == nil {
				t.Fatalf("invalid settings accepted: %#v", tc.body)
			}
			got, err := a.mainSettings()
			if err != nil || got["proxy_enabled"] != true || str(got["proxy_url"]) != "http://127.0.0.1:7890" {
				t.Fatalf("invalid update changed defaults: %#v err=%v", got, err)
			}
		})
	}
}

func TestSettingsProxyTestUsesFixedEndpointThroughConfiguredProxy(t *testing.T) {
	var seen atomic.Int32
	var badResponse atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if !r.URL.IsAbs() || !strings.EqualFold(r.URL.Host, "www.wikidata.org") || r.URL.Path != "/w/api.php" {
			http.Error(w, "wrong destination", http.StatusBadRequest)
			return
		}
		if badResponse.Load() {
			_, _ = w.Write([]byte(`{"error":{"info":"test failure"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"query":{"general":{"sitename":"Wikidata"}}}`))
	}))
	defer proxy.Close()
	a := testApp(t)
	a.proxyTestEndpoint = "http://www.wikidata.org/w/api.php?action=query&meta=siteinfo&format=json"
	if _, err := a.patchMainSettings(Object{"proxy_url": proxy.URL}); err != nil {
		t.Fatal(err)
	}
	token := login(t, a)
	result := req(t, a, http.MethodPost, "/api/v1/settings/proxy/test", token, nil)
	if result.Code != http.StatusOK {
		t.Fatalf("proxy test failed: %d %s", result.Code, result.Body.String())
	}
	var response Object
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil || response["ok"] != true || str(response["endpoint"]) != "Wikidata" || str(response["checked_at"]) == "" || seen.Load() != 1 {
		t.Fatalf("unexpected proxy test response %#v, requests=%d err=%v", response, seen.Load(), err)
	}
	badResponse.Store(true)
	failed := req(t, a, http.MethodPost, "/api/v1/settings/proxy/test", token, nil)
	if failed.Code != http.StatusBadGateway || !strings.Contains(failed.Body.String(), "Wikidata did not return") {
		t.Fatalf("invalid API response was not reported clearly: %d %s", failed.Code, failed.Body.String())
	}
}

func TestQBitLocalConnectionIgnoresGlobalPublicProxy(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		http.Error(w, "must not proxy local downloader", http.StatusBadGateway)
	}))
	defer proxy.Close()
	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.3.0"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbit.Close()
	a := testApp(t)
	if _, err := a.patchMainSettings(Object{"proxy_url": proxy.URL}); err != nil {
		t.Fatal(err)
	}
	result, err := a.qbit(context.Background(), Object{"url": qbit.URL, "username": "u", "password": "p"}, "torrents", Object{})
	if err != nil || result == nil || proxyRequests.Load() != 0 {
		t.Fatalf("qBittorrent was routed through global public proxy: result=%#v err=%v proxy_requests=%d", result, err, proxyRequests.Load())
	}
}
