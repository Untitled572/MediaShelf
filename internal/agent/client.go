package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL, Token string
	HTTP           *http.Client
}

func NewClient(c Config) *Client {
	return &Client{BaseURL: strings.TrimRight(c.ServerURL, "/"), Token: c.Token, HTTP: &http.Client{Timeout: 45 * time.Second}}
}
func (c *Client) request(method, path string, body any, contentType string, out any) error {
	var r io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			r = bytes.NewReader(v)
		default:
			b, e := json.Marshal(body)
			if e != nil {
				return e
			}
			r = bytes.NewReader(b)
		}
	}
	req, e := http.NewRequest(method, c.BaseURL+path, r)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		_, e = io.Copy(io.Discard, resp.Body)
		return e
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type RemoteConfig struct {
	DeviceID  string          `json:"device_id"`
	Libraries []RemoteLibrary `json:"libraries"`
	Tools     []RemoteTool    `json:"tools"`
}
type RemoteLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
type RemoteTool struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	WorkingDir string `json:"working_dir"`
}
type Job struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (c *Client) RemoteConfig() (RemoteConfig, error) {
	var v RemoteConfig
	e := c.request(http.MethodGet, "/api/v1/agent/config", nil, "", &v)
	return v, e
}
func (c *Client) Heartbeat() error {
	return c.request(http.MethodPost, "/api/v1/agent/heartbeat", map[string]any{}, "", nil)
}
func (c *Client) Poll() ([]Job, error) {
	var v []Job
	e := c.request(http.MethodPost, "/api/v1/agent/poll", map[string]any{}, "", &v)
	return v, e
}
func (c *Client) Result(id, status, result string) error {
	return c.request(http.MethodPost, "/api/v1/agent/jobs/"+id+"/result", map[string]string{"status": status, "result": result}, "", nil)
}
func (c *Client) Scan(libraryID string, complete bool, works []Work) error {
	return c.request(http.MethodPost, "/api/v1/agent/scan", map[string]any{"library_id": libraryID, "complete": complete, "works": works}, "", nil)
}
func (c *Client) UploadCover(path string) (string, error) {
	b, e := MakeCoverThumbnail(path)
	if e != nil {
		return "", e
	}
	var v struct {
		URL string `json:"url"`
	}
	e = c.request(http.MethodPost, "/api/v1/agent/covers", b, "image/jpeg", &v)
	return v.URL, e
}
