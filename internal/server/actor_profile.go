package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	actorProfileTimeout        = 45 * time.Second
	actorProfileRequestTimeout = 15 * time.Second
	actorProfileBodyLimit      = 1 << 20
	wikipediaJAEndpoint        = "https://ja.wikipedia.org/w/api.php"
	wikipediaENEndpoint        = "https://en.wikipedia.org/w/api.php"
	wikidataAPIEndpoint        = "https://www.wikidata.org/w/api.php"
	wikipediaJAArticlePrefix   = "https://ja.wikipedia.org/wiki/"
	wikipediaENArticlePrefix   = "https://en.wikipedia.org/wiki/"
	wikidataAdultActorID       = "Q488111"
)

var (
	xHandlePattern        = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	instagramPattern      = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)
	youtubeChannelPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,100}$`)
	wikidataIDPattern     = regexp.MustCompile(`^Q[1-9][0-9]*$`)
)

type wikiPageResponse struct {
	Query struct {
		Pages []struct {
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
			Extract   string `json:"extract"`
			PageProps struct {
				WikidataID string `json:"wikibase_item"`
			} `json:"pageprops"`
			Thumbnail struct {
				Source string `json:"source"`
			} `json:"thumbnail"`
		} `json:"pages"`
		Redirects []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"redirects"`
	} `json:"query"`
}

type wikidataValue struct {
	Value any `json:"value"`
}

type wikidataSnak struct {
	DataValue wikidataValue `json:"datavalue"`
}

type wikidataStatement struct {
	MainSnak wikidataSnak `json:"mainsnak"`
}

type wikidataEntity struct {
	Labels map[string]struct {
		Value string `json:"value"`
	} `json:"labels"`
	Descriptions map[string]struct {
		Value string `json:"value"`
	} `json:"descriptions"`
	Claims map[string][]wikidataStatement `json:"claims"`
}

type wikidataResponse struct {
	Entities map[string]wikidataEntity `json:"entities"`
}

func (a *App) getActorProfile(w http.ResponseWriter, requested string) {
	name, policy := a.resolveActorProfileName(requested)
	profile, err := a.get("actor_profiles", actorKey(name))
	if err != nil {
		profile = emptyActorProfile(name, actorNames(policy["aliases"]))
	}
	write(w, http.StatusOK, profile)
}

func (a *App) refreshActorProfile(w http.ResponseWriter, r *http.Request, requested string) {
	name, policy := a.resolveActorProfileName(requested)
	profile, err := a.fetchActorProfile(r.Context(), name, actorNames(policy["aliases"]))
	if err != nil {
		message := "演员资料刷新失败：" + err.Error()
		var network net.Error
		if (errors.As(err, &network) && network.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
			message = "演员百科连接超时，已自动重试。请稍后再试或检查设置页代理；已缓存资料仍可查看。"
		}
		fail(w, http.StatusBadGateway, message)
		return
	}
	a.mu.Lock()
	// Keep a valid translation when refreshing an unchanged public biography.
	if previous, getErr := a.get("actor_profiles", actorKey(name)); getErr == nil && strings.TrimSpace(str(profile["biography"])) != "" && str(previous["translation_source_hash"]) == digest(strings.TrimSpace(str(profile["biography"]))) {
		for _, key := range []string{"biography_zh", "description_zh", "translation_provider", "translation_updated_at", "translation_source_url", "translation_source_hash"} {
			if value, ok := previous[key]; ok {
				profile[key] = value
			}
		}
	}
	putErr := a.put("actor_profiles", profile)
	a.mu.Unlock()
	if err := putErr; err != nil {
		fail(w, http.StatusInternalServerError, "演员资料缓存保存失败："+err.Error())
		return
	}
	write(w, http.StatusOK, profile)
}

func (a *App) resolveActorProfileName(requested string) (string, Object) {
	requested = strings.TrimSpace(requested)
	policies := actorPolicies(a)
	aliases := aliasMap(policies)
	canonical := canonicalActor(requested, aliases)
	policy := policies[actorKey(canonical)]
	if policy == nil {
		policy = Object{"name": canonical}
	}
	if canonical == "" {
		canonical = requested
	}
	return canonical, policy
}

func emptyActorProfile(name string, aliases []string) Object {
	name = strings.TrimSpace(name)
	return Object{
		"id": actorKey(name), "name": name, "description": "", "biography": "", "birth_date": "", "birth_place": "", "nationality": []string{},
		"aliases": aliases, "avatar_url": "", "links": []Object{}, "sources": []Object{}, "updated_at": "", "status": "not_fetched",
	}
}

