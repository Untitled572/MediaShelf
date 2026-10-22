package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QBConfig configures a qBittorrent Web API endpoint. LAN endpoints are
// supported; callers should keep credentials in their local secret store.
type QBConfig struct{ URL, Username, Password string }

type QBittorrent struct {
	base               string
	username, password string
	client             *http.Client
	version            string
}

type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	Size     int64   `json:"size"`
	Category string  `json:"category"`
	Tags     string  `json:"tags"`
	SavePath string  `json:"save_path"`
	AutoTMM  bool    `json:"auto_tmm"`
	AddedOn  int64   `json:"added_on"`
	DLSpeed  int64   `json:"dlspeed"`
	UPSpeed  int64   `json:"upspeed"`
}

type AddTorrent struct {
	URL, SavePath, Category, Tags string
	Paused                        bool
	AutoTMM                       *bool
}

type Category struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

var errResponseTooLarge = errors.New("response exceeds 4 MiB limit")

const qbLoginFailureCooldown = 5 * time.Minute

var qbLoginFailures = struct {
	sync.Mutex
	entries map[string]time.Time
}{entries: make(map[string]time.Time)}

func NewQBittorrent(c QBConfig) (*QBittorrent, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.URL), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("qBittorrent URL must be an absolute HTTP(S) URL")
	}
	jar, _ := cookiejar.New(nil)
	return &QBittorrent{base: strings.TrimRight(c.URL, "/"), username: c.Username, password: c.Password, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}, nil
}

func (q *QBittorrent) endpoint(path string) string {
	return q.base + "/api/v2/" + strings.TrimLeft(path, "/")
}
func (q *QBittorrent) request(ctx context.Context, method, path string, values url.Values, out any) error {
	var body *strings.Reader
	if values == nil {
		values = url.Values{}
	}
	body = strings.NewReader(values.Encode())
	req, err := http.NewRequestWithContext(ctx, method, q.endpoint(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	origin := req.URL.Scheme + "://" + req.URL.Host
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qBittorrent API %s: HTTP %d", path, resp.StatusCode)
	}
	if path == "auth/login" {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if err != nil {
			return err
		}
		status := strings.TrimSpace(string(b))
		// qBittorrent 5.3 may return HTTP 200 with an empty body while still
		// establishing the session cookie. Login validates it with app/version.
		if status != "" && status != "Ok." {
			return fmt.Errorf("qBittorrent login failed: %s", status)
		}
		return nil
	}
	if out == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errResponseTooLarge
	}
	if text, ok := out.(*string); ok {
		*text = strings.TrimSpace(string(b))
		return nil
	}
	return json.Unmarshal(b, out)
}

func (q *QBittorrent) Login(ctx context.Context) error {
	// qBittorrent can be configured with WebUI authentication disabled. Probe
	// the unauthenticated version endpoint when no credentials were supplied so
	// read-only local/LAN endpoints can still be listed.
	if q.username == "" && q.password == "" {
		if _, err := q.Version(ctx); err == nil {
			return nil
		} else {
			return fmt.Errorf("qBittorrent credentials are required: anonymous version probe failed (%w)", err)
		}
	}
	key := q.loginFailureKey()
	if authFailureCoolingDown(key, time.Now()) {
		return errors.New("qBittorrent login temporarily suppressed after a recent authentication failure")
	}
	err := q.request(ctx, http.MethodPost, "auth/login", url.Values{"username": {q.username}, "password": {q.password}}, nil)
	var v string
	if err == nil {
		err = q.request(ctx, http.MethodGet, "app/version", nil, &v)
		if err != nil {
			err = fmt.Errorf("login succeeded but version query failed: %w", err)
		}
	}
	if err != nil {
		cacheAuthFailure(key, time.Now().Add(qbLoginFailureCooldown))
		return err
	}
	q.version = strings.TrimSpace(v)
	clearAuthFailure(key)
	return nil
}

func (q *QBittorrent) loginFailureKey() string {
	sum := sha256.Sum256([]byte(q.base + "\x00" + q.username + "\x00" + q.password))
	return hex.EncodeToString(sum[:])
}

func authFailureCoolingDown(key string, now time.Time) bool {
	qbLoginFailures.Lock()
	defer qbLoginFailures.Unlock()
	until, ok := qbLoginFailures.entries[key]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(qbLoginFailures.entries, key)
		return false
	}
	return true
}

func cacheAuthFailure(key string, until time.Time) {
	qbLoginFailures.Lock()
	defer qbLoginFailures.Unlock()
	now := time.Now()
	for k, expiry := range qbLoginFailures.entries {
		if !now.Before(expiry) {
			delete(qbLoginFailures.entries, k)
		}
	}
	qbLoginFailures.entries[key] = until
}

