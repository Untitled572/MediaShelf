package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	actorTranslationTimeout = 20 * time.Second
	actorTranslationLimit   = 5000
	myMemoryEndpoint        = "https://api.mymemory.translated.net/get"
	wikipediaZHEndpoint     = "https://zh.wikipedia.org/w/api.php"
)

type myMemoryResponse struct {
	ResponseStatus  int    `json:"responseStatus"`
	QuotaFinished   *bool  `json:"quotaFinished"`
	ResponseDetails string `json:"responseDetails"`
	ResponseData    struct {
		TranslatedText string `json:"translatedText"`
	} `json:"responseData"`
}

type actorTranslationWikidata struct {
	Entities map[string]struct {
		Descriptions map[string]struct {
			Value string `json:"value"`
		} `json:"descriptions"`
		Sitelinks map[string]struct {
			Title string `json:"title"`
		} `json:"sitelinks"`
	} `json:"entities"`
}

type actorTranslationWiki struct {
	Query struct {
		Pages []struct {
			Title   string `json:"title"`
			Missing bool   `json:"missing"`
			Extract string `json:"extract"`
		} `json:"pages"`
	} `json:"query"`
}

func (a *App) translateActorProfile(w http.ResponseWriter, r *http.Request, requested string) {
	name, _ := a.resolveActorProfileName(requested)
	profile, err := a.get("actor_profiles", actorKey(name))
	if err != nil || profile == nil {
		fail(w, http.StatusNotFound, "请先获取演员百科资料")
		return
	}
	biography := strings.TrimSpace(str(profile["biography"]))
	if biography == "" {
		fail(w, http.StatusBadRequest, "缓存资料没有可翻译的简介")
		return
	}
	if len([]rune(biography)) > actorTranslationLimit {
		fail(w, http.StatusBadRequest, "简介超过 5000 字，暂不支持翻译")
		return
	}
	input := Object{}
	if r.Body != nil {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		if readErr != nil {
			fail(w, http.StatusBadRequest, "无法读取翻译选项")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &input); err != nil {
				fail(w, http.StatusBadRequest, "翻译选项格式无效")
				return
			}
		}
	}
	hash := digest(biography)
	if str(profile["biography_zh"]) != "" && str(profile["translation_source_hash"]) == hash && !flag(input["force"]) {
		write(w, http.StatusOK, profile)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actorTranslationTimeout)
	defer cancel()
	client, err := a.actorTranslationClient()
	if err != nil {
		fail(w, http.StatusBadGateway, "翻译服务不可用："+err.Error())
		return
	}
	defer client.CloseIdleConnections()
	zhBiography, zhDescription, provider, sourceURL, err := translateCachedActorBiography(ctx, client, profile, biography, a.actorProfileAPIBase)
	if err != nil {
		fail(w, http.StatusBadGateway, "演员简介翻译失败："+err.Error())
		return
	}
	if strings.TrimSpace(zhBiography) == "" {
		fail(w, http.StatusBadGateway, "翻译服务没有返回中文简介")
		return
	}
	// Translation may take up to 20 seconds. Merge only into the latest cached
	// record if its original biography still matches what we translated.
	a.mu.Lock()
	latest, err := a.get("actor_profiles", actorKey(name))
	if err != nil || latest == nil || digest(strings.TrimSpace(str(latest["biography"]))) != hash {
		a.mu.Unlock()
		fail(w, http.StatusConflict, "演员资料在翻译期间已更新，请重新翻译")
		return
	}
	latest["biography_zh"] = zhBiography
	if zhDescription != "" {
		latest["description_zh"] = zhDescription
	}
	latest["translation_provider"] = provider
	latest["translation_updated_at"] = now()
	if sourceURL == "" {
		sourceURL = cachedProfileWikipediaURL(latest)
	}
	latest["translation_source_url"] = sourceURL
	latest["translation_source_hash"] = hash
	putErr := a.put("actor_profiles", latest)
	a.mu.Unlock()
	if err := putErr; err != nil {
		fail(w, http.StatusInternalServerError, "翻译缓存保存失败："+err.Error())
		return
	}
	write(w, http.StatusOK, latest)
}

