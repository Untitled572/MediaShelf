package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestJavLibraryAccountConfigRedactsCookieAndPreservesPolicy(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	if w := req(t, a, http.MethodPut, "/api/v1/accounts/javlibrary", token, Object{
		"username": "tester", "cookie": "secret=value", "wanted_url": "https://www.javlibrary.com/cn/userwanted.php?list&u=123",
	}); w.Code != http.StatusOK {
		t.Fatalf("save config: %d %s", w.Code, w.Body.String())
	}
	got := req(t, a, http.MethodGet, "/api/v1/accounts/javlibrary", token, nil)
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "secret=value") {
		t.Fatalf("GET leaked cookie or failed: %d %s", got.Code, got.Body.String())
	}
	var response Object
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !flag(response["cookie_configured"]) || str(response["wanted_url"]) == "" {
		t.Fatalf("missing redacted config fields: %#v", response)
	}
	if w := req(t, a, http.MethodPut, "/api/v1/accounts/javlibrary", token, Object{"username": "tester2"}); w.Code != http.StatusOK {
		t.Fatalf("update config: %d %s", w.Code, w.Body.String())
	}
	cfg := a.javLibraryAccountConfig()
	if str(cfg["cookie"]) != "secret=value" || str(cfg["username"]) != "tester2" {
		t.Fatalf("blank cookie update did not preserve secret: %#v", cfg)
	}
	if w := req(t, a, http.MethodPut, "/api/v1/accounts/javlibrary", token, Object{"clear_cookie": true}); w.Code != http.StatusOK {
		t.Fatalf("clear cookie: %d %s", w.Code, w.Body.String())
	}
	if str(a.javLibraryAccountConfig()["cookie"]) != "" {
		t.Fatal("explicit clear_cookie did not clear cookie")
	}

	_ = a.put("actor_policies", Object{
		"id": "Canonical", "name": "Canonical", "aliases": []string{"Imported Alias"},
		"policy": "pause", "target": 7, "favorite": false,
	})
	changed, alreadyFavorite, err := a.addJavLibraryFavoriteActor("Imported Alias")
	if err != nil || !changed || alreadyFavorite {
		t.Fatalf("actor favorite merge: changed=%v existing=%v err=%v", changed, alreadyFavorite, err)
	}
	actor, err := a.get("actor_policies", "Canonical")
	if err != nil || !flag(actor["favorite"]) || str(actor["policy"]) != "pause" || num(actor["target"]) != 7 || !contains(actor["aliases"], "Imported Alias") {
		t.Fatalf("actor favorite import overwrote policy fields: %#v err=%v", actor, err)
	}
}

func TestJavLibraryAccountRejectsCrossOriginAndDoesNotClaimEmptySync(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	if w := req(t, a, http.MethodPut, "/api/v1/accounts/javlibrary", token, Object{
		"cookie": "secret=value", "wanted_url": "https://attacker.example/list",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("cross-origin URL accepted: %d %s", w.Code, w.Body.String())
	}
	if w := req(t, a, http.MethodPost, "/api/v1/accounts/javlibrary/sync", token, Object{"kinds": []string{"wanted"}}); w.Code != http.StatusOK {
		t.Fatalf("missing URL should return explicit sync error: %d %s", w.Code, w.Body.String())
	} else {
		var result Object
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if flag(result["complete"]) || str(a.javLibraryAccountConfig()["last_status"]) != "failed" || !strings.Contains(w.Body.String(), "未配置") {
			t.Fatalf("empty import was reported as success: %#v", result)
		}
	}
}

func TestMetadataOnlyCandidateCannotBeSubmittedToDownloader(t *testing.T) {
	a := testApp(t)
	token := login(t, a)
	if err := a.put("candidates", Object{
		"id": "javlibrary:example-001", "code": "EXAMPLE-001", "title": "Public metadata",
		"url": "", "source_url": "https://www.javlibrary.com/cn/?v=example", "metadata_only": true,
		"source": "javlibrary", "status": "pending",
	}); err != nil {
		t.Fatal(err)
	}
	w := req(t, a, http.MethodPost, "/api/v1/candidates/javlibrary:example-001/download", token, Object{"downloader_id": "missing"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "请先选择磁力或种子资源") {
		t.Fatalf("metadata-only candidate reached downloader path: %d %s", w.Code, w.Body.String())
	}
}
