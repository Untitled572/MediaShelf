package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type CatalogConfig struct{ Source, BaseURL, ProxyURL, Cookie string }
type Metadata struct {
	Code           string   `json:"code"`
	Title          string   `json:"title"`
	Year           int      `json:"year,omitempty"`
	Actors         []string `json:"actors,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Rating         *float64 `json:"rating,omitempty"`
	SourceRating   *float64 `json:"source_rating,omitempty"`
	RatingCount    int      `json:"rating_count,omitempty"`
	RatingRaw      string   `json:"rating_raw,omitempty"`
	RatingCountRaw string   `json:"rating_count_raw,omitempty"`
	RatingScale    string   `json:"rating_scale,omitempty"`
	CoverURL       string   `json:"cover_url,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	Source         string   `json:"source"`
}
type ActorPage struct {
	Actor      string     `json:"actor"`
	Works      []Metadata `json:"works"`
	Page       int        `json:"page"`
	TotalPages int        `json:"total_pages,omitempty"`
	NextURL    string     `json:"next_url,omitempty"`
	HasMore    bool       `json:"has_more"`
	Complete   bool       `json:"complete"`
}
type CatalogClient struct {
	source, base    string
	client          *http.Client
	cookie          string
	proxyConfigured bool
}

func NewCatalogClient(c CatalogConfig) (*CatalogClient, error) {
	source := strings.ToLower(strings.TrimSpace(c.Source))
	if source == "" {
		source = "javdb"
	}
	bases := map[string]string{"javdb": "https://javdb.com", "javlibrary": "https://www.javlibrary.com", "fanza": "https://www.dmm.co.jp"}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = bases[source]
	}
	if _, ok := bases[source]; !ok {
		return nil, fmt.Errorf("unsupported catalog source %q", source)
	}
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("catalog base URL must be absolute HTTP(S)")
	}
	transport := &http.Transport{DialContext: safeExternalDial}
	if c.ProxyURL != "" {
		pu, e := url.Parse(c.ProxyURL)
		if e != nil || pu.Host == "" {
			return nil, errors.New("invalid catalog proxy URL")
		}
		transport.Proxy = http.ProxyURL(pu)
		transport.DialContext = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	cookie := c.Cookie
	if source == "fanza" && !strings.Contains(strings.ToLower(cookie), "age_check_done=") {
		if cookie != "" {
			cookie += "; "
		}
		cookie += "age_check_done=1"
	}
	return &CatalogClient{source: source, base: base, cookie: cookie, proxyConfigured: c.ProxyURL != "", client: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: providerRedirectPolicy}}, nil
}
func providerRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many provider redirects")
	}
	if len(via) > 0 && !sameOrigin(req.URL, via[len(via)-1].URL) {
		req.Header.Del("Cookie")
	}
	return validateExternalURL(req.URL)
}
func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func safeExternalDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil {
		return nil, e
	}
	var last error
	d := net.Dialer{Timeout: 8 * time.Second}
	for _, ip := range ips {
		if forbiddenIP(ip.IP) {
			continue
		}
		c, e := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	if last != nil {
		return nil, last
	}
	return nil, fmt.Errorf("provider host resolves only to local or private addresses")
}
func forbiddenIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, raw := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, network, _ := net.ParseCIDR(raw)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
func validateExternalURL(u *url.URL) error {
	if u == nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("provider returned a non-HTTP URL")
	}
	host := strings.Trim(u.Hostname(), "[]")
	if ip := net.ParseIP(host); ip != nil && forbiddenIP(ip) {
		return errors.New("provider URL points to a local or private address")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return errors.New("provider URL points to localhost")
	}
	return nil
}
func (c *CatalogClient) fetch(ctx context.Context, raw string) (*html.Node, string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return nil, "", e
	}
	if !u.IsAbs() {
		root, _ := url.Parse(c.base)
		u = root.ResolveReference(u)
	}
	if e = validateExternalURL(u); e != nil {
		return nil, "", e
	}
	if c.proxyConfigured {
		if e = validateExternalHost(ctx, u.Hostname()); e != nil {
			return nil, "", e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, "", e
	}
	req.Header.Set("User-Agent", "MediaShelf/1.0 (+metadata lookup)")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7,ja;q=0.5")
	baseURL, _ := url.Parse(c.base)
	if c.cookie != "" && sameOrigin(u, baseURL) {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		return nil, "", fmt.Errorf("%s provider blocked request (HTTP %d)", c.source, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		return nil, "", fmt.Errorf("%s provider requires login", c.source)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s provider returned HTTP %d", c.source, resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		return nil, "", e
	}
	if len(b) > 4<<20 {
		return nil, "", errors.New("provider response exceeds 4 MiB limit")
	}
	if hasBrowserVerificationChallenge(b) {
		return nil, "", fmt.Errorf("%s provider requires browser verification; data source is restricted", c.source)
	}
	n, e := html.Parse(strings.NewReader(string(b)))
	if e != nil {
		return nil, "", e
	}
	return n, resp.Request.URL.String(), nil
}

// hasBrowserVerificationChallenge matches explicit Cloudflare challenge-page
// markers. Generic Cloudflare CDN or Insights URLs are common on ordinary
// pages and must not be treated as an access challenge.
func hasBrowserVerificationChallenge(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "challenge-platform") || strings.Contains(s, "cf-chl-") || strings.Contains(s, "cf_chl_")
}

func validateExternalHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if forbiddenIP(ip) {
			return errors.New("provider host resolves to a local or reserved address")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("provider host has no DNS addresses")
	}
	for _, ip := range ips {
		if forbiddenIP(ip.IP) {
			return errors.New("provider host resolves to a local or reserved address")
		}
	}
	return nil
}
func (c *CatalogClient) Lookup(ctx context.Context, code string) (Metadata, error) {
	code = normalizeCode(code)
	if code == "" {
		return Metadata{}, errors.New("code is required")
	}
	switch c.source {
	case "javdb":
		return c.lookupJavDB(ctx, code)
	case "javlibrary":
		return c.lookupJavLibrary(ctx, code)
	case "fanza":
		return c.lookupFanza(ctx, code)
	default:
		return Metadata{}, errors.New("unsupported catalog source")
	}
}
func (c *CatalogClient) lookupJavDB(ctx context.Context, code string) (Metadata, error) {
	q := url.Values{"q": {code}}
	root, _, e := c.fetch(ctx, c.base+"/search?"+q.Encode())
	if e != nil {
		return Metadata{}, e
	}
	var detail string
	walk(root, func(n *html.Node) {
		if detail != "" || n.Type != html.ElementNode || n.Data != "a" || !hasClass(n, "box") {
			return
		}
		title := videoCodeText(n)
		if normalizeCode(title) == code {
			detail = attr(n, "href")
		}
	})
	if detail == "" {
		return Metadata{}, fmt.Errorf("%s: code %s not found", c.source, code)
	}
	n, u, e := c.fetch(ctx, detail)
	if e != nil {
		return Metadata{}, e
	}
	return parseDetail(n, code, c.source, u), nil
}
func (c *CatalogClient) lookupJavLibrary(ctx context.Context, code string) (Metadata, error) {
	q := url.Values{"keyword": {code}}
	root, _, e := c.fetch(ctx, c.base+"/cn/vl_searchbyid.php?"+q.Encode())
	if e != nil {
		return Metadata{}, e
	}
	var detail string
	walk(root, func(n *html.Node) {
		if detail != "" || n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		if hasAncestorClass(n, "video") {
			id := strings.TrimSpace(firstClassText(n, "id"))
			if normalizeCode(id) == code {
				detail = attr(n, "href")
			}
		}
	})
	if detail == "" {
		return Metadata{}, fmt.Errorf("%s: code %s not found", c.source, code)
	}
	n, u, e := c.fetch(ctx, detail)
	if e != nil {
		return Metadata{}, e
	}
	return parseDetail(n, code, c.source, u), nil
}
func (c *CatalogClient) lookupFanza(ctx context.Context, code string) (Metadata, error) {
	raw := c.base + "/search/=/searchstr=" + url.PathEscape(code) + "/limit=30/sort=rankprofile"
	root, _, e := c.fetch(ctx, raw)
	if e != nil {
		return Metadata{}, e
	}
	var detail string
	walk(root, func(n *html.Node) {
		if detail != "" || n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		href := attr(n, "href")
		if strings.Contains(href, "/detail/") {
			if strings.Contains(strings.ToLower(href), "cid="+strings.ToLower(code)) || normalizeCode(codeRE.FindString(nodeText(n))) == code {
				detail = href
			}
		}
	})
	if detail == "" {
		return Metadata{}, fmt.Errorf("%s: code %s not found", c.source, code)
	}
	n, u, e := c.fetch(ctx, detail)
	if e != nil {
		return Metadata{}, e
	}
	return parseDetail(n, code, c.source, u), nil
}