func (a *App) actorTranslationClient() (*http.Client, error) {
	if a.actorProfileHTTPClient != nil {
		return a.actorProfileHTTPClient, nil
	}
	if a.actorProfileAPIBase != "" {
		return &http.Client{Timeout: actorTranslationTimeout}, nil
	}
	transport := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 12 * time.Second}
	proxyURL, err := a.outboundProxy()
	if err != nil {
		return nil, err
	}
	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.User != nil {
			return nil, errors.New("global proxy URL is invalid")
		}
		transport.Proxy = http.ProxyURL(proxy)
		transport.DialContext = (&net.Dialer{Timeout: 6 * time.Second}).DialContext
	} else {
		transport.DialContext = safeActorTranslationDial
	}
	return &http.Client{Transport: transport, Timeout: actorTranslationTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 2 || len(via) == 0 || req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
			return errors.New("translation service redirect was rejected")
		}
		return nil
	}}, nil
}

func safeActorTranslationDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || !actorTranslationHostAllowed(host) {
		return nil, errors.New("outbound host is not an approved translation endpoint")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() {
			return nil, errors.New("translation endpoint resolved to a private address")
		}
		conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		last = e
	}
	if last != nil {
		return nil, last
	}
	return nil, errors.New("translation endpoint did not resolve")
}

func actorTranslationHostAllowed(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "ja.wikipedia.org", "en.wikipedia.org", "zh.wikipedia.org", "www.wikidata.org", "api.mymemory.translated.net":
		return true
	default:
		return false
	}
}

func validateActorTranslationPublicHost(ctx context.Context, host string) error {
	if !actorTranslationHostAllowed(host) {
		return errors.New("translation endpoint host is not approved")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("translation endpoint has no DNS addresses")
	}
	for _, ip := range ips {
		if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() {
			return errors.New("translation endpoint resolved to a private address")
		}
	}
	return nil
}

func translateCachedActorBiography(ctx context.Context, client *http.Client, profile Object, biography, testBase string) (translated, description, provider, sourceURL string, err error) {
	baseWikiData, baseZhWiki, baseMyMemory := wikidataAPIEndpoint, wikipediaZHEndpoint, myMemoryEndpoint
	if testBase != "" {
		baseWikiData = strings.TrimRight(testBase, "/") + "/wikidata-zh"
		baseZhWiki = strings.TrimRight(testBase, "/") + "/wikipedia-zh"
		baseMyMemory = strings.TrimRight(testBase, "/") + "/mymemory"
	}
	if entityID := cachedProfileWikidataID(profile); entityID != "" {
		if testBase == "" {
			if err = validateActorTranslationPublicHost(ctx, "www.wikidata.org"); err != nil {
				return "", "", "", "", err
			}
		}
		values := url.Values{"action": {"wbgetentities"}, "ids": {entityID}, "props": {"sitelinks|descriptions"}, "languages": {"zh-hans|zh|ja"}, "format": {"json"}}
		var data actorTranslationWikidata
		if requestErr := getWikimediaJSON(ctx, client, baseWikiData, values, &data); requestErr == nil {
			entity := data.Entities[entityID]
			description = entity.Descriptions["zh-hans"].Value
			if description == "" {
				description = entity.Descriptions["zh"].Value
			}
			title := entity.Sitelinks["zhwiki"].Title
			if title != "" {
				if testBase == "" {
					if hostErr := validateActorTranslationPublicHost(ctx, "zh.wikipedia.org"); hostErr != nil {
						return "", "", "", "", hostErr
					}
				}
				pageValues := url.Values{"action": {"query"}, "titles": {title}, "prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "format": {"json"}, "formatversion": {"2"}, "variant": {"zh-cn"}}
				var page actorTranslationWiki
				if pageErr := getWikimediaJSON(ctx, client, baseZhWiki, pageValues, &page); pageErr == nil && len(page.Query.Pages) > 0 && !page.Query.Pages[0].Missing && strings.TrimSpace(page.Query.Pages[0].Extract) != "" {
					description = entity.Descriptions["zh-hans"].Value
					if description == "" {
						description = entity.Descriptions["zh"].Value
					}
					articleTitle := strings.ReplaceAll(page.Query.Pages[0].Title, " ", "_")
					return strings.TrimSpace(page.Query.Pages[0].Extract), description, "wikipedia_zh", "https://zh.wikipedia.org/wiki/" + url.PathEscape(articleTitle), nil
				}
			}
		}
	}
	if testBase == "" {
		if err = validateActorTranslationPublicHost(ctx, "api.mymemory.translated.net"); err != nil {
			return "", "", "", "", err
		}
	}
	language := "ja"
	if source, parseErr := url.Parse(cachedProfileWikipediaURL(profile)); parseErr == nil && source.Hostname() == "en.wikipedia.org" {
		language = "en"
	}
	chunks := splitTranslationText(biography, 500)
	translatedParts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		values := url.Values{"q": {chunk}, "langpair": {language + "|zh-CN"}}
		var raw json.RawMessage
		if err = getTranslationJSON(ctx, client, baseMyMemory, values, &raw); err != nil {
			return "", "", "", "", err
		}
		part, parseErr := parseMyMemoryTranslation(raw, chunk)
		if parseErr != nil {
			return "", "", "", "", parseErr
		}
		translatedParts = append(translatedParts, part)
	}
	return strings.Join(translatedParts, ""), description, "mymemory", myMemoryEndpoint, nil
}

