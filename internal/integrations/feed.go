package integrations

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type FeedRules struct{ Include, Exclude []string }
type FeedItem struct {
	Code        string    `json:"code"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Source      string    `json:"source"`
	PublishedAt time.Time `json:"published_at,omitempty"`
}
type xmlFeed struct {
	Channel struct {
		Items []xmlItem `xml:"item"`
	} `xml:"channel"`
	Items []xmlItem `xml:"entry"`
}
type feedLink struct {
	Text string `xml:",chardata"`
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}
type xmlItem struct {
	Title     string     `xml:"title"`
	Links     []feedLink `xml:"link"`
	GUID      string     `xml:"guid"`
	ID        string     `xml:"id"`
	PubDate   string     `xml:"pubDate"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
}

var codeRE = regexp.MustCompile(`(?i)([a-z]{2,12}[-_ ]?\d{2,6}[a-z]?(?:[-_]\d{2,4})?)|((?:fc2[-_ ]?ppv[-_ ]?)?\d{5,10})`)

func ParseFeed(raw []byte, rules FeedRules) ([]FeedItem, error) {
	if len(raw) > 2<<20 {
		return nil, errors.New("feed response exceeds 2 MiB limit")
	}
	var f xmlFeed
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	items := f.Channel.Items
	if len(items) == 0 {
		items = f.Items
	}
	inc, err := compileRules(rules.Include)
	if err != nil {
		return nil, err
	}
	exc, err := compileRules(rules.Exclude)
	if err != nil {
		return nil, err
	}
	out := make([]FeedItem, 0, len(items))
	for _, it := range items {
		title := strings.TrimSpace(it.Title)
		link := ""
		for _, l := range it.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = firstNonEmpty(l.Href, l.Text)
				break
			}
		}
		link = strings.TrimSpace(link)
		if title == "" && link == "" {
			continue
		}
		text := title + " " + link
		if len(inc) > 0 && !matchesAny(inc, text) {
			continue
		}
		if matchesAny(exc, text) {
			continue
		}
		code := ""
		if m := codeRE.FindStringSubmatch(title); m != nil {
			for _, v := range m[1:] {
				if v != "" {
					code = normalizeCode(v)
					break
				}
			}
		}
		if code == "" {
			if m := codeRE.FindStringSubmatch(link); m != nil {
				for _, v := range m[1:] {
					if v != "" {
						code = normalizeCode(v)
						break
					}
				}
			}
		}
		if code == "" {
			continue
		}
		published := firstNonEmpty(it.PubDate, it.Published, it.Updated)
		t := parseFeedTime(published)
		u, err := url.Parse(link)
		if err == nil && u.IsAbs() && (u.Scheme == "http" || u.Scheme == "https") {
			out = append(out, FeedItem{Code: code, Title: title, URL: u.String(), Source: u.Host, PublishedAt: t})
		} else if link == "" {
			out = append(out, FeedItem{Code: code, Title: title})
		}
	}
	return out, nil
}

// FetchFeed retrieves a public RSS/Atom URL with bounded time, redirects, and body size.
func FetchFeed(ctx context.Context, rawURL string, rules FeedRules, proxyURLs ...string) ([]FeedItem, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err = validateExternalURL(u); err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: safeExternalDial}
	if len(proxyURLs) > 0 && strings.TrimSpace(proxyURLs[0]) != "" {
		proxy, err := url.Parse(strings.TrimSpace(proxyURLs[0]))
		if err != nil || proxy.Host == "" || proxy.User != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.RawQuery != "" || proxy.Fragment != "" {
			return nil, errors.New("invalid feed proxy URL")
		}
		transport.Proxy = http.ProxyURL(proxy)
		transport.DialContext = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many feed redirects")
		}
		return validateExternalURL(req.URL)
	}}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2<<20 {
		return nil, errors.New("feed response exceeds 2 MiB limit")
	}
	return ParseFeed(body, rules)
}
func compileRules(r []string) ([]*regexp.Regexp, error) {
	a := make([]*regexp.Regexp, 0, len(r))
	for _, s := range r {
		if strings.TrimSpace(s) == "" {
			continue
		}
		x, e := regexp.Compile(s)
		if e != nil {
			return nil, e
		}
		a = append(a, x)
	}
	return a, nil
}
func matchesAny(r []*regexp.Regexp, s string) bool {
	for _, x := range r {
		if x.MatchString(s) {
			return true
		}
	}
	return false
}
func normalizeCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
func parseFeedTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, "Mon, 2 Jan 2006 15:04:05 MST"} {
		if t, e := time.Parse(layout, s); e == nil {
			return t
		}
	}
	return time.Time{}
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