func (c *CatalogClient) SearchActor(ctx context.Context, name string) (ActorPage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ActorPage{}, errors.New("actor name is required")
	}
	var search string
	switch c.source {
	case "javdb":
		q := url.Values{"q": {name}, "f": {"actor"}}
		search = c.base + "/search?" + q.Encode()
	case "javlibrary":
		q := url.Values{"keyword": {name}}
		search = c.base + "/cn/vl_star.php?" + q.Encode()
	case "fanza":
		search = c.base + "/search/=/searchstr=" + url.PathEscape(name) + "/"
	default:
		return ActorPage{}, errors.New("unsupported catalog source")
	}
	root, _, e := c.fetch(ctx, search)
	if e != nil {
		return ActorPage{}, e
	}
	var p ActorPage
	p.Page = 1
	walk(root, func(n *html.Node) {
		if p.Actor != "" || n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		href, text := attr(n, "href"), strings.TrimSpace(nodeText(n))
		if href == "" || text == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
			return
		}
		switch c.source {
		case "javdb":
			if hasClass(n, "actor-box") || hasAncestorClass(n, "actor-box") {
				p.Actor = href
			}
		case "javlibrary":
			if hasAncestorClass(n, "star") || strings.Contains(strings.ToLower(href), "vl_star.php") {
				p.Actor = href
			}
		case "fanza":
			h := strings.ToLower(href)
			if strings.Contains(h, "article=actress") || strings.Contains(h, "/actress/") || strings.Contains(h, "/performer/") {
				p.Actor = href
			}
		}
	})
	if p.Actor == "" {
		return p, fmt.Errorf("%s: actor %q not found in provider search results", c.source, name)
	}
	return p, nil
}
func (c *CatalogClient) Catalog(ctx context.Context, actor string, page int) (ActorPage, error) {
	if page < 1 {
		return ActorPage{}, errors.New("page must be >= 1")
	}
	target := strings.TrimSpace(actor)
	if target == "" {
		return ActorPage{}, errors.New("actor is required")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "/") {
		p, e := c.SearchActor(ctx, target)
		if e != nil {
			return ActorPage{}, e
		}
		target = p.Actor
	}
	u, e := url.Parse(target)
	if e != nil {
		return ActorPage{}, e
	}
	if !u.IsAbs() {
		rel, parseErr := url.Parse(target)
		if parseErr != nil {
			return ActorPage{}, parseErr
		}
		base, _ := url.Parse(c.base)
		u = base.ResolveReference(rel)
	}
	if e = validateExternalURL(u); e != nil {
		return ActorPage{}, e
	}
	q := u.Query()
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u.RawQuery = q.Encode()
	root, final, e := c.fetch(ctx, u.String())
	if e != nil {
		return ActorPage{}, e
	}
	p := ActorPage{Actor: target, Page: page, Works: []Metadata{}}
	switch c.source {
	case "javdb":
		parseJavDBCatalog(root, final, &p)
	case "javlibrary":
		parseJavLibraryCatalog(root, final, &p)
	case "fanza":
		parseFanzaCatalog(root, final, &p)
	}
	p.NextURL = nextPageURL(root, final)
	p.HasMore = p.NextURL != ""
	p.TotalPages = totalPages(root)
	if len(p.Works) == 0 && !p.HasMore {
		return ActorPage{}, fmt.Errorf("%s: provider page had no recognizable catalog entries", c.source)
	}
	p.Complete = !p.HasMore
	return p, nil
}

