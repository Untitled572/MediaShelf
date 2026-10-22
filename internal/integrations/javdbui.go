package integrations

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultJavDBUIAPIBase = "https://jdforrepam.com/api"
	defaultJavDBUIWebBase = "https://catembylegacy.fastcdn.dpdns.org/"
	javDBUISignatureSalt  = "lpw6vgqzsp"
	javDBUISignatureSeed  = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
	maxJavDBUIResponse    = 4 << 20
)

// JavDBUIClient reads the public JavDB Web UI API. It does not log in or use
// account cookies; requests carry the timestamp signature used by the public
// web bundle.
type JavDBUIClient struct {
	base            *url.URL
	http            *http.Client
	proxyConfigured bool
}

// JavDBUIActor is an actor card returned by the public recommendation API.
type JavDBUIActor struct {
	ID          string          `json:"id"`
	Type        json.RawMessage `json:"type,omitempty"`
	AvatarURL   string          `json:"avatar_url,omitempty"`
	Name        string          `json:"name"`
	OtherName   string          `json:"other_name,omitempty"`
	Gender      *int            `json:"gender,omitempty"`
	VideosCount int             `json:"videos_count,omitempty"`
}

// JavDBUIActorRecommendations contains the three actor recommendation lists
// returned by /v1/actors/recommend.
type JavDBUIActorRecommendations struct {
	NewActors       []JavDBUIActor `json:"new_actors"`
	MonthlyActors   []JavDBUIActor `json:"monthly_actors"`
	RecommendActors []JavDBUIActor `json:"recommend_actors"`
}

// NewJavDBUIClient constructs a client for the public Web UI API. BaseURL may
// be overridden for deployments that provide a compatible API endpoint.
func NewJavDBUIClient(c CatalogConfig) (*JavDBUIClient, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultJavDBUIAPIBase
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("JavDB Web UI API base URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if err = validateExternalURL(u); err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: safeExternalDial}
	if c.ProxyURL != "" {
		proxyURL, parseErr := url.Parse(c.ProxyURL)
		if parseErr != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
			return nil, errors.New("invalid JavDB Web UI API proxy URL")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		// Proxies commonly run on loopback. Validate the destination separately
		// before sending the request through the configured proxy.
		transport.DialContext = (&net.Dialer{Timeout: 8 * time.Second}).DialContext
	}
	return &JavDBUIClient{
		base: u,
		http: &http.Client{
			Transport: transport,
			Timeout:   12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many JavDB Web UI API redirects")
				}
				if !sameOrigin(req.URL, u) {
					return errors.New("JavDB Web UI API redirected to a different origin")
				}
				return validateExternalURL(req.URL)
			},
		},
		proxyConfigured: c.ProxyURL != "",
	}, nil
}

