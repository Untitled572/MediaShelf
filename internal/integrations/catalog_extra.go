package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func (c *CatalogClient) lookupJavBus(ctx context.Context, code string) (Metadata, error) {
	root, final, err := c.fetch(ctx, c.base+"/"+url.PathEscape(code))
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	m = parseDetail(root, code, "javbus", final)
	if container := firstClassNode(root, "container"); container != nil {
		if title := firstElementText(container, "h3"); title != "" {
			m.Title = strings.TrimSpace(strings.TrimPrefix(title, code))
		}
	}
	if image := firstDescendantAttr(root, "bigImage", "img", "src"); image != "" {
		m.CoverURL = resolveURL(final, image)
	}
	walk(root, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasAncestorClass(n, "genre") {
			if tag := strings.TrimSpace(nodeText(n)); tag != "" {
				m.Tags = appendUnique(m.Tags, tag)
			}
		}
		if n.Type == html.ElementNode && n.Data == "span" && strings.Contains(nodeText(n), "發行日期") {
			if value := nextSiblingText(n); len(value) >= 4 {
				if year, e := strconv.Atoi(value[:4]); e == nil {
					m.Year = year
				}
			}
		}
	})
	walk(root, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "avatar-box") {
			walk(n, func(x *html.Node) {
				if x.Type == html.ElementNode && x.Data == "img" {
					if name := strings.TrimSpace(attr(x, "title")); name != "" {
						m.Actors = appendUnique(m.Actors, name)
					}
				}
			})
		}
	})
	if m.Title == "" {
		return Metadata{}, fmt.Errorf("javbus: code %s page had no recognizable title", code)
	}
	return m, nil
}

// Jav321 accepts a form POST and returns its search result/detail in one HTML
// document. Keep the same URL, response-size, redirect, and challenge checks
// as fetch; this intentionally does not invoke browser verification.
func (c *CatalogClient) fetchForm(ctx context.Context, raw string, form url.Values) (*html.Node, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	if !u.IsAbs() {
		base, _ := url.Parse(c.base)
		u = base.ResolveReference(u)
	}
	if err = validateExternalURL(u); err != nil {
		return nil, "", err
	}
	if c.proxyConfigured {
		if err = validateExternalHost(ctx, u.Hostname()); err != nil {
			return nil, "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "MediaShelf/1.0 (+metadata lookup)")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7,ja;q=0.5")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > 4<<20 {
		return nil, "", errors.New("provider response exceeds 4 MiB limit")
	}
	if hasBrowserVerificationChallenge(body) {
		return nil, "", fmt.Errorf("%s provider requires browser verification; data source is restricted", c.source)
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, "", err
	}
	return root, resp.Request.URL.String(), nil
}

func (c *CatalogClient) lookupJav321(ctx context.Context, code string) (Metadata, error) {
	root, final, err := c.fetchForm(ctx, c.base+"/search", url.Values{"sn": {code}})
	if err != nil {
		return Metadata{}, err
	}
	var detail string
	walk(root, func(n *html.Node) {
		if detail != "" || n.Type != html.ElementNode || n.Data != "a" || !hasAncestorClass(n, "dropdown-menu") {
			return
		}
		href := attr(n, "href")
		if href != "" && !strings.Contains(strings.ToLower(href), "/search") {
			detail = href
		}
	})
	if detail == "" {
		return Metadata{}, fmt.Errorf("jav321: code %s not found", code)
	}
	pageURL := resolveURL(final, detail)
	if pageURL == "" {
		return Metadata{}, errors.New("jav321: result contained an invalid detail URL")
	}
	if actual := normalizeCode(codeRE.FindString(siblingValueForBold(root, "品番"))); actual != "" && actual != normalizeCode(code) {
		return Metadata{}, fmt.Errorf("jav321: returned code %s does not match %s", actual, code)
	}
	// Search results include the recognized work details in the same response.
	m := parseDetail(root, code, "jav321", pageURL)
	if heading := firstClassNode(root, "panel-heading"); heading != nil {
		if title := firstElementText(heading, "h3"); title != "" {
			m.Title = strings.TrimSpace(title)
		}
	}
	if m.Title == "" {
		return Metadata{}, fmt.Errorf("jav321: code %s result had no recognizable title", code)
	}
	for _, label := range []string{"発売日", "配信開始日"} {
		if value := siblingValueForBold(root, label); len(value) >= 4 {
			if year, e := strconv.Atoi(value[:4]); e == nil {
				m.Year = year
				break
			}
		}
	}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.Data == "a" {
			href := attr(n, "href")
			text := strings.TrimSpace(nodeText(n))
			if strings.Contains(href, "/genre/") && text != "" {
				m.Tags = appendUnique(m.Tags, text)
			}
		}
		if n.Data == "img" && hasAncestorClass(n, "thumbnail") {
			for parent := n.Parent; parent != nil; parent = parent.Parent {
				if parent.Type == html.ElementNode && parent.Data == "a" && strings.Contains(attr(parent, "href"), "/star/") {
					if name := strings.TrimSpace(attr(n, "alt")); name != "" {
						m.Actors = appendUnique(m.Actors, name)
					}
					break
				}
			}
		}
	})
	if cover := firstAttrByClass(root, "img-responsive", "src"); cover != "" {
		m.CoverURL = resolveURL(pageURL, cover)
	}
	return m, nil
}

func parseJavBusCatalog(root *html.Node, base string, p *ActorPage) {
	seen := map[string]bool{}
	walk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" || !hasClass(n, "movie-box") {
			return
		}
		href := attr(n, "href")
		text := strings.TrimSpace(nodeText(n))
		code := normalizeCode(codeRE.FindString(text))
		if code == "" || href == "" || seen[code] {
			return
		}
		seen[code] = true
		p.Works = append(p.Works, Metadata{Code: code, Title: strings.TrimSpace(strings.TrimPrefix(text, code)), Source: "javbus", SourceURL: resolveURL(base, href), CoverURL: resolveURL(base, firstImage(n))})
	})
}

func firstClassNode(root *html.Node, class string) *html.Node {
	var found *html.Node
	walk(root, func(n *html.Node) {
		if found == nil && n.Type == html.ElementNode && hasClass(n, class) {
			found = n
		}
	})
	return found
}

func firstAttrByClass(root *html.Node, class, key string) string {
	var found string
	walk(root, func(n *html.Node) {
		if found == "" && n.Type == html.ElementNode && hasClass(n, class) {
			found = attr(n, key)
		}
	})
	return found
}

func siblingValueForBold(root *html.Node, label string) string {
	var value string
	walk(root, func(n *html.Node) {
		if value == "" && n.Type == html.ElementNode && n.Data == "b" && strings.TrimSpace(nodeText(n)) == label {
			value = nextSiblingText(n)
		}
	})
	return strings.TrimLeft(strings.TrimSpace(value), ":： ")
}

func firstDescendantAttr(root *html.Node, parentClass, tag, key string) string {
	var found string
	walk(root, func(n *html.Node) {
		if found == "" && n.Type == html.ElementNode && n.Data == tag && hasAncestorClass(n, parentClass) {
			found = attr(n, key)
		}
	})
	return found
}