func parseJavDBCatalog(root *html.Node, base string, p *ActorPage) {
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" || !hasClass(n, "box") {
			return
		}
		code, href := strings.TrimSpace(videoCodeText(n)), attr(n, "href")
		if code == "" || href == "" {
			return
		}
		p.Works = append(p.Works, Metadata{Code: normalizeCode(code), Title: strings.TrimSpace(nodeText(n)), Source: "javdb", SourceURL: resolveURL(base, href), CoverURL: firstImage(n)})
	})
}
func parseJavLibraryCatalog(root *html.Node, base string, p *ActorPage) {
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" || !hasAncestorClass(n, "video") {
			return
		}
		code, href := strings.TrimSpace(firstClassText(n, "id")), attr(n, "href")
		if code == "" || href == "" {
			return
		}
		p.Works = append(p.Works, Metadata{Code: normalizeCode(code), Title: strings.TrimSpace(nodeText(n)), Source: "javlibrary", SourceURL: resolveURL(base, href), CoverURL: firstImage(n)})
	})
}
func parseFanzaCatalog(root *html.Node, base string, p *ActorPage) {
	seen := map[string]bool{}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		href := attr(n, "href")
		if !strings.Contains(strings.ToLower(href), "/detail/") {
			return
		}
		code := ""
		if m := codeRE.FindString(strings.TrimSpace(nodeText(n))); m != "" {
			code = normalizeCode(m)
		}
		if code == "" {
			h := strings.ToLower(href)
			if i := strings.Index(h, "cid="); i >= 0 {
				v := href[i+4:]
				if j := strings.IndexAny(v, "&/#"); j >= 0 {
					v = v[:j]
				}
				code = normalizeCode(v)
			}
		}
		if code == "" || seen[code] {
			return
		}
		seen[code] = true
		p.Works = append(p.Works, Metadata{Code: code, Title: strings.TrimSpace(nodeText(n)), Source: "fanza", SourceURL: resolveURL(base, href), CoverURL: firstImage(n)})
	})
}

func parseDetail(root *html.Node, code, source, sourceURL string) Metadata {
	m := Metadata{Code: code, Source: source, SourceURL: sourceURL}
	m.Title = firstClassText(root, "current-title")
	if m.Title == "" {
		m.Title = firstElementText(root, "h1")
	}
	m.Title = strings.TrimSpace(strings.TrimPrefix(m.Title, code))
	m.CoverURL = firstImageByID(root, "video_jacket_img")
	if m.CoverURL == "" {
		m.CoverURL = firstClassAttr(root, "video-cover", "src")
	}
	if m.CoverURL != "" {
		m.CoverURL = resolveURL(sourceURL, m.CoverURL)
	}
	labelMap := map[string]string{}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		label := ""
		if n.Data == "strong" || n.Data == "th" || n.Data == "td" {
			label = strings.TrimSpace(nodeText(n))
		}
		if label == "" {
			return
		}
		if strings.Contains(strings.ToLower(label), "actress") || strings.Contains(strings.ToLower(label), "performer") || strings.Contains(label, "演員") {
			m.Actors = append(m.Actors, anchorTexts(nextSiblingNode(n))...)
			return
		}
		v := nextSiblingText(n)
		if v != "" {
			labelMap[strings.Trim(label, " :：")] = v
		}
	})
	for k, v := range labelMap {
		switch {
		case strings.Contains(k, "日期") || strings.Contains(k, "発売日") || strings.Contains(k, "Date"):
			if len(v) >= 4 {
				if y, e := strconv.Atoi(v[:4]); e == nil {
					m.Year = y
				}
			}
		case strings.Contains(k, "演員") || strings.Contains(k, "actress") || strings.Contains(k, "performer"):
			m.Actors = splitNames(v)
		case strings.Contains(k, "類別") || strings.Contains(k, "ジャンル") || strings.Contains(k, "genre"):
			m.Tags = splitNames(v)
		}
	}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if hasClass(n, "star") || attr(n, "id") == "performer" {
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				if ch.Type == html.ElementNode && ch.Data == "a" {
					s := strings.TrimSpace(nodeText(ch))
					if s != "" {
						m.Actors = appendUnique(m.Actors, s)
					}
				}
			}
		}
		if hasClass(n, "score") || hasClass(n, "score-stars") {
			raw := strings.TrimSpace(nodeText(n))
			if source == "javdb" && hasClass(n, "score-stars") {
				raw = strings.TrimSpace(followingText(n))
			}
			setSourceRating(&m, raw, source)
		}
	})
	if source == "fanza" {
		parseFanzaRating(root, &m)
	}
	if m.Title == "" {
		m.Title = firstElementText(root, "title")
	}
	return m
}

