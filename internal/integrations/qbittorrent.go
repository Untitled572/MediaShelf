package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
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
	AddedOn  int64   `json:"added_on"`
	DLSpeed  int64   `json:"dlspeed"`
	UPSpeed  int64   `json:"upspeed"`
}

type AddTorrent struct {
	URL, SavePath, Category, Tags string
	Paused                        bool
}

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
		if status != "Ok." {
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
		return errors.New("response exceeds 4 MiB limit")
	}
	if text, ok := out.(*string); ok {
		*text = strings.TrimSpace(string(b))
		return nil
	}
	return json.Unmarshal(b, out)
}

func (q *QBittorrent) Login(ctx context.Context) error {
	if err := q.request(ctx, http.MethodPost, "auth/login", url.Values{"username": {q.username}, "password": {q.password}}, nil); err != nil {
		return err
	}
	var v string
	if err := q.request(ctx, http.MethodGet, "app/version", nil, &v); err != nil {
		return fmt.Errorf("login succeeded but version query failed: %w", err)
	}
	q.version = strings.TrimSpace(v)
	return nil
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
	s := strings.Split(q.version, ".")
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
	v := url.Values{}
	if category != "" {
		v.Set("category", category)
	}
	var a []Torrent
	err := q.request(ctx, http.MethodGet, "torrents/info?"+v.Encode(), nil, &a)
	return a, err
}
func (q *QBittorrent) Add(ctx context.Context, a AddTorrent) error {
	v := url.Values{"urls": {a.URL}}
	if a.SavePath != "" {
		v.Set("savepath", a.SavePath)
	}
	if a.Category != "" {
		v.Set("category", a.Category)
	}
	if a.Tags != "" {
		v.Set("tags", a.Tags)
	}
	if a.Paused {
		v.Set("paused", "true")
	}
	return q.request(ctx, http.MethodPost, "torrents/add", v, nil)
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