func (a *App) fetchActorProfile(parent context.Context, name string, aliases []string) (Object, error) {
	ctx, cancel := context.WithTimeout(parent, actorProfileTimeout)
	defer cancel()
	wikipediaJAURL, wikipediaENURL, wikidataURL := wikipediaJAEndpoint, wikipediaENEndpoint, wikidataAPIEndpoint
	if a.actorProfileAPIBase != "" {
		// This override is only set by package tests. Production endpoints above
		// are constants, so user input cannot choose an outbound host.
		wikipediaJAURL = strings.TrimRight(a.actorProfileAPIBase, "/") + "/wikipedia-ja"
		wikipediaENURL = strings.TrimRight(a.actorProfileAPIBase, "/") + "/wikipedia-en"
		wikidataURL = strings.TrimRight(a.actorProfileAPIBase, "/") + "/wikidata"
	}
	client, err := a.actorProfileClient()
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	values := url.Values{
		"action": {"query"}, "titles": {name}, "prop": {"extracts|pageprops|pageimages"}, "redirects": {"1"},
		"exintro": {"1"}, "explaintext": {"1"}, "pithumbsize": {"400"}, "format": {"json"}, "formatversion": {"2"},
	}
	var page wikiPageResponse
	if err := getWikimediaJSON(ctx, client, wikipediaJAURL, values, &page); err != nil {
		return nil, err
	}
	fromEnglish := false
	if len(page.Query.Pages) == 0 || page.Query.Pages[0].Missing {
		page = wikiPageResponse{}
		if err := getWikimediaJSON(ctx, client, wikipediaENURL, values, &page); err != nil {
			return nil, err
		}
		if len(page.Query.Pages) == 0 || page.Query.Pages[0].Missing {
			profile := emptyActorProfile(name, aliases)
			profile["status"] = "not_found"
			profile["updated_at"] = now()
			return profile, nil
		}
		fromEnglish = true
	}
	wiki := page.Query.Pages[0]
	redirectedFromName := false
	for _, redirect := range page.Query.Redirects {
		if actorProfileNameMatch(redirect.From, name) && actorProfileNameMatch(redirect.To, wiki.Title) {
			redirectedFromName = true
		}
	}
	// Only accept a direct exact page or a redirect explicitly reported by
	// Wikimedia from this exact requested name. Search results are never used.
	if !actorProfileNameMatch(wiki.Title, name) && !redirectedFromName {
		return nil, fmt.Errorf("Wikipedia returned a different title (%q)", wiki.Title)
	}
	profile := emptyActorProfile(name, aliases)
	articlePrefix := wikipediaJAArticlePrefix
	if fromEnglish {
		articlePrefix = wikipediaENArticlePrefix
	}
	articleURL := articlePrefix + url.PathEscape(strings.ReplaceAll(wiki.Title, " ", "_"))
	profile["sources"] = []Object{{"label": "Wikipedia", "url": articleURL}}
	if avatar := safeWikimediaThumbnail(wiki.Thumbnail.Source); avatar != "" {
		profile["avatar_url"] = avatar
	}

	entityID := wiki.PageProps.WikidataID
	if !wikidataIDPattern.MatchString(entityID) {
		profile["status"] = "unverified"
		profile["updated_at"] = now()
		return profile, nil
	}
	entityValues := url.Values{"action": {"wbgetentities"}, "ids": {entityID}, "props": {"labels|descriptions|claims"}, "languages": {"ja|en"}, "format": {"json"}}
	var entityData wikidataResponse
	if err := getWikimediaJSON(ctx, client, wikidataURL, entityValues, &entityData); err != nil {
		return nil, err
	}
	entity, exists := entityData.Entities[entityID]
	if !exists {
		return nil, fmt.Errorf("Wikidata entity %s was unavailable", entityID)
	}
	entityIDs := []string{entityID}
	entityIDs = append(entityIDs, wikidataItemIDs(entity, "P19")...)
	entityIDs = append(entityIDs, wikidataItemIDs(entity, "P27")...)
	entityIDs = uniqueStrings(entityIDs)
	labelsValues := url.Values{"action": {"wbgetentities"}, "ids": {strings.Join(entityIDs, "|")}, "props": {"labels"}, "languages": {"ja|en"}, "format": {"json"}}
	var labelsData wikidataResponse
	if err := getWikimediaJSON(ctx, client, wikidataURL, labelsValues, &labelsData); err != nil {
		return nil, err
	}
	entity.Labels = labelsData.Entities[entityID].Labels
	if !hasAdultActorEvidence(entity, wiki.Extract) {
		profile["status"] = "unverified"
		profile["updated_at"] = now()
		return profile, nil
	}
	profile["status"] = "found"
	profile["gender"] = "unknown"
	for _, genderID := range wikidataItemIDs(entity, "P21") {
		if genderID == "Q6581072" {
			profile["gender"] = "female"
			profile["gender_source"] = "wikidata"
			break
		}
		if genderID == "Q6581097" {
			profile["gender"] = "male"
			profile["gender_source"] = "wikidata"
			break
		}
	}
	profile["biography"] = strings.TrimSpace(wiki.Extract)
	description := entity.Descriptions["ja"].Value
	if description == "" {
		description = entity.Descriptions["en"].Value
	}
	profile["description"] = description
	profile["birth_date"] = wikidataDate(entity, "P569")
	profile["birth_place"] = wikidataItemLabel(entity, labelsData.Entities, "P19")
	nationality := wikidataItemLabels(entity, labelsData.Entities, "P27")
	profile["nationality"] = nationality
	profile["links"] = wikidataProfileLinks(entity)
	profile["sources"] = append(asObjects(profile["sources"]), Object{"label": "Wikidata", "url": "https://www.wikidata.org/wiki/" + entityID})
	profile["updated_at"] = now()
	return profile, nil
}

