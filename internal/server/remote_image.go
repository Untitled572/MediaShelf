package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediashelf/internal/integrations"
)

const onlineImageCacheTTL = 7 * 24 * time.Hour

type remoteImageFetcher func(context.Context, string, string) ([]byte, string, error)

func (a *App) serveRemoteImage(w http.ResponseWriter, r *http.Request) {
	a.serveRemoteImageWith(w, r, integrations.FetchRemoteImage)
}

// serveRemoteImageWith keeps the network fetch injectable for deterministic
// cache and response-limit tests. Production always uses the guarded fetcher.
func (a *App) serveRemoteImageWith(w http.ResponseWriter, r *http.Request, fetch remoteImageFetcher) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	u, err := url.Parse(rawURL)
	if rawURL == "" || err != nil || u == nil || !u.IsAbs() || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(rawURL, "\r\n") {
		fail(w, http.StatusBadRequest, "有效的 HTTP(S) 图片 URL 为必填项")
		return
	}
	cacheDir := filepath.Join(a.dataDir, "online-covers")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		fail(w, http.StatusInternalServerError, "图片缓存不可用")
		return
	}
	key := digest(rawURL)
	if body, mime, ok := readCachedRemoteImage(cacheDir, key); ok {
		writeRemoteImage(w, body, mime)
		return
	}
	proxyURL, err := a.outboundProxy()
	if err != nil {
		fail(w, http.StatusBadGateway, "全局代理设置无效："+err.Error())
		return
	}
	body, mime, err := fetch(r.Context(), rawURL, proxyURL)
	if err != nil {
		fail(w, http.StatusBadGateway, "图片获取失败："+err.Error())
		return
	}
	ext := remoteImageExtension(mime)
	if ext == "" || len(body) == 0 || len(body) > integrations.MaxRemoteImageBytes {
		fail(w, http.StatusBadGateway, "图片类型不受支持或超过 5 MiB")
		return
	}
	if err := cacheRemoteImage(cacheDir, key, ext, body); err != nil {
		// Successful fetches remain usable when the cache directory is read-only.
		writeRemoteImage(w, body, mime)
		return
	}
	writeRemoteImage(w, body, mime)
}

func remoteImageExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}

func readCachedRemoteImage(dir, key string) ([]byte, string, bool) {
	for ext, mime := range map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"} {
		path := filepath.Join(dir, key+ext)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if time.Since(info.ModTime()) >= onlineImageCacheTTL {
			_ = os.Remove(path)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil || len(body) == 0 || len(body) > integrations.MaxRemoteImageBytes {
			_ = os.Remove(path)
			continue
		}
		return body, mime, true
	}
	return nil, "", false
}

func cacheRemoteImage(dir, key, ext string, body []byte) error {
	if ext == "" || len(body) == 0 || len(body) > integrations.MaxRemoteImageBytes {
		return errors.New("invalid remote image cache entry")
	}
	tmp, err := os.CreateTemp(dir, ".image-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, key+ext)); err != nil {
		return err
	}
	return nil
}

func writeRemoteImage(w http.ResponseWriter, body []byte, mime string) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
