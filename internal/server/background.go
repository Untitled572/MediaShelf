package server

import (
	"context"
	"mediashelf/internal/integrations"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Background discovers RSS candidates only. It never adds a download or runs a processing tool.
func (a *App) Background(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshDueFeeds(ctx)
		}
	}
}
func (a *App) refreshDueFeeds(ctx context.Context) {
	feeds, e := a.list("feeds")
	if e != nil {
		return
	}
	for _, f := range feeds {
		if disabled, exists := f["enabled"]; exists && !flag(disabled) {
			continue
		}
		last, _ := time.Parse(time.RFC3339, str(f["last_attempt"]))
		if time.Since(last) < time.Hour {
			continue
		}
		f["last_attempt"] = now()
		_ = a.put("feeds", f)
		rules := integrations.FeedRules{}
		if s := str(f["include"]); s != "" {
			rules.Include = []string{s}
		}
		if s := str(f["exclude"]); s != "" {
			rules.Exclude = []string{s}
		}
		proxyURL, e := a.outboundProxy()
		if e == nil {
			items, fetchErr := integrations.FetchFeed(ctx, str(f["url"]), rules, proxyURL)
			if fetchErr != nil {
				e = fetchErr
			} else {
				for _, it := range items {
					o := toObject(it)
					o["id"] = digest(it.URL)
					o["source"] = f["name"]
					o["status"] = "new"
					if old, _ := a.get("candidates", str(o["id"])); old != nil {
						continue
					}
					_ = a.put("candidates", o)
				}
				f["last_refresh"] = now()
				f["last_error"] = ""
			}
		}
		if e != nil {
			f["last_error"] = e.Error()
		}
		_ = a.put("feeds", f)
	}
}
func queueDuplicate(torrents any, rawURL, code string) bool {
	xs := asObjects(torrents)
	hash := magnetHash(rawURL)
	for _, x := range xs {
		if hash != "" && equalCode(str(x["hash"]), hash) {
			return true
		}
		if sameWorkCode(extractCode(str(x["name"])), code) {
			return true
		}
	}
	return false
}
func magnetHash(s string) string {
	u, e := url.Parse(s)
	if e != nil {
		return ""
	}
	return strings.TrimPrefix(u.Query().Get("xt"), "urn:btih:")
}
func equalCode(a, b string) bool {
	return strings.EqualFold(strings.ReplaceAll(a, "_", "-"), strings.ReplaceAll(b, "_", "-"))
}

var workCode = regexp.MustCompile(`(?i)(?:FC2[-_ ]?(?:PPV[-_ ]?)?[0-9]{4,10}|[A-Z]{2,10}[-_ ]?[0-9]{2,8})`)
var canonicalWorkCode = regexp.MustCompile(`(?i)^(?:FC2[-_ ]?(?:PPV[-_ ]?)?[0-9]{4,10}|[A-Z]{2,10}[-_ ]?[0-9]{2,8})$`)

// sameWorkCode folds formatting only for code families we recognize. Unknown
// identifiers retain exact (case-insensitive) comparison to avoid merging
// unrelated values by stripping punctuation indiscriminately.
func sameWorkCode(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	a, aKnown := normalizeWorkCode(a)
	b, bKnown := normalizeWorkCode(b)
	if aKnown && bKnown {
		return a == b
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func normalizeWorkCode(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if !canonicalWorkCode.MatchString(code) {
		return code, false
	}
	code = strings.ToUpper(code)
	code = strings.NewReplacer("-", "", "_", "", " ", "").Replace(code)
	return code, true
}

func extractCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(workCode.FindString(s), "_", "-"))
}
