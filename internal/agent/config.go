package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config is the device-local trust boundary. The server may request work only
// against these explicitly configured library and tool IDs.
type Config struct {
	ServerURL           string                `json:"server_url"`
	Token               string                `json:"token"`
	Libraries           map[string]string     `json:"libraries"`
	Tools               map[string]Tool       `json:"tools"`
	Downloaders         map[string]Downloader `json:"downloaders,omitempty"`
	ScanIntervalMinutes int                   `json:"scan_interval_minutes,omitempty"`
}

type Tool struct {
	Path       string `json:"path"`
	WorkingDir string `json:"working_dir,omitempty"`
}

type Downloader struct {
	URL string `json:"url"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	u, err := url.Parse(strings.TrimSpace(c.ServerURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("server_url must be an absolute http(s) URL")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token is required")
	}
	if len(c.Libraries) == 0 {
		return errors.New("at least one local library allowlist entry is required")
	}
	if c.ScanIntervalMinutes < 0 {
		return errors.New("scan_interval_minutes cannot be negative")
	}
	for id, root := range c.Libraries {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(root) == "" {
			return errors.New("library IDs and roots must be non-empty")
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("library %q root must be absolute", id)
		}
	}
	for id, tool := range c.Tools {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(tool.Path) == "" {
			return errors.New("tool IDs and paths must be non-empty")
		}
		if !filepath.IsAbs(tool.Path) {
			return fmt.Errorf("tool %q path must be absolute", id)
		}
		if tool.WorkingDir != "" && !filepath.IsAbs(tool.WorkingDir) {
			return fmt.Errorf("tool %q working_dir must be absolute", id)
		}
		if strings.ContainsAny(tool.Path, "\r\n\x00\"") {
			return fmt.Errorf("tool %q path contains unsafe characters", id)
		}
		ext := strings.ToLower(filepath.Ext(tool.Path))
		if (ext == ".cmd" || ext == ".bat") && strings.ContainsAny(tool.Path, "%!^&|<>()") {
			return fmt.Errorf("tool %q script path contains command-interpreter metacharacters", id)
		}
	}
	for id, downloader := range c.Downloaders {
		if strings.TrimSpace(id) == "" {
			return errors.New("downloader ID must be non-empty")
		}
		u, e := url.Parse(strings.TrimSpace(downloader.URL))
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("downloader %q URL must be an absolute HTTP(S) URL without embedded credentials", id)
		}
	}
	return nil
}

func (c Config) LibraryRoot(id string) (string, bool) { v, ok := c.Libraries[id]; return v, ok }

func (c Config) LocalTool(id string) (Tool, bool) { v, ok := c.Tools[id]; return v, ok }

func (c Config) LocalDownloader(id string) (Downloader, bool) {
	v, ok := c.Downloaders[id]
	return v, ok
}