func splitTranslationText(text string, maxBytes int) []string {
	if text == "" || maxBytes < 1 {
		return nil
	}
	runes := []rune(text)
	chunks := make([]string, 0, (len(text)+maxBytes-1)/maxBytes)
	for start := 0; start < len(runes); {
		end, preferred, used := start, start, 0
		for end < len(runes) {
			size := len(string(runes[end]))
			if used+size > maxBytes {
				break
			}
			used += size
			end++
			if translationBoundary(runes[end-1]) {
				preferred = end
			}
		}
		if end == len(runes) {
			chunks = append(chunks, string(runes[start:end]))
			break
		}
		cut := end
		if preferred > start {
			cut = preferred
		}
		chunks = append(chunks, string(runes[start:cut]))
		start = cut
	}
	return chunks
}

func translationBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', ';', '；', '\n', ' ', '\t':
		return true
	default:
		return false
	}
}

func parseMyMemoryTranslation(raw json.RawMessage, original string) (string, error) {
	var response myMemoryResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", errors.New("MyMemory 返回格式无效")
	}
	if response.ResponseStatus != http.StatusOK {
		return "", fmt.Errorf("MyMemory 返回状态 %d", response.ResponseStatus)
	}
	if response.QuotaFinished == nil || *response.QuotaFinished {
		return "", errors.New("MyMemory 当前配额已用完或未提供配额状态")
	}
	translated := strings.TrimSpace(html.UnescapeString(response.ResponseData.TranslatedText))
	if translated == "" || strings.EqualFold(translated, strings.TrimSpace(original)) || looksLikeTranslationQuotaMessage(translated+" "+response.ResponseDetails) {
		return "", errors.New("MyMemory 未返回有效译文，可能是配额或语言对不可用")
	}
	return translated, nil
}

func looksLikeTranslationQuotaMessage(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"quota", "limit exceeded", "exceeded the limit", "daily limit", "used all available", "free translations", "select two distinct languages", "no translation found", "translation unavailable", "please try again later"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(text, "配额") || strings.Contains(text, "额度")
}

func cachedProfileWikidataID(profile Object) string {
	for _, source := range asObjects(profile["sources"]) {
		if str(source["label"]) != "Wikidata" {
			continue
		}
		u, err := url.Parse(str(source["url"]))
		if err == nil && strings.EqualFold(u.Hostname(), "www.wikidata.org") {
			id := strings.TrimPrefix(u.Path, "/wiki/")
			if wikidataIDPattern.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

func cachedProfileWikipediaURL(profile Object) string {
	for _, source := range asObjects(profile["sources"]) {
		if str(source["label"]) != "Wikipedia" {
			continue
		}
		u, err := url.Parse(str(source["url"]))
		if err == nil && (strings.EqualFold(u.Hostname(), "ja.wikipedia.org") || strings.EqualFold(u.Hostname(), "en.wikipedia.org")) && u.Scheme == "https" {
			return u.String()
		}
	}
	return ""
}

func getTranslationJSON(ctx context.Context, client *http.Client, endpoint string, values url.Values, out *json.RawMessage) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "MediaShelf/1.0 (public actor biography translation)")
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, actorProfileBodyLimit+1))
			resp.Body.Close()
			if readErr != nil {
				err = readErr
			} else if len(body) > actorProfileBodyLimit {
				return errors.New("translation response exceeded 1 MiB")
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = &wikimediaHTTPError{status: resp.StatusCode}
			} else if !json.Valid(body) {
				return errors.New("translation service returned invalid JSON")
			} else {
				*out = body
				return nil
			}
		}
		if attempt != 0 || ctx.Err() != nil || !retryableWikimediaError(err) {
			return err
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("translation request failed")
}