type javDBUIEnvelope struct {
	Success int             `json:"success"`
	Action  string          `json:"action"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type javDBUIMovieList struct {
	Movies []javDBUIMovie `json:"movies"`
}

type javDBUIMovie struct {
	ID          string          `json:"id"`
	Number      string          `json:"number"`
	Title       string          `json:"title"`
	OriginTitle string          `json:"origin_title"`
	CoverURL    string          `json:"cover_url"`
	ThumbURL    string          `json:"thumb_url"`
	Score       json.RawMessage `json:"score"`
	ReleaseDate string          `json:"release_date"`
	Actors      []struct {
		Name string `json:"name"`
	} `json:"actors"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

func (c *JavDBUIClient) get(ctx context.Context, path string, query url.Values, dest any) error {
	if c == nil || c.base == nil || c.http == nil {
		return errors.New("JavDB Web UI API client is not initialized")
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	u.RawQuery = query.Encode()
	if err := validateExternalURL(&u); err != nil {
		return err
	}
	if c.proxyConfigured {
		ctxCheck, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := validateExternalHost(ctxCheck, u.Hostname())
		cancel()
		if err != nil {
			return fmt.Errorf("JavDB Web UI API host validation failed: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sum := md5.Sum([]byte(ts + javDBUISignatureSeed))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaShelf/1.0 (+public metadata lookup)")
	req.Header.Set("jdsignature", ts+"."+javDBUISignatureSalt+"."+hex.EncodeToString(sum[:]))
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("JavDB Web UI API request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJavDBUIResponse+1))
	if err != nil {
		return fmt.Errorf("read JavDB Web UI API response: %w", err)
	}
	if len(body) > maxJavDBUIResponse {
		return errors.New("JavDB Web UI API response exceeds 4 MiB limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("JavDB Web UI API returned HTTP %d", resp.StatusCode)
	}
	var envelope javDBUIEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode JavDB Web UI API response: %w", err)
	}
	if envelope.Success != 1 {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = strings.TrimSpace(envelope.Action)
		}
		if message == "" {
			message = "provider reported an unsuccessful response"
		}
		return fmt.Errorf("JavDB Web UI API: %s", message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("JavDB Web UI API returned no data")
	}
	if err := json.Unmarshal(envelope.Data, dest); err != nil {
		return fmt.Errorf("decode JavDB Web UI API data: %w", err)
	}
	return nil
}

// Latest returns one page from /v1/movies/latest. An empty list is preserved as
// an empty list; no entries are synthesized when the provider has none.
func (c *JavDBUIClient) Latest(ctx context.Context, page int) ([]Metadata, error) {
	if page < 1 {
		page = 1
	}
	var result javDBUIMovieList
	if err := c.get(ctx, "/v1/movies/latest", url.Values{"page": {strconv.Itoa(page)}, "filter_by": {"all"}}, &result); err != nil {
		return nil, err
	}
	if result.Movies == nil {
		return nil, errors.New("JavDB Web UI API latest response has no movies array")
	}
	return javDBUIMetadataList(result.Movies), nil
}

// Recommendations returns the public aggregate list from /v1/movies/recommend.
func (c *JavDBUIClient) Recommendations(ctx context.Context) ([]Metadata, error) {
	var result struct {
		Movies []javDBUIMovie `json:"movies"`
	}
	if err := c.get(ctx, "/v1/movies/recommend", nil, &result); err != nil {
		return nil, err
	}
	if result.Movies == nil {
		return nil, errors.New("JavDB Web UI API recommendation response has no movies array")
	}
	return javDBUIMetadataList(result.Movies), nil
}

// RecommendedActors returns the public aggregate actor recommendation lists.
func (c *JavDBUIClient) RecommendedActors(ctx context.Context) (JavDBUIActorRecommendations, error) {
	var result JavDBUIActorRecommendations
	if err := c.get(ctx, "/v1/actors/recommend", nil, &result); err != nil {
		return JavDBUIActorRecommendations{}, err
	}
	if result.NewActors == nil || result.MonthlyActors == nil || result.RecommendActors == nil {
		return JavDBUIActorRecommendations{}, errors.New("JavDB Web UI API actor recommendation response is missing a list")
	}
	return result, nil
}

// Lookup searches by code and enriches the exact match from /v4/movies/{id}.
func (c *JavDBUIClient) Lookup(ctx context.Context, code string) (Metadata, error) {
	code = normalizeCode(code)
	if code == "" {
		return Metadata{}, errors.New("code is required")
	}
	var result javDBUIMovieList
	query := url.Values{"q": {code}, "page": {"1"}, "type": {"movie"}}
	if err := c.get(ctx, "/v2/search", query, &result); err != nil {
		return Metadata{}, err
	}
	if result.Movies == nil {
		return Metadata{}, errors.New("JavDB Web UI API search response has no movies array")
	}
	for _, movie := range result.Movies {
		if normalizeCode(movie.Number) == code {
			metadata := javDBUIMetadata(movie)
			if movie.ID == "" {
				return metadata, nil
			}
			detail, err := c.FetchDetails(ctx, movie.ID)
			if err != nil {
				return Metadata{}, err
			}
			return mergeJavDBUIMetadata(metadata, detail), nil
		}
	}
	return Metadata{}, fmt.Errorf("javdbui: code %s not found", code)
}

// FetchDetails returns the actors, tags, rating, and other metadata available
// for one provider movie ID from /v4/movies/{id}.
func (c *JavDBUIClient) FetchDetails(ctx context.Context, id string) (Metadata, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\?#") {
		return Metadata{}, errors.New("valid JavDB Web UI movie ID is required")
	}
	var result struct {
		Movie javDBUIMovie `json:"movie"`
	}
	if err := c.get(ctx, "/v4/movies/"+url.PathEscape(id), nil, &result); err != nil {
		return Metadata{}, err
	}
	if normalizeCode(result.Movie.Number) == "" {
		return Metadata{}, errors.New("JavDB Web UI API movie detail has no number")
	}
	if detailID := strings.TrimSpace(result.Movie.ID); detailID != "" && detailID != id {
		return Metadata{}, errors.New("JavDB Web UI API movie detail ID does not match the requested ID")
	}
	return mergeJavDBUIDetail(javDBUIMetadata(result.Movie), result.Movie), nil
}

func javDBUIMetadataList(movies []javDBUIMovie) []Metadata {
	out := make([]Metadata, 0, len(movies))
	for _, movie := range movies {
		if metadata := javDBUIMetadata(movie); metadata.Code != "" {
			out = append(out, metadata)
		}
	}
	return out
}

func javDBUIMetadata(movie javDBUIMovie) Metadata {
	code := normalizeCode(movie.Number)
	title := strings.TrimSpace(movie.Title)
	if title == "" {
		title = strings.TrimSpace(movie.OriginTitle)
	}
	metadata := Metadata{Code: code, ProviderID: strings.TrimSpace(movie.ID), Title: title, Source: "javdbui", SourceURL: defaultJavDBUIWebBase}
	if len(movie.ReleaseDate) >= 4 {
		if year, err := strconv.Atoi(movie.ReleaseDate[:4]); err == nil && year >= 1800 && year <= time.Now().Year()+1 {
			metadata.Year = year
		}
	}
	metadata.CoverURL = safeJavDBUIImage(movie.CoverURL)
	metadata.ThumbnailURL = safeJavDBUIImage(movie.ThumbURL)
	if score := parseJavDBUIScore(movie.Score); score > 0 {
		metadata.Rating = &score
		metadata.SourceRating = &score
		metadata.RatingScale = "0-5"
		metadata.RatingRaw = strconv.FormatFloat(score, 'f', -1, 64)
	}
	return metadata
}

func mergeJavDBUIDetail(metadata Metadata, detail javDBUIMovie) Metadata {
	if title := strings.TrimSpace(detail.Title); title != "" {
		metadata.Title = title
	} else if title := strings.TrimSpace(detail.OriginTitle); title != "" && metadata.Title == "" {
		metadata.Title = title
	}
	if year, err := strconv.Atoi(detail.ReleaseDate[:min(4, len(detail.ReleaseDate))]); err == nil && year >= 1800 && year <= time.Now().Year()+1 {
		metadata.Year = year
	}
	if cover := safeJavDBUIImage(detail.CoverURL); cover != "" {
		metadata.CoverURL = cover
	}
	if thumb := safeJavDBUIImage(detail.ThumbURL); thumb != "" {
		metadata.ThumbnailURL = thumb
	}
	metadata.Actors = metadata.Actors[:0]
	for _, actor := range detail.Actors {
		if name := strings.TrimSpace(actor.Name); name != "" {
			metadata.Actors = appendUnique(metadata.Actors, name)
		}
	}
	metadata.Tags = metadata.Tags[:0]
	for _, tag := range detail.Tags {
		if name := strings.TrimSpace(tag.Name); name != "" {
			metadata.Tags = appendUnique(metadata.Tags, name)
		}
	}
	if score := parseJavDBUIScore(detail.Score); score > 0 {
		metadata.Rating = &score
		metadata.SourceRating = &score
		metadata.RatingScale = "0-5"
		metadata.RatingRaw = strconv.FormatFloat(score, 'f', -1, 64)
	}
	return metadata
}

func mergeJavDBUIMetadata(base, detail Metadata) Metadata {
	if detail.Code != "" {
		base.Code = detail.Code
	}
	if detail.Title != "" {
		base.Title = detail.Title
	}
	if detail.Year != 0 {
		base.Year = detail.Year
	}
	if detail.CoverURL != "" {
		base.CoverURL = detail.CoverURL
	}
	if detail.ThumbnailURL != "" {
		base.ThumbnailURL = detail.ThumbnailURL
	}
	if len(detail.Actors) != 0 {
		base.Actors = detail.Actors
	}
	if len(detail.Tags) != 0 {
		base.Tags = detail.Tags
	}
	if detail.Rating != nil {
		base.Rating = detail.Rating
		base.SourceRating = detail.SourceRating
		base.RatingScale = detail.RatingScale
		base.RatingRaw = detail.RatingRaw
	}
	return base
}

func safeJavDBUIImage(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || validateExternalURL(u) != nil || u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func parseJavDBUIScore(raw json.RawMessage) float64 {
	value := strings.TrimSpace(string(raw))
	if len(value) > 1 && value[0] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil {
			return 0
		}
		value = strings.TrimSpace(decoded)
	}
	score, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score <= 0 || score > 5 {
		return 0
	}
	return score
}