var scoreRE = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)`)
var ratingCountRE = regexp.MustCompile(`(?i)(?:評論|評價|reviews?|votes?|ratings?)\s*[:：]?\s*([0-9][0-9,]*)|[（(]\s*([0-9][0-9,]*)\s*[）)]`)

func setSourceRating(m *Metadata, raw, source string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	m.RatingRaw = raw
	count := ratingCountRE.FindStringSubmatch(raw)
	if len(count) > 1 {
		for _, value := range count[1:] {
			if value != "" {
				m.RatingCountRaw = value
				m.RatingCount, _ = strconv.Atoi(strings.ReplaceAll(value, ",", ""))
				break
			}
		}
	}
	match := scoreRE.FindStringSubmatch(raw)
	if len(match) < 2 {
		return
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil {
		return
	}
	divisor := 1.0
	switch source {
	case "javdb":
		m.RatingScale = "0-5"
	case "javlibrary":
		m.RatingScale = "0-10"
		divisor = 2
	case "fanza":
		m.RatingScale = "0-50"
		divisor = 10
	default:
		m.RatingScale = "source"
	}
	normalized := f / divisor
	m.SourceRating = &f
	m.Rating = &normalized
}

func parseFanzaRating(root *html.Node, m *Metadata) {
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "td" || !strings.Contains(nodeText(n), "平均評価") {
			return
		}
		for cell := n.NextSibling; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode || cell.Data != "td" {
				continue
			}
			var raw string
			walk(cell, func(x *html.Node) {
				if x.Type == html.ElementNode && x.Data == "img" {
					src := attr(x, "src")
					base := src
					if i := strings.LastIndex(base, "/"); i >= 0 {
						base = base[i+1:]
					}
					if ext := strings.LastIndex(base, "."); ext >= 0 {
						base = base[:ext]
					}
					if _, err := strconv.Atoi(base); err == nil {
						raw = base
					}
				}
			})
			if raw != "" {
				setSourceRating(m, raw, "fanza")
			}
			break
		}
	})
}

func nextSiblingNode(n *html.Node) *html.Node {
	for x := n.NextSibling; x != nil; x = x.NextSibling {
		if x.Type == html.TextNode && strings.TrimSpace(x.Data) == "" {
			continue
		}
		return x
	}
	return nil
}
func anchorTexts(n *html.Node) []string {
	out := []string{}
	if n == nil {
		return out
	}
	walk(n, func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == "a" {
			text := strings.TrimSpace(nodeText(x))
			if text != "" {
				out = appendUnique(out, text)
			}
		}
	})
	return out
}
func followingText(n *html.Node) string {
	var b strings.Builder
	for x := n.NextSibling; x != nil; x = x.NextSibling {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		} else {
			break
		}
	}
	return b.String()
}
func videoCodeText(n *html.Node) string {
	var title *html.Node
	walk(n, func(x *html.Node) {
		if title == nil && x.Type == html.ElementNode && hasClass(x, "video-title") {
			title = x
		}
	})
	if title == nil {
		return ""
	}
	var code string
	walk(title, func(x *html.Node) {
		if code == "" && x.Type == html.ElementNode && x.Data == "strong" {
			code = strings.TrimSpace(nodeText(x))
		}
	})
	if code != "" {
		return code
	}
	return strings.TrimSpace(nodeText(title))
}

func splitNames(s string) []string {
	a := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '/' || r == '、' })
	out := []string{}
	for _, v := range a {
		v = strings.TrimSpace(v)
		if v != "" {
			out = appendUnique(out, v)
		}
	}
	return out
}
func appendUnique(a []string, s string) []string {
	for _, v := range a {
		if v == s {
			return a
		}
	}
	return append(a, s)
}
func resolveURL(base, ref string) string {
	u, e := url.Parse(ref)
	if e != nil {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ref
	}
	u = b.ResolveReference(u)
	if u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	return u.String()
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var f func(*html.Node)
	f = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func walk(n *html.Node, f func(*html.Node)) {
	f(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, f)
	}
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasClass(n *html.Node, class string) bool {
	for _, s := range strings.Fields(attr(n, "class")) {
		if s == class {
			return true
		}
	}
	return false
}
func hasAncestorClass(n *html.Node, class string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if hasClass(p, class) {
			return true
		}
	}
	return false
}
func firstClassText(n *html.Node, class string) string {
	var s string
	walk(n, func(x *html.Node) {
		if s == "" && x.Type == html.ElementNode && hasClass(x, class) {
			s = nodeText(x)
		}
	})
	return s
}
func firstElementText(n *html.Node, tag string) string {
	var s string
	walk(n, func(x *html.Node) {
		if s == "" && x.Type == html.ElementNode && x.Data == tag {
			s = nodeText(x)
		}
	})
	return s
}
func firstClassAttr(n *html.Node, class, key string) string {
	var s string
	walk(n, func(x *html.Node) {
		if s == "" && x.Type == html.ElementNode && hasClass(x, class) {
			s = attr(x, key)
		}
	})
	return s
}
func firstImage(n *html.Node) string {
	var s string
	walk(n, func(x *html.Node) {
		if s == "" && x.Type == html.ElementNode && x.Data == "img" {
			s = attr(x, "src")
			if s == "" {
				s = attr(x, "data-src")
			}
		}
	})
	return s
}
func firstImageByID(n *html.Node, id string) string {
	var s string
	walk(n, func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == "img" && attr(x, "id") == id {
			s = attr(x, "src")
		}
	})
	return s
}
func nextSiblingText(n *html.Node) string {
	for x := n.NextSibling; x != nil; x = x.NextSibling {
		if t := strings.TrimSpace(nodeText(x)); t != "" {
			return t
		}
	}
	return ""
}
func hasNextPage(n *html.Node) bool {
	found := false
	walk(n, func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == "a" && (attr(x, "rel") == "next" || hasClass(x, "next") || hasClass(x, "pagination-next")) {
			found = true
		}
	})
	return found
}
func nextPageURL(n *html.Node, base string) string {
	var next string
	walk(n, func(x *html.Node) {
		if next != "" || x.Type != html.ElementNode || x.Data != "a" {
			return
		}
		if attr(x, "rel") != "next" && !hasClass(x, "next") && !hasClass(x, "pagination-next") {
			return
		}
		if href := attr(x, "href"); href != "" {
			next = resolveURL(base, href)
		}
	})
	return next
}
func totalPages(n *html.Node) int {
	max := 0
	walk(n, func(x *html.Node) {
		if x.Type != html.ElementNode || x.Data != "a" || !hasClass(x, "pagination-link") {
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(nodeText(x)))
		if err == nil && v > max {
			max = v
		}
	})
	return max
}
