package server

import (
	"context"
	"mediashelf/internal/integrations"
	"net/http"
	"strings"
	"time"
)

const javLibraryAccountStore = "external_accounts"

func (a *App) javLibraryAccountAPI(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) < 2 || p[1] != "javlibrary" {
		fail(w, http.StatusNotFound, "账号来源不存在")
		return
	}
	if len(p) == 2 {
		switch r.Method {
		case http.MethodGet:
			a.getJavLibraryAccount(w)
		case http.MethodPut:
			a.putJavLibraryAccount(w, r)
		default:
			fail(w, http.StatusMethodNotAllowed, "GET 或 PUT required")
		}
		return
	}
	if len(p) == 3 && p[2] == "sync" && r.Method == http.MethodPost {
		a.syncJavLibraryAccount(w, r)
		return
	}
	fail(w, http.StatusNotFound, "接口不存在")
}

func (a *App) javLibraryAccountConfig() Object {
	stored, _ := a.get(javLibraryAccountStore, "javlibrary")
	if stored == nil {
		stored = Object{"id": "javlibrary", "username": "", "wanted_url": "", "favorites_url": "", "actors_url": ""}
	}
	return stored
}

func (a *App) javLibraryAccountResponse() Object {
	cfg := a.javLibraryAccountConfig()
	counts, _ := cfg["last_counts"].(map[string]any)
	if counts == nil {
		counts = Object{"wanted": 0, "favorites": 0, "actors": 0}
	}
	return Object{
		"username":          str(cfg["username"]),
		"wanted_url":        str(cfg["wanted_url"]),
		"favorites_url":     str(cfg["favorites_url"]),
		"actors_url":        str(cfg["actors_url"]),
		"cookie_configured": str(cfg["cookie"]) != "",
		"last_sync":         str(cfg["last_sync"]),
		"last_status":       str(cfg["last_status"]),
		"counts":            counts,
	}
}

func (a *App) getJavLibraryAccount(w http.ResponseWriter) {
	write(w, http.StatusOK, a.javLibraryAccountResponse())
}

func (a *App) putJavLibraryAccount(w http.ResponseWriter, r *http.Request) {
	input, ok := read(w, r)
	if !ok {
		return
	}
	current := a.javLibraryAccountConfig()
	for _, field := range []string{"username", "wanted_url", "favorites_url", "actors_url"} {
		if raw, exists := input[field]; exists {
			value, valid := raw.(string)
			if !valid || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
				fail(w, http.StatusBadRequest, field+" 必须是有效文本")
				return
			}
			current[field] = strings.TrimSpace(value)
		}
	}
	baseURL := a.javLibraryBaseURL()
	for _, field := range []string{"wanted_url", "favorites_url", "actors_url"} {
		if raw := str(current[field]); raw != "" {
			if err := integrations.ValidateJavLibraryAccountListURL(baseURL, raw); err != nil {
				fail(w, http.StatusBadRequest, field+" 无效："+err.Error())
				return
			}
		}
	}
	if raw, exists := input["cookie"]; exists {
		cookie, valid := raw.(string)
		if !valid || len(cookie) > 8192 || strings.ContainsAny(cookie, "\r\n") {
			fail(w, http.StatusBadRequest, "cookie 无效或超过 8 KiB")
			return
		}
		if strings.TrimSpace(cookie) != "" {
			current["cookie"] = strings.TrimSpace(cookie)
		}
	}
	if clear, exists := input["clear_cookie"]; exists {
		value, valid := clear.(bool)
		if !valid {
			fail(w, http.StatusBadRequest, "clear_cookie 必须为布尔值")
			return
		}
		if value {
			delete(current, "cookie")
		}
	}
	current["id"] = "javlibrary"
	if err := a.put(javLibraryAccountStore, current); err != nil {
		fail(w, http.StatusInternalServerError, "账号配置保存失败")
		return
	}
	write(w, http.StatusOK, a.javLibraryAccountResponse())
}

func (a *App) javLibraryBaseURL() string {
	if source, err := a.get("sources", "javlibrary"); err == nil {
		if base := strings.TrimSpace(str(source["base_url"])); base != "" {
			return base
		}
	}
	return "https://www.javlibrary.com"
}