func hasAdultActorOccupation(entity wikidataEntity) bool {
	for _, id := range wikidataItemIDs(entity, "P106") {
		if id == wikidataAdultActorID {
			return true
		}
	}
	return false
}

func hasAdultActorEvidence(entity wikidataEntity, biography string) bool {
	if hasAdultActorOccupation(entity) {
		return true
	}
	description := entity.Descriptions["ja"].Value + " " + entity.Descriptions["en"].Value
	lower := strings.ToLower(description)
	if strings.Contains(lower, "adult film actress") || strings.Contains(lower, "adult film actor") || strings.Contains(lower, "pornographic actress") || strings.Contains(lower, "pornographic film actor") || strings.Contains(lower, "av actress") || strings.Contains(lower, "av actor") {
		return true
	}
	return strings.Contains(biography, "AV女優") || strings.Contains(biography, "AV男優") || strings.Contains(biography, "成人向け女優") || strings.Contains(biography, "ポルノ俳優")
}

func actorProfileNameMatch(a, b string) bool {
	if actorKey(a) == actorKey(b) {
		return true
	}
	compact := func(s string) string {
		var result strings.Builder
		for _, r := range s {
			if !unicode.IsSpace(r) {
				result.WriteRune(r)
			}
		}
		return result.String()
	}
	return compact(a) != "" && compact(a) == compact(b) && hasJapaneseRunes(a) && hasJapaneseRunes(b)
}

func hasJapaneseRunes(s string) bool {
	for _, r := range s {
		if (r >= 0x3040 && r <= 0x30ff) || (r >= 0x3400 && r <= 0x9fff) {
			return true
		}
	}
	return false
}

func (a *App) actorProfileClient() (*http.Client, error) {
	if a.actorProfileHTTPClient != nil {
		return a.actorProfileHTTPClient, nil
	}
	if a.actorProfileAPIBase != "" {
		return &http.Client{Timeout: actorProfileTimeout}, nil
	}
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	proxyURL, err := a.outboundProxy()
	if err != nil {
		return nil, err
	}
	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") {
			return nil, errors.New("global proxy URL is invalid")
		}
		transport.Proxy = http.ProxyURL(proxy)
		transport.DialContext = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	} else {
		transport.DialContext = safeWikimediaDial
	}
	return &http.Client{Transport: transport, Timeout: actorProfileRequestTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 2 || req.URL.Scheme != "https" || len(via) == 0 || !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
			return errors.New("Wikimedia redirect was rejected")
		}
		return nil
	}}, nil
}

func safeWikimediaDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || (host != "ja.wikipedia.org" && host != "en.wikipedia.org" && host != "www.wikidata.org") {
		return nil, errors.New("outbound host is not an approved Wikimedia API")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() {
			return nil, errors.New("Wikimedia API resolved to a non-public address")
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var last error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last != nil {
		return nil, last
	}
	return nil, errors.New("Wikimedia API host did not resolve")
}

type wikimediaHTTPError struct{ status int }

func (e *wikimediaHTTPError) Error() string {
	return fmt.Sprintf("Wikimedia API returned HTTP %d", e.status)
}
func retryableWikimediaError(err error) bool {
	var response *wikimediaHTTPError
	if errors.As(err, &response) {
		return response.status == 429 || response.status == 502 || response.status == 503 || response.status == 504
	}
	var network net.Error
	return (errors.As(err, &network) && network.Timeout()) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
func getWikimediaJSON(ctx context.Context, client *http.Client, endpoint string, values url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		err := getWikimediaJSONAttempt(ctx, client, endpoint, values, out)
		if err == nil {
			return nil
		}
		if attempt == 1 || ctx.Err() != nil || !retryableWikimediaError(err) {
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
	return errors.New("Wikimedia request failed")
}
func getWikimediaJSONAttempt(ctx context.Context, client *http.Client, endpoint string, values url.Values, out any) error {
	requestURL := endpoint + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaShelf/1.0 (public actor profile lookup)")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &wikimediaHTTPError{status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, actorProfileBodyLimit+1))
	if err != nil {
		return err
	}
	if len(body) > actorProfileBodyLimit {
		return errors.New("Wikimedia response exceeded 1 MiB")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid Wikimedia JSON response: %w", err)
	}
	return nil
}

func wikidataPropertyValues(entity wikidataEntity, property string) []any {
	out := []any{}
	for _, statement := range entity.Claims[property] {
		if statement.MainSnak.DataValue.Value != nil {
			out = append(out, statement.MainSnak.DataValue.Value)
		}
	}
	return out
}

func wikidataItemIDs(entity wikidataEntity, property string) []string {
	ids := []string{}
	for _, value := range wikidataPropertyValues(entity, property) {
		if object, ok := value.(map[string]any); ok {
			id, _ := object["id"].(string)
			if wikidataIDPattern.MatchString(id) {
				ids = append(ids, id)
			}
		}
	}
	return uniqueStrings(ids)
}

func wikidataItemLabel(entity wikidataEntity, all map[string]wikidataEntity, property string) string {
	labels := wikidataItemLabels(entity, all, property)
	if len(labels) > 0 {
		return labels[0]
	}
	return ""
}

func wikidataItemLabels(entity wikidataEntity, all map[string]wikidataEntity, property string) []string {
	labels := []string{}
	for _, id := range wikidataItemIDs(entity, property) {
		label := all[id].Labels["ja"].Value
		if label == "" {
			label = all[id].Labels["en"].Value
		}
		if label != "" {
			labels = appendUnique(labels, label)
		}
	}
	return labels
}

func wikidataDate(entity wikidataEntity, property string) string {
	for _, value := range wikidataPropertyValues(entity, property) {
		if object, ok := value.(map[string]any); ok {
			if raw, ok := object["time"].(string); ok {
				if len(raw) >= 11 {
					return strings.TrimPrefix(raw[:11], "+")[:10]
				}
			}
		}
	}
	return ""
}

func wikidataProfileLinks(entity wikidataEntity) []Object {
	links := []Object{}
	seen := map[string]bool{}
	for _, value := range wikidataPropertyValues(entity, "P2002") {
		if handle, ok := value.(string); ok && xHandlePattern.MatchString(handle) {
			appendProfileLink(&links, seen, "X", "https://x.com/"+url.PathEscape(handle))
		}
	}
	for _, value := range wikidataPropertyValues(entity, "P2003") {
		if handle, ok := value.(string); ok && instagramPattern.MatchString(handle) {
			appendProfileLink(&links, seen, "Instagram", "https://www.instagram.com/"+url.PathEscape(handle)+"/")
		}
	}
	for _, value := range wikidataPropertyValues(entity, "P2397") {
		if channel, ok := value.(string); ok && youtubeChannelPattern.MatchString(channel) {
			appendProfileLink(&links, seen, "YouTube", "https://www.youtube.com/channel/"+url.PathEscape(channel))
		}
	}
	for _, value := range wikidataPropertyValues(entity, "P856") {
		if raw, ok := value.(string); ok {
			if safe := safeOfficialURL(raw); safe != "" {
				appendProfileLink(&links, seen, "Official website", safe)
			}
		}
	}
	if len(links) == 0 {
		return links
	}
	return links
}

func appendProfileLink(links *[]Object, seen map[string]bool, label, raw string) {
	if !seen[raw] {
		*links = append(*links, Object{"label": label, "url": raw})
		seen[raw] = true
	}
}

func safeOfficialURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return ""
	}
	return u.String()
}

func safeWikimediaThumbnail(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "upload.wikimedia.org") || u.User != nil {
		return ""
	}
	return u.String()
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
