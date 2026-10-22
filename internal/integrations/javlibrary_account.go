package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const maxJavLibraryAccountPages = 20

// JavLibraryAccountClient reads user-supplied JavLibrary list URLs. It does
// not submit forms or change remote account state.
type JavLibraryAccountClient struct {
	base            *url.URL
	cookie          string
	client          *http.Client
	proxyConfigured bool
}

// JavLibraryAccountItem is a movie list entry recognized on a JavLibrary page.
type JavLibraryAccountItem struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	SourceURL string `json:"source_url,omitempty"`
}

// ValidateJavLibraryAccountListURL ensures a user-supplied list URL stays on
// the configured JavLibrary origin. DNS/private-address checks are repeated
// immediately before each request by the client.
func ValidateJavLibraryAccountListURL(base, raw string) error {
	client, err := NewJavLibraryAccountClient(CatalogConfig{Source: "javlibrary", BaseURL: base})
	if err != nil {
		return err
	}
	_, err = client.validateListURL(raw)
	return err
}

// NewJavLibraryAccountClient creates a read-only scraper for account list
// pages. BaseURL is the configured JavLibrary origin, Cookie is kept only in
// memory by the caller, and ProxyURL is the shared outbound proxy if enabled.
func NewJavLibraryAccountClient(c CatalogConfig) (*JavLibraryAccountClient, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = "https://www.javlibrary.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("JavLibrary base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	if err := validateExternalURL(u); err != nil {
		return nil, err
	}
	cookie := strings.TrimSpace(c.Cookie)
	if len(cookie) > 8192 || strings.ContainsAny(cookie, "\r\n") {
		return nil, errors.New("JavLibrary cookie is invalid or exceeds 8 KiB")
	}
	transport := &http.Transport{DialContext: safeExternalDial}
	if strings.TrimSpace(c.ProxyURL) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(c.ProxyURL))
		if err != nil || proxyURL.Host == "" || proxyURL.User != nil || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
			return nil, errors.New("invalid JavLibrary account proxy URL")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		transport.DialContext = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	return &JavLibraryAccountClient{
		base:            u,
		cookie:          cookie,
		proxyConfigured: strings.TrimSpace(c.ProxyURL) != "",
		client: &http.Client{
			Transport: transport,
			Timeout:   12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many JavLibrary redirects")
				}
				if !sameOrigin(req.URL, u) {
					return errors.New("JavLibrary account page redirected outside the configured origin")
				}
				return validateExternalURL(req.URL)
			},
		},
	}, nil
}

// ImportMovies follows only next links present in the returned HTML and stops
// after maxPages (capped at 20). The input URL must share the configured origin.
func (c *JavLibraryAccountClient) ImportMovies(ctx context.Context, rawURL string, maxPages int) ([]JavLibraryAccountItem, error) {
	if maxPages < 1 {
		maxPages = 1
	}
	if maxPages > maxJavLibraryAccountPages {
		maxPages = maxJavLibraryAccountPages
	}
	current, err := c.validateListURL(rawURL)
	if err != nil {
		return nil, err
	}
	items := []JavLibraryAccountItem{}
	seenPages, seenCodes := map[string]bool{}, map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if seenPages[current.String()] {
			return nil, errors.New("JavLibrary pagination repeated a page URL")
		}
		seenPages[current.String()] = true
		root, finalURL, err := c.fetchPage(ctx, current)
		if err != nil {
			return nil, err
		}
		pageEntries := ActorPage{Works: []Metadata{}}
		parseJavLibraryCatalog(root, finalURL, &pageEntries)
		for _, entry := range pageEntries.Works {
			code := normalizeCode(entry.Code)
			if code == "" || seenCodes[code] {
				continue
			}
			seenCodes[code] = true
			items = append(items, JavLibraryAccountItem{Code: code, Title: strings.TrimSpace(entry.Title), SourceURL: entry.SourceURL})
		}
		next := nextPageURL(root, finalURL)
		if next == "" {
			break
		}
		current, err = c.validateListURL(next)
		if err != nil {
			return nil, fmt.Errorf("JavLibrary next page rejected: %w", err)
		}
		if page == maxPages-1 {
			return nil, fmt.Errorf("JavLibrary list exceeds the %d-page safety limit", maxPages)
		}
	}
	if len(items) == 0 {
		return nil, errors.New("JavLibrary page contained no recognizable movie entries; check the list URL, session, and page markup")
	}
	return items, nil
}