func clearAuthFailure(key string) {
	qbLoginFailures.Lock()
	delete(qbLoginFailures.entries, key)
	qbLoginFailures.Unlock()
}
func (q *QBittorrent) Version(ctx context.Context) (string, error) {
	var v string
	err := q.request(ctx, http.MethodGet, "app/version", nil, &v)
	if err == nil {
		q.version = strings.TrimSpace(v)
	}
	return q.version, err
}
func (q *QBittorrent) apiVersion(ctx context.Context) (int, error) {
	if q.version == "" {
		if _, err := q.Version(ctx); err != nil {
			return 0, err
		}
	}
	s := strings.Split(strings.TrimPrefix(strings.TrimSpace(q.version), "v"), ".")
	if len(s) < 2 {
		return 0, fmt.Errorf("unrecognized qBittorrent version %q", q.version)
	}
	major, e1 := strconv.Atoi(s[0])
	minor, e2 := strconv.Atoi(s[1])
	if e1 != nil || e2 != nil {
		return 0, fmt.Errorf("unrecognized qBittorrent version %q", q.version)
	}
	return major*100 + minor, nil
}
func (q *QBittorrent) ListTorrents(ctx context.Context, category string) ([]Torrent, error) {
	const initialPageSize = 250
	const maxTorrents = 50000
	all := make([]Torrent, 0)
	offset, pageSize := 0, initialPageSize
	for offset < maxTorrents {
		v := url.Values{}
		v.Set("limit", strconv.Itoa(pageSize))
		v.Set("offset", strconv.Itoa(offset))
		v.Set("sort", "hash")
		v.Set("reverse", "false")
		if category != "" {
			v.Set("category", category)
		}
		var page []Torrent
		if err := q.request(ctx, http.MethodGet, "torrents/info?"+v.Encode(), nil, &page); err != nil {
			if errors.Is(err, errResponseTooLarge) {
				if pageSize == 1 {
					return nil, errors.New("qBittorrent returned more than 4 MiB for one torrent; cannot list safely")
				}
				pageSize /= 2
				continue
			}
			return nil, err
		}
		all = append(all, page...)
		offset += len(page)
		if len(page) < pageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("qBittorrent torrent list exceeds the %d-item safety limit", maxTorrents)
}

func (q *QBittorrent) ListCategories(ctx context.Context) (map[string]Category, error) {
	var categories map[string]Category
	if err := q.request(ctx, http.MethodGet, "torrents/categories", nil, &categories); err != nil {
		return nil, err
	}
	if categories == nil {
		categories = map[string]Category{}
	}
	return categories, nil
}

func (q *QBittorrent) Add(ctx context.Context, a AddTorrent) error {
	v := url.Values{"urls": {a.URL}}
	if a.AutoTMM == nil || !*a.AutoTMM {
		if a.SavePath != "" {
			v.Set("savepath", a.SavePath)
		}
	}
	if a.AutoTMM != nil {
		v.Set("autoTMM", strconv.FormatBool(*a.AutoTMM))
	}
	if a.Category != "" {
		v.Set("category", a.Category)
	}
	if a.Tags != "" {
		v.Set("tags", a.Tags)
	}
	if a.Paused {
		v.Set("paused", "true")
		v.Set("stopped", "true")
	}
	var status string
	if err := q.request(ctx, http.MethodPost, "torrents/add", v, &status); err != nil {
		return err
	}
	if status != "" && status != "Ok." {
		var result struct {
			Success int `json:"success_count"`
			Pending int `json:"pending_count"`
			Failure int `json:"failure_count"`
		}
		if json.Unmarshal([]byte(status), &result) != nil || result.Failure != 0 || result.Success+result.Pending < 1 {
			return fmt.Errorf("qBittorrent add failed: %s", status)
		}
	}
	return nil
}
func hashesValue(h []string) (string, error) {
	if len(h) == 0 {
		return "", errors.New("at least one torrent hash is required")
	}
	for _, s := range h {
		if strings.ContainsAny(s, "|\r\n") {
			return "", errors.New("invalid torrent hash")
		}
	}
	return strings.Join(h, "|"), nil
}
func (q *QBittorrent) Delete(ctx context.Context, hashes []string, deleteFiles bool) error {
	hs, e := hashesValue(hashes)
	if e != nil {
		return e
	}
	d := "false"
	if deleteFiles {
		d = "true"
	}
	return q.request(ctx, http.MethodPost, "torrents/delete", url.Values{"hashes": {hs}, "deleteFiles": {d}}, nil)
}
func (q *QBittorrent) SetRunning(ctx context.Context, hashes []string, start bool) error {
	hs, e := hashesValue(hashes)
	if e != nil {
		return e
	}
	ver, e := q.apiVersion(ctx)
	if e != nil {
		return e
	}
	action := "stop"
	if ver < 500 {
		action = "pause"
	}
	if start {
		action = "start"
		if ver < 500 {
			action = "resume"
		}
	}
	return q.request(ctx, http.MethodPost, "torrents/"+action, url.Values{"hashes": {hs}}, nil)
}
func (q *QBittorrent) SetCategory(ctx context.Context, hashes []string, category string) error {
	hs, e := hashesValue(hashes)
	if e != nil {
		return e
	}
	return q.request(ctx, http.MethodPost, "torrents/setCategory", url.Values{"hashes": {hs}, "category": {category}}, nil)
}
func (q *QBittorrent) SetTags(ctx context.Context, hashes []string, tags string) error {
	hs, e := hashesValue(hashes)
	if e != nil {
		return e
	}
	return q.request(ctx, http.MethodPost, "torrents/addTags", url.Values{"hashes": {hs}, "tags": {tags}}, nil)
}
func (q *QBittorrent) SetLimits(ctx context.Context, hashes []string, download, upload int64) error {
	_, e := hashesValue(hashes)
	if e != nil {
		return e
	}
	if download < 0 || upload < 0 {
		return errors.New("limits must be non-negative bytes per second")
	}
	for _, h := range hashes {
		if e = q.request(ctx, http.MethodPost, "torrents/setDownloadLimit", url.Values{"hashes": {h}, "limit": {strconv.FormatInt(download, 10)}}, nil); e != nil {
			return e
		}
		if e = q.request(ctx, http.MethodPost, "torrents/setUploadLimit", url.Values{"hashes": {h}, "limit": {strconv.FormatInt(upload, 10)}}, nil); e != nil {
			return e
		}
	}
	return nil
}
