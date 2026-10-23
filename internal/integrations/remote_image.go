package integrations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const MaxRemoteImageBytes = 5 << 20

const javDBUIImageHost = "tp.spfcas.com"
const javDBUIImageXORKey byte = 0x7a

// FetchRemoteImage fetches a public raster image, optionally through the
// configured outbound proxy. Every initial and redirected destination is
// checked against DNS-resolved private/reserved addresses before connecting.
func FetchRemoteImage(ctx context.Context, rawURL, proxyURL string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User != nil || strings.ContainsAny(rawURL, "\r\n") {
		return nil, "", errors.New("invalid image URL")
	}
	if err := validateRemoteImageTarget(ctx, u); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: safeExternalDial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	if strings.TrimSpace(proxyURL) != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.User != nil {
			return nil, "", errors.New("invalid outbound proxy URL")
		}
		transport.Proxy = http.ProxyURL(proxy)
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many image redirects")
		}
		if err := validateRemoteImageTarget(req.Context(), req.URL); err != nil {
			return err
		}
		return nil
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "MediaShelf/1.0 (image proxy)")
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp,image/gif")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("image server returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxRemoteImageBytes {
		return nil, "", errors.New("image exceeds 5 MiB limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 || len(body) > MaxRemoteImageBytes {
		return nil, "", errors.New("image is empty or exceeds 5 MiB limit")
	}
	body, contentType, err := decodeRemoteRaster(body, u.Hostname(), resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	return body, contentType, nil
}

// decodeRemoteRaster validates the bytes instead of trusting CDN MIME headers.
// JavDBUI's tp.spfcas.com cover CDN XORs its image payload with the fixed byte
// key 0x7a; decoding is intentionally limited to that exact host.
func decodeRemoteRaster(body []byte, host, declaredType string) ([]byte, string, error) {
	if len(body) == 0 || len(body) > MaxRemoteImageBytes {
		return nil, "", errors.New("image is empty or exceeds 5 MiB limit")
	}
	if config, format, err := image.DecodeConfig(bytes.NewReader(body)); err == nil {
		if mime := rasterMIME(format); mime != "" && config.Width > 0 && config.Height > 0 {
			return body, mime, nil
		}
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), javDBUIImageHost) {
		decoded := make([]byte, len(body))
		for i, b := range body {
			decoded[i] = b ^ javDBUIImageXORKey
		}
		// Observed CDN streams can begin with an encoded zero marker.
		if len(decoded) > 1 && decoded[0] == 0 {
			decoded = decoded[1:]
		}
		if config, format, err := image.DecodeConfig(bytes.NewReader(decoded)); err == nil {
			if mime := rasterMIME(format); mime != "" && config.Width > 0 && config.Height > 0 {
				return decoded, mime, nil
			}
		}
	}
	declaredType = strings.ToLower(strings.TrimSpace(strings.Split(declaredType, ";")[0]))
	return nil, "", fmt.Errorf("unsupported or invalid raster image content type %q", declaredType)
}

func rasterMIME(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
}

func validateRemoteImageTarget(ctx context.Context, u *url.URL) error {
	if u == nil || u.User != nil {
		return errors.New("invalid image URL")
	}
	if err := validateExternalURL(u); err != nil {
		return err
	}
	return validateExternalHost(ctx, u.Hostname())
}

// ValidateRemoteImageURL validates a public image destination without fetching
// its body, for browser-direct redirects.
func ValidateRemoteImageURL(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	return validateRemoteImageTarget(ctx, u)
}
func RemoteImageNeedsDecoding(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), javDBUIImageHost)
}