func (a *App) syncJavLibraryAccount(w http.ResponseWriter, r *http.Request) {
	input, ok := read(w, r)
	if !ok {
		return
	}
	kindsRaw, ok := input["kinds"].([]any)
	if !ok || len(kindsRaw) == 0 {
		fail(w, http.StatusBadRequest, "需要指定同步类型")
		return
	}
	kinds := map[string]bool{}
	for _, raw := range kindsRaw {
		kind, ok := raw.(string)
		if !ok || (kind != "wanted" && kind != "favorites" && kind != "actors") {
			fail(w, http.StatusBadRequest, "同步类型仅支持 wanted、favorites、actors")
			return
		}
		kinds[kind] = true
	}
	account := a.javLibraryAccountConfig()
	proxyURL, err := a.outboundProxy()
	if err != nil {
		fail(w, http.StatusInternalServerError, "全局代理配置无效："+err.Error())
		return
	}
	client, err := integrations.NewJavLibraryAccountClient(integrations.CatalogConfig{
		Source: "javlibrary", BaseURL: a.javLibraryBaseURL(), ProxyURL: proxyURL, Cookie: str(account["cookie"]),
	})
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	imported := Object{"wanted": 0, "favorites": 0, "actors": 0}
	skipped := Object{"local_works": 0, "existing_candidates": 0, "already_favorite_actors": 0}
	errorsFound := []string{}
	knownWorks := recommendationOwnedCodeIndex(mustList(a, "works"))
	knownCandidates := candidateCodeSet(mustList(a, "candidates"))
	for _, kind := range []string{"wanted", "favorites"} {
		if !kinds[kind] {
			continue
		}
		urlField := kind + "_url"
		listURL := str(account[urlField])
		if listURL == "" {
			errorsFound = append(errorsFound, kind+"：尚未配置列表 URL")
			continue
		}
		items, importErr := client.ImportMovies(ctx, listURL, 20)
		if importErr != nil {
			errorsFound = append(errorsFound, kind+"："+importErr.Error())
			continue
		}
		for _, item := range items {
			if knownWorks.contains(item.Code) {
				skipped["local_works"] = num(skipped["local_works"]) + 1
				continue
			}
			if knownCandidates.contains(item.Code) {
				skipped["existing_candidates"] = num(skipped["existing_candidates"]) + 1
				continue
			}
			candidate := Object{
				"id": "javlibrary:" + strings.ToLower(item.Code), "code": item.Code, "title": item.Title,
				"url": "", "source_url": item.SourceURL, "source": "javlibrary", "metadata_only": true, "account_list": kind,
				"status": "pending", "created_at": now(),
			}
			if err := a.put("candidates", candidate); err != nil {
				errorsFound = append(errorsFound, kind+"：候选保存失败")
				continue
			}
			knownCandidates.add(item.Code)
			imported[kind] = num(imported[kind]) + 1
		}
	}
	if kinds["actors"] {
		listURL := str(account["actors_url"])
		if listURL == "" {
			errorsFound = append(errorsFound, "actors：尚未配置列表 URL")
		} else {
			actors, importErr := client.ImportFavoriteActors(ctx, listURL, 20)
			if importErr != nil {
				errorsFound = append(errorsFound, "actors："+importErr.Error())
			} else {
				for _, name := range actors {
					changed, wasFavorite, err := a.addJavLibraryFavoriteActor(name)
					if err != nil {
						errorsFound = append(errorsFound, "actors：演员状态保存失败")
						continue
					}
					if wasFavorite {
						skipped["already_favorite_actors"] = num(skipped["already_favorite_actors"]) + 1
					} else if changed {
						imported["actors"] = num(imported["actors"]) + 1
					}
				}
			}
		}
	}
	lastSync := now()
	account["last_sync"] = lastSync
	account["last_counts"] = imported
	if len(errorsFound) == 0 {
		account["last_status"] = "complete"
	} else if num(imported["wanted"])+num(imported["favorites"])+num(imported["actors"]) > 0 {
		account["last_status"] = "partial"
	} else {
		account["last_status"] = "failed"
	}
	if err := a.put(javLibraryAccountStore, account); err != nil {
		errorsFound = append(errorsFound, "账号同步状态保存失败")
	}
	write(w, http.StatusOK, Object{
		"imported": imported, "skipped_duplicates": skipped, "errors": errorsFound,
		"last_sync": lastSync, "complete": len(errorsFound) == 0,
	})
}

type candidateCodeIndex struct{ values map[string]bool }

func candidateCodeSet(candidates []Object) candidateCodeIndex {
	index := candidateCodeIndex{values: make(map[string]bool, len(candidates)*2)}
	for _, candidate := range candidates {
		index.add(str(candidate["code"]))
	}
	return index
}

func (index candidateCodeIndex) add(code string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return
	}
	index.values[strings.ToLower(code)] = true
	if normalized, known := normalizeWorkCode(code); known {
		index.values[normalized] = true
	}
}

func (index candidateCodeIndex) contains(code string) bool {
	code = strings.TrimSpace(code)
	if index.values[strings.ToLower(code)] {
		return true
	}
	if normalized, known := normalizeWorkCode(code); known && index.values[normalized] {
		return true
	}
	return false
}

func (a *App) addJavLibraryFavoriteActor(name string) (changed, alreadyFavorite bool, err error) {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" {
		return false, false, nil
	}
	policies, err := a.list("actor_policies")
	if err != nil {
		return false, false, err
	}
	key := name
	for _, policy := range policies {
		canonical := str(policy["name"])
		if canonical == "" {
			canonical = str(policy["id"])
		}
		if strings.EqualFold(strings.Join(strings.Fields(canonical), " "), name) || containsCaseInsensitive(policy["aliases"], name) {
			key = canonical
			break
		}
	}
	current, _ := a.get("actor_policies", key)
	if current == nil {
		current = Object{"id": key, "name": key, "policy": "follow", "target": 2}
	}
	if flag(current["favorite"]) {
		return false, true, nil
	}
	current["id"] = key
	if str(current["name"]) == "" {
		current["name"] = key
	}
	current["favorite"] = true
	if err := a.put("actor_policies", current); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func containsCaseInsensitive(raw any, target string) bool {
	for _, value := range stringSlice(raw) {
		if strings.EqualFold(strings.Join(strings.Fields(value), " "), strings.Join(strings.Fields(target), " ")) {
			return true
		}
	}
	return false
}
