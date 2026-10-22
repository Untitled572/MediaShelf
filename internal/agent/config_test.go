package agent

import (
	"path/filepath"
	"testing"
)

func TestConfigRequiresAbsoluteLocalAllowlist(t *testing.T) {
	tmp := t.TempDir()
	c := Config{ServerURL: "http://127.0.0.1:8765", Token: "secret", Libraries: map[string]string{"lib": tmp}, Tools: map[string]Tool{"player": {Path: filepath.Join(tmp, "player.exe")}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid local config rejected: %v", err)
	}
	if _, ok := c.LibraryRoot("server-added"); ok {
		t.Fatal("server-only library must not be locally allowlisted")
	}
	if _, ok := c.LocalTool("server-added"); ok {
		t.Fatal("server-only tool must not be locally allowlisted")
	}
	c.Libraries["bad"] = "relative/path"
	if err := c.Validate(); err == nil {
		t.Fatal("relative library root accepted")
	}
}

func TestConfigRejectsNonHTTPServerAndUnsafeScriptPath(t *testing.T) {
	tmp := t.TempDir()
	c := Config{ServerURL: "file:///etc/passwd", Token: "x", Libraries: map[string]string{"lib": tmp}}
	if err := c.Validate(); err == nil {
		t.Fatal("non-http server URL accepted")
	}
	c.ServerURL = "https://example.test"
	c.Tools = map[string]Tool{"script": {Path: filepath.Join(tmp, "unsafe%PATH%.cmd")}}
	if err := c.Validate(); err == nil {
		t.Fatal("command expansion characters accepted in tool path")
	}
}

func TestDownloaderURLAllowlistCannotBeExpandedByJob(t *testing.T) {
	c := Config{ServerURL: "http://127.0.0.1:8765", Token: "secret", Libraries: map[string]string{"lib": t.TempDir()}, Downloaders: map[string]Downloader{"qb": {URL: "http://127.0.0.1:8080/qb"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(c, nil)
	p := qbitPayload{DownloaderID: "qb", Operation: "list"}
	p.Config.URL = "http://127.0.0.1:8081/qb"
	if _, err := r.runQBit(nil, p); err == nil {
		t.Fatal("job reached a URL outside the local allowlist")
	}
	p.DownloaderID = "server-added"
	if _, err := r.runQBit(nil, p); err == nil {
		t.Fatal("server-only downloader ID was accepted")
	}
	if sameHTTPURL(c.Downloaders["qb"].URL, "http://127.0.0.1:8080/other") {
		t.Fatal("different URL path matched allowlist")
	}
	if sameHTTPURL(c.Downloaders["qb"].URL, "http://user:pass@127.0.0.1:8080/qb") {
		t.Fatal("URL credentials matched allowlist")
	}
}
