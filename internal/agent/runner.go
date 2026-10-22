package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"mediashelf/internal/integrations"
)

type Runner struct {
	Config   Config
	Client   *Client
	Logger   *log.Logger
	Interval time.Duration
}
type scanPayload struct {
	LibraryID string `json:"library_id"`
}
type toolPayload struct {
	ToolID string `json:"tool_id"`
}
type qbitPayload struct {
	RequestID    string `json:"request_id"`
	DownloaderID string `json:"downloader_id"`
	Operation    string `json:"operation"`
	Config       struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"config"`
	Params json.RawMessage `json:"params"`
}
type qbitParams struct {
	Category      string   `json:"category"`
	Action        string   `json:"action"`
	Hashes        []string `json:"hashes"`
	DeleteFiles   bool     `json:"delete_files"`
	URL           string   `json:"url"`
	SavePath      string   `json:"save_path"`
	Tags          string   `json:"tags"`
	Paused        bool     `json:"paused"`
	DownloadLimit int64    `json:"download_limit"`
	UploadLimit   int64    `json:"upload_limit"`
}

func NewRunner(c Config, logger *log.Logger) *Runner {
	return &Runner{Config: c, Client: NewClient(c), Logger: logger, Interval: 20 * time.Second}
}

// Run retries transient server failures with bounded exponential backoff. With
// once=true it performs one network cycle and returns its first error.
func (r *Runner) Run(ctx context.Context, once, scanAll bool) error {
	if !once {
		if err := r.Client.Heartbeat(); err != nil {
			r.logf("initial heartbeat: %v", err)
		}
		go r.heartbeatLoop(ctx)
	}
	lastScan := time.Time{}
	backoff := time.Second
	for {
		scanInterval := time.Duration(r.Config.ScanIntervalMinutes) * time.Minute
		if scanInterval <= 0 {
			scanInterval = 60 * time.Minute
		}
		dueScan := scanAll && lastScan.IsZero() || !once && (lastScan.IsZero() || time.Since(lastScan) >= scanInterval)
		if dueScan {
			err := r.scanConfigured(ctx)
			lastScan = time.Now()
			if err != nil {
				r.logf("library scan: %v", err)
				if once {
					return err
				}
			}
		}
		if once {
			if err := r.Client.Heartbeat(); err != nil {
				return err
			}
		}
		err := r.cycle(ctx)
		if err == nil {
			backoff = time.Second
		} else {
			r.logf("agent connection/work cycle: %v", err)
			if once {
				return err
			}
		}
		if once {
			return nil
		}
		wait := r.Interval
		if err != nil {
			wait = backoff
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (r *Runner) cycle(ctx context.Context) error {
	jobs, err := r.Client.Poll()
	if err != nil {
		return err
	}
	var remote *RemoteConfig
	for _, job := range jobs {
		if job.ID == "" {
			continue
		}
		status := "completed"
		result := "ok"
		if job.Type == "scan" || job.Type == "scan_library" {
			var p scanPayload
			if e := json.Unmarshal(job.Payload, &p); e != nil {
				status = "failed"
				result = e.Error()
			} else if e = r.scanLibrary(ctx, p.LibraryID, &remote); e != nil {
				status = "failed"
				result = e.Error()
			}
		} else if job.Type == "launch" || job.Type == "launch_tool" || job.Type == "tool_launch" {
			var p toolPayload
			if e := json.Unmarshal(job.Payload, &p); e != nil {
				status = "failed"
				result = e.Error()
			} else if e = r.launchAllowedTool(p.ToolID, &remote); e != nil {
				status = "failed"
				result = e.Error()
			}
		} else if job.Type == "qbit" {
			var p qbitPayload
			if e := json.Unmarshal(job.Payload, &p); e != nil {
				status = "failed"
				result = e.Error()
			} else {
				data, e := r.runQBit(ctx, p)
				if e != nil {
					status = "failed"
					result = marshalQBitResult(p.RequestID, e.Error())
				} else {
					result = marshalQBitResult(p.RequestID, data)
				}
			}
		} else {
			status = "failed"
			result = "unsupported job type"
		}
		if e := r.Client.Result(url.PathEscape(job.ID), status, result); e != nil {
			r.logf("report job %s: %v", job.ID, e)
		}
	}
	return nil
}

func (r *Runner) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Client.Heartbeat(); err != nil {
				r.logf("heartbeat: %v", err)
			}
		}
	}
}

func marshalQBitResult(requestID string, data any) string {
	b, e := json.Marshal(map[string]any{"request_id": requestID, "data": data})
	if e != nil {
		return `{"request_id":"","data":null}`
	}
	return string(b)
}

func (r *Runner) runQBit(ctx context.Context, p qbitPayload) (any, error) {
	allowlist, ok := r.Config.LocalDownloader(p.DownloaderID)
	if !ok {
		return nil, fmt.Errorf("downloader %q is not locally allowlisted", p.DownloaderID)
	}
	if !sameHTTPURL(allowlist.URL, p.Config.URL) {
		return nil, errors.New("job downloader URL does not match local allowlist")
	}
	if p.Config.Username == "" || p.Config.Password == "" {
		return nil, errors.New("qBittorrent credentials are required")
	}
	var params qbitParams
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &params); err != nil {
			return nil, fmt.Errorf("invalid qBittorrent parameters: %w", err)
		}
	}
	client, err := integrations.NewQBittorrent(integrations.QBConfig{URL: allowlist.URL, Username: p.Config.Username, Password: p.Config.Password})
	if err != nil {
		return nil, err
	}
	if err = client.Login(ctx); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(p.Operation)) {
	case "torrents", "list":
		return client.ListTorrents(ctx, params.Category)
	case "action":
		switch strings.ToLower(params.Action) {
		case "start":
			err = client.SetRunning(ctx, params.Hashes, true)
		case "stop":
			err = client.SetRunning(ctx, params.Hashes, false)
		case "delete":
			if params.DeleteFiles {
				return nil, errors.New("deleting torrent files is disabled in the device agent")
			}
			err = client.Delete(ctx, params.Hashes, false)
		case "category":
			err = client.SetCategory(ctx, params.Hashes, params.Category)
		case "tags":
			err = client.SetTags(ctx, params.Hashes, params.Tags)
		case "limits":
			err = client.SetLimits(ctx, params.Hashes, params.DownloadLimit, params.UploadLimit)
		default:
			return nil, fmt.Errorf("unsupported qBittorrent action %q", params.Action)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "add":
		if strings.TrimSpace(params.URL) == "" {
			return nil, errors.New("torrent URL is required")
		}
		if err = client.Add(ctx, integrations.AddTorrent{URL: params.URL, SavePath: params.SavePath, Category: params.Category, Tags: params.Tags, Paused: params.Paused}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fmt.Errorf("unsupported qBittorrent operation %q", p.Operation)
	}
}

func sameHTTPURL(a, b string) bool {
	ua, ea := url.Parse(strings.TrimSpace(a))
	ub, eb := url.Parse(strings.TrimSpace(b))
	if ea != nil || eb != nil || ua.User != nil || ub.User != nil {
		return false
	}
	if (ua.Scheme != "http" && ua.Scheme != "https") || ua.Scheme != ub.Scheme {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && strings.TrimRight(ua.EscapedPath(), "/") == strings.TrimRight(ub.EscapedPath(), "/") && ua.RawQuery == ub.RawQuery && ua.Fragment == ub.Fragment
}

func (r *Runner) remoteConfig(cache **RemoteConfig) (*RemoteConfig, error) {
	if *cache != nil {
		return *cache, nil
	}
	v, e := r.Client.RemoteConfig()
	if e != nil {
		return nil, e
	}
	*cache = &v
	return &v, nil
}

func (r *Runner) scanLibrary(ctx context.Context, id string, remote **RemoteConfig) error {
	root, ok := r.Config.LibraryRoot(id)
	if !ok {
		return fmt.Errorf("library %q is not locally allowlisted", id)
	}
	rc, err := r.remoteConfig(remote)
	if err != nil {
		return err
	}
	if !remoteHasLibrary(rc, id) {
		return fmt.Errorf("library %q is not present in server device config", id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s := ScanLibrary(id, root)
	if !s.Complete {
		_ = r.Client.Scan(id, false, []Work{})
		return s.Error()
	}
	for i := range s.Works {
		if s.Works[i].coverPath != "" {
			u, e := r.Client.UploadCover(s.Works[i].coverPath)
			if e == nil {
				s.Works[i].CoverURL = u
			} else {
				r.logf("cover upload for %s: %v", s.Works[i].Title, e)
			}
		}
		s.Works[i].coverPath = ""
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Client.Scan(id, true, s.Works); err != nil {
		return err
	}
	return nil
}

func (r *Runner) scanConfigured(ctx context.Context) error {
	rc, err := r.Client.RemoteConfig()
	if err != nil {
		return err
	}
	cached := &rc
	var first error
	for id := range r.Config.Libraries {
		if remoteHasLibrary(cached, id) {
			if e := r.scanLibrary(ctx, id, &cached); e != nil && first == nil {
				first = e
			}
		}
	}
	if first != nil {
		return first
	}
	return nil
}

func (r *Runner) launchAllowedTool(id string, remote **RemoteConfig) error {
	tool, ok := r.Config.LocalTool(id)
	if !ok {
		return fmt.Errorf("tool %q is not locally allowlisted", id)
	}
	rc, err := r.remoteConfig(remote)
	if err != nil {
		return err
	}
	var remoteTool *RemoteTool
	for i := range rc.Tools {
		if rc.Tools[i].ID == id {
			remoteTool = &rc.Tools[i]
			break
		}
	}
	if remoteTool == nil {
		return fmt.Errorf("tool %q is not registered for this device", id)
	}
	if !samePath(tool.Path, remoteTool.Path) {
		return errors.New("server tool path does not match local allowlist")
	}
	if tool.WorkingDir != "" && remoteTool.WorkingDir != "" && !samePath(tool.WorkingDir, remoteTool.WorkingDir) {
		return errors.New("server tool working_dir does not match local allowlist")
	}
	return launchTool(tool)
}

func remoteHasLibrary(c *RemoteConfig, id string) bool {
	for _, l := range c.Libraries {
		if l.ID == id {
			return true
		}
	}
	return false
}
func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	aa, e := filepath.Abs(a)
	if e != nil {
		return false
	}
	bb, e := filepath.Abs(b)
	if e != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
func (r *Runner) logf(f string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(f, args...)
	}
}