// ImportFavoriteActors extracts actor links from user-supplied pages. The page
// must contain the same .star / #performer markup used by JavLibrary movie
// details and actor links observed by the existing JavLibrary adapter.
func (c *JavLibraryAccountClient) ImportFavoriteActors(ctx context.Context, rawURL string, maxPages int) ([]string, error) {
	if maxPages < 1 {
		maxPages = 1
	}
	if maxPages > maxJavLibraryAccountPages {
		maxPages = maxJavLibraryAccountPages
	}
	current, err := c.validateListURL(rawURL)
	if err != nil {
		return nil, err
	}
	actors, seenPages := []string{}, map[string]bool{}
	seenActors := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if seenPages[current.String()] {
			return nil, errors.New("JavLibrary pagination repeated a page URL")
		}
		seenPages[current.String()] = true
		root, finalURL, err := c.fetchPage(ctx, current)
		if err != nil {
			return nil, err
		}
		for _, name := range parseJavLibraryFavoriteActors(root) {
			key := strings.ToLower(strings.Join(strings.Fields(name), " "))
			if key != "" && !seenActors[key] {
				seenActors[key] = true
				actors = append(actors, name)
			}
		}
		next := nextPageURL(root, finalURL)
		if next == "" {
			break
		}
		current, err = c.validateListURL(next)
		if err != nil {
			return nil, fmt.Errorf("JavLibrary next page rejected: %w", err)
		}
		if page == maxPages-1 {
			return nil, fmt.Errorf("JavLibrary actor list exceeds the %d-page safety limit", maxPages)
		}
	}
	if len(actors) == 0 {
		return nil, errors.New("JavLibrary page contained no recognizable actor entries; check the list URL, session, and page markup")
	}
	return actors, nil
}

func (c *JavLibraryAccountClient) validateListURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !u.IsAbs() || u.User != nil || u.Fragment != "" {
		return nil, errors.New("JavLibrary list URL must be absolute HTTP(S) without credentials or fragment")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("JavLibrary list URL must use HTTP(S)")
	}
	if err := validateExternalURL(u); err != nil {
		return nil, err
	}
	if !sameOrigin(u, c.base) {
		return nil, errors.New("JavLibrary list URL must match the configured JavLibrary origin")
	}
	return u, nil
}

func (c *JavLibraryAccountClient) fetchPage(ctx context.Context, u *url.URL) (*html.Node, string, error) {
	if c.proxyConfigured {
		ctxCheck, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := validateExternalHost(ctxCheck, u.Hostname())
		cancel()
		if err != nil {
			return nil, "", fmt.Errorf("JavLibrary host validation failed: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "MediaShelf/1.0 (+read-only account list import)")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7,ja;q=0.5")
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("JavLibrary account page request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, "", fmt.Errorf("JavLibrary account page denied access (HTTP %d); session may be invalid or access may require browser verification", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("JavLibrary account page returned HTTP %d", resp.StatusCode)
	}
	if !sameOrigin(resp.Request.URL, c.base) {
		return nil, "", errors.New("JavLibrary account page redirected outside the configured origin")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > 4<<20 {
		return nil, "", errors.New("JavLibrary account page exceeds 4 MiB")
	}
	if hasBrowserVerificationChallenge(body) {
		return nil, "", errors.New("JavLibrary requires browser verification; account import is unavailable")
	}
	if loginPageURL(resp.Request.URL) {
		return nil, "", errors.New("JavLibrary session expired or the URL redirected to a login page")
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, "", err
	}
	return root, resp.Request.URL.String(), nil
}

func loginPageURL(u *url.URL) bool {
	path := strings.ToLower(u.Path)
	return strings.Contains(path, "login") || strings.Contains(path, "signin") || strings.Contains(path, "sign_in")
}

func parseJavLibraryFavoriteActors(root *html.Node) []string {
	actors := []string{}
	seen := map[string]bool{}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" || attr(n, "href") == "" {
			return
		}
		href := strings.ToLower(attr(n, "href"))
		if !strings.Contains(href, "vl_star.php") || (!hasAncestorClass(n, "star") && !hasAncestorID(n, "performer")) {
			return
		}
		name := strings.TrimSpace(nodeText(n))
		if name == "" {
			return
		}
		key := strings.ToLower(strings.Join(strings.Fields(name), " "))
		if !seen[key] {
			seen[key] = true
			actors = append(actors, name)
		}
	})
	return actors
}

func hasAncestorID(n *html.Node, id string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if attr(p, "id") == id {
			return true
		}
	}
	return false
}
