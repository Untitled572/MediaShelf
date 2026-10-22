package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const wikidataProxyTestEndpoint = "https://www.wikidata.org/w/api.php?action=query&meta=siteinfo&format=json"

func (a *App) mainSettings() (Object, error) {
	settings := Object{
		"id": "main", "proxy_enabled": true, "proxy_url": "http://127.0.0.1:7890",
	}
	saved, err := a.get("settings", "main")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	for key, value := range saved {
		settings[key] = value
	}
	settings["id"] = "main"
	enabled, ok := settings["proxy_enabled"].(bool)
	if !ok {
		return nil, errors.New("proxy_enabled 必须为布尔值")
	}
	proxyURL, ok := settings["proxy_url"].(string)
	if !ok {
		return nil, errors.New("proxy_url 必须为字符串")
	}
	if err := validateOutboundProxyURL(proxyURL, enabled); err != nil {
		return nil, err
	}
	return settings, nil
}

func (a *App) patchMainSettings(input Object) (Object, error) {
	settings, err := a.mainSettings()
	if err != nil {
		return nil, err
	}
	for key, value := range input {
		if key != "id" {
			settings[key] = value
		}
	}
	settings["id"] = "main"
	enabled, ok := settings["proxy_enabled"].(bool)
	if !ok {
		return nil, errors.New("proxy_enabled 必须为布尔值")
	}
	proxyURL, ok := settings["proxy_url"].(string)
	if !ok {
		return nil, errors.New("proxy_url 必须为字符串")
	}
	if err := validateOutboundProxyURL(proxyURL, enabled); err != nil {
		return nil, err
	}
	if err := a.put("settings", settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func validateOutboundProxyURL(raw string, required bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return errors.New("启用代理时必须填写代理地址")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("代理地址必须是绝对 HTTP(S) URL")
	}
	if u.User != nil {
		return errors.New("代理地址不能包含用户名或密码")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("代理地址不能包含查询参数或片段")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("代理地址端口无效")
		}
	}
	return nil
}

// outboundProxy returns the one shared proxy setting for all public metadata
// and feed traffic. Legacy per-source proxy_url values are intentionally
// ignored so a saved source cannot override the global switch or address.
func (a *App) outboundProxy() (string, error) {
	settings, err := a.mainSettings()
	if err != nil {
		return "", err
	}
	if !flag(settings["proxy_enabled"]) {
		return "", nil
	}
	return strings.TrimSpace(str(settings["proxy_url"])), nil
}

func (a *App) proxyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	settings, err := a.mainSettings()
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if !flag(settings["proxy_enabled"]) {
		fail(w, http.StatusBadRequest, "全局代理已关闭，请启用后测试")
		return
	}
	proxyURL := str(settings["proxy_url"])
	if err := validateOutboundProxyURL(proxyURL, true); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	endpoint := wikidataProxyTestEndpoint
	if a.proxyTestEndpoint != "" { // package test seam; production always targets fixed Wikidata API.
		endpoint = a.proxyTestEndpoint
	}
	proxy, _ := url.Parse(proxyURL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DialContext: (&net.Dialer{Timeout: 4 * time.Second}).DialContext, TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 6 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("代理测试端点发生重定向")
	}}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err == nil {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "MediaShelf/1.0 proxy test")
	}
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = fmt.Errorf("Wikidata API returned HTTP %d", resp.StatusCode)
			} else {
				var payload map[string]any
				limited := io.LimitReader(resp.Body, (256<<10)+1)
				var body []byte
				body, err = io.ReadAll(limited)
				if err == nil && len(body) > 256<<10 {
					err = errors.New("Wikidata response exceeded 256 KiB")
				}
				if err == nil {
					err = json.Unmarshal(body, &payload)
					if err == nil {
						if _, ok := payload["query"].(map[string]any); !ok {
							err = errors.New("Wikidata did not return a valid API response")
						}
					}
				}
			}
		}
	}
	if err != nil {
		fail(w, http.StatusBadGateway, "代理连接测试失败："+err.Error())
		return
	}
	write(w, http.StatusOK, Object{"ok": true, "checked_at": now(), "endpoint": "Wikidata"})
}
