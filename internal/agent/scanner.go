package agent

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var videoExt = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".m4v": true, ".webm": true}
var codePattern = regexp.MustCompile(`(?i)([a-z]{2,8}[-_ ]?\d{2,8})`)
var fc2CodePattern = regexp.MustCompile(`(?i)(FC2[-_ ]?PPV[-_ ]?\d{5,})`)
var resolutionPattern = regexp.MustCompile(`(?i)(2160p|1080p|720p|480p|4k|8k)`)
var partPattern = regexp.MustCompile(`(?i)(?:^|[._ -])(?:cd|disc|disk|part|pt|seg|segment)[._ -]?(\d{1,4})(?:$|[._ -])`)
var numericPartPattern = regexp.MustCompile(`(?i)(?:_|-)(\d{3,4})(?:$|[._ -])`)
var explicitFullPattern = regexp.MustCompile(`(?i)(?:^|[._ -])(?:full|complete)(?:$|[._ -])`)

type Work struct {
	ID               string    `json:"id,omitempty"`
	Code             string    `json:"code"`
	Title            string    `json:"title"`
	Year             *int      `json:"year,omitempty"`
	Actors           []string  `json:"actors"`
	Tags             []string  `json:"tags"`
	Rating           *float64  `json:"rating"`
	SourceRating     *float64  `json:"source_rating"`
	CoverURL         string    `json:"cover_url,omitempty"`
	SourceURL        string    `json:"source_url,omitempty"`
	HasNFO           bool      `json:"has_nfo"`
	Kind             string    `json:"kind"`
	CollectionStatus string    `json:"collection_status,omitempty"`
	Files            []File    `json:"files"`
	Progress         *Progress `json:"progress,omitempty"`
	coverPath        string
	nfoPriority      int
}
type File struct {
	ID           string `json:"id,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	LibraryID    string `json:"library_id"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	ModifiedAt   string `json:"modified_at"`
	Version      string `json:"version,omitempty"`
	MediaKind    string `json:"media_kind"`
	Missing      bool   `json:"missing"`
}
type Progress struct {
	Compression string `json:"compression"`
	Cutting     string `json:"cutting"`
	Translation string `json:"translation"`
	Verified    bool   `json:"verified"`
}
type ScanResult struct {
	Works    []Work
	Complete bool
	Err      error
}

type nfoData struct {
	XMLName       xml.Name `xml:"movie"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	Year          int      `xml:"year"`
	ID            string   `xml:"id"`
	UniqueIDs     []nfoID  `xml:"uniqueid"`
	Actors        []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
	Genres    []string `xml:"genre"`
	Rating    string   `xml:"rating"`
	Thumb     []string `xml:"thumb"`
	Premiered string   `xml:"premiered"`
	Website   string   `xml:"website"`
	URL       string   `xml:"url"`
}

type nfoID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

// ScanLibrary enumerates videos without changing library contents. A traversal
// error makes the result incomplete, so callers must never report completion.
func ScanLibrary(libraryID, root string) ScanResult {
	result := ScanResult{Complete: true, Works: []Work{}}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ScanResult{Err: err}
	}
	var videos []string
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Complete = false
			if result.Err == nil {
				result.Err = walkErr
			}
			return nil
		}
		if !d.IsDir() && videoExt[strings.ToLower(filepath.Ext(path))] {
			videos = append(videos, path)
		}
		return nil
	})
	if err != nil {
		result.Complete = false
		result.Err = err
	}
	if !result.Complete {
		return result
	}
	sort.Strings(videos)
	groups := map[string]*Work{}
	for _, path := range videos {
		dir := filepath.Dir(path)
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		// Prefer the file-specific NFO; movie.nfo still groups every video in
		// the directory and supplies metadata when no file-specific NFO exists.
		folderNFO := filepath.Join(dir, "movie.nfo")
		folderMeta, folderOK := readNFO(folderNFO)
		data, fileMetaOK := readNFO(filepath.Join(dir, stem+".nfo"))
		if !fileMetaOK {
			data = folderMeta
		}
		code := normalizeCode(firstNonBlank(selectNFOCode(data.UniqueIDs), data.ID, codeFromText(stem), codeFromText(filepath.Base(dir))))
		key := "file:" + path
		if folderOK {
			key = "folder:" + dir
		} else if code != "" {
			key = "code:" + code
		}
		work := groups[key]
		if work == nil {
			title := firstNonBlank(data.Title, data.OriginalTitle, stem)
			kind := ""
			if rel, e := filepath.Rel(rootAbs, path); e == nil {
				parts := strings.Split(rel, string(filepath.Separator))
				if len(parts) > 1 {
					kind = parts[0]
				}
			}
			if kind == "" {
				kind = "unknown"
			}
			year := data.Year
			if year <= 0 {
				year = yearFrom(data.Premiered)
			}
			priority := nfoPriority(fileMetaOK, folderOK)
			work = &Work{Code: code, Title: title, Year: yearPtr(year), Actors: []string{}, Tags: unique(data.Genres), Kind: kind, Files: []File{}, HasNFO: fileMetaOK || folderOK, nfoPriority: priority}
			work.SourceRating = floatPtr(data.Rating)
			work.SourceURL = firstNonBlank(data.Website, data.URL)
			thumb := ""
			if len(data.Thumb) > 0 {
				thumb = data.Thumb[0]
			}
			work.coverPath = findCover(rootAbs, dir, stem, thumb)
			for _, a := range data.Actors {
				if strings.TrimSpace(a.Name) != "" {
					work.Actors = append(work.Actors, strings.TrimSpace(a.Name))
				}
			}
			groups[key] = work
		} else if fileMetaOK || folderOK {
			mergeNFOIntoWork(work, data, rootAbs, dir, stem, nfoPriority(fileMetaOK, folderOK))
		}
		st, e := os.Stat(path)
		if e != nil {
			result.Complete = false
			result.Err = e
			continue
		}
		rel, e := filepath.Rel(rootAbs, path)
		if e != nil {
			result.Complete = false
			result.Err = e
			continue
		}
		version, mediaKind := inferMediaHints(path, code)
		work.Files = append(work.Files, File{LibraryID: libraryID, RelativePath: filepath.ToSlash(rel), Size: st.Size(), ModifiedAt: st.ModTime().Format(time.RFC3339), Version: version, MediaKind: mediaKind, Missing: false})
	}
	for _, w := range groups {
		result.Works = append(result.Works, *w)
	}
	sort.Slice(result.Works, func(i, j int) bool {
		if result.Works[i].Code == result.Works[j].Code {
			return result.Works[i].Title < result.Works[j].Title
		}
		return result.Works[i].Code < result.Works[j].Code
	})
	return result
}

func nfoPriority(fileNFO, folderNFO bool) int {
	if fileNFO {
		return 2
	}
	if folderNFO {
		return 1
	}
	return 0
}

func mergeNFOIntoWork(work *Work, data nfoData, root, dir, stem string, priority int) {
	if priority == 0 {
		return
	}
	work.HasNFO = true
	prefer := priority >= work.nfoPriority
	if title := firstNonBlank(data.Title, data.OriginalTitle); title != "" && (prefer || strings.TrimSpace(work.Title) == "") {
		work.Title = title
	}
	year := data.Year
	if year <= 0 {
		year = yearFrom(data.Premiered)
	}
	if year > 0 && (prefer || work.Year == nil) {
		work.Year = yearPtr(year)
	}
	for _, actor := range data.Actors {
		if name := strings.TrimSpace(actor.Name); name != "" {
			if prefer || len(work.Actors) == 0 {
				work.Actors = appendUniqueString(work.Actors, name)
			}
		}
	}
	if prefer || len(work.Tags) == 0 {
		work.Tags = unique(append(work.Tags, data.Genres...))
	}
	if rating := floatPtr(data.Rating); rating != nil && (prefer || work.SourceRating == nil) {
		work.SourceRating = rating
	}
	if source := firstNonBlank(data.Website, data.URL); source != "" && (prefer || work.SourceURL == "") {
		work.SourceURL = source
	}
	thumb := ""
	if len(data.Thumb) > 0 {
		thumb = data.Thumb[0]
	}
	if cover := findCover(root, dir, stem, thumb); cover != "" && (prefer || work.coverPath == "") {
		work.coverPath = cover
	}
	if priority > work.nfoPriority {
		work.nfoPriority = priority
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	return append(values, value)
}

func readNFO(path string) (nfoData, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nfoData{}, false
	}
	var d nfoData
	if xml.Unmarshal(b, &d) != nil {
		return nfoData{}, false
	}
	return d, true
}
func normalizeCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "www.") {
		return ""
	}
	if m := fc2CodePattern.FindStringSubmatch(s); len(m) > 1 {
		return canonicalCode(m[1])
	}
	if m := codePattern.FindStringSubmatch(s); len(m) > 1 {
		return canonicalCode(m[1])
	}
	return ""
}
func canonicalCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}
func codeFromText(s string) string {
	if m := fc2CodePattern.FindStringSubmatch(s); len(m) > 1 {
		return canonicalCode(m[1])
	}
	m := codePattern.FindStringSubmatch(s)
	if len(m) > 1 {
		return canonicalCode(m[1])
	}
	return ""
}

// inferMediaHints extracts only explicit filename facts. Absence of a segment
// marker does not prove a file is a complete work, so the default stays unknown.
func inferMediaHints(path, code string) (version, mediaKind string) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	search := name
	if code != "" {
		search = replaceFold(search, code, "")
	}
	resolution := ""
	if m := resolutionPattern.FindStringSubmatch(search); len(m) > 1 {
		resolution = strings.ToLower(m[1])
	}
	part := ""
	if m := partPattern.FindStringSubmatch(search); len(m) > 1 {
		part = "part-" + m[1]
	} else if m := numericPartPattern.FindStringSubmatch(search); len(m) > 1 {
		part = "part-" + m[1]
	}
	hints := []string{}
	if resolution != "" {
		hints = append(hints, resolution)
	}
	if part != "" {
		hints = append(hints, part)
	}
	version = strings.Join(hints, "+")
	switch {
	case part != "":
		mediaKind = "segment"
	case explicitFullPattern.MatchString(search):
		mediaKind = "full"
	default:
		mediaKind = "unknown"
	}
	return
}
func replaceFold(s, old, replacement string) string {
	if old == "" {
		return s
	}
	i := strings.Index(strings.ToLower(s), strings.ToLower(old))
	if i < 0 {
		return s
	}
	return s[:i] + replacement + s[i+len(old):]
}
func selectNFOCode(ids []nfoID) string {
	bestRank := -1
	best := ""
	for _, id := range ids {
		typ := strings.ToLower(strings.TrimSpace(id.Type))
		rank := 0
		switch typ {
		case "dvdid":
			rank = 3
		case "num":
			rank = 2
		case "id":
			rank = 1
		default:
			continue
		}
		if strings.EqualFold(strings.TrimSpace(id.Default), "true") {
			rank += 10
		}
		value := normalizeCode(id.Value)
		if value != "" && rank > bestRank {
			bestRank = rank
			best = value
		}
	}
	return best
}
func firstNonBlank(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
func yearPtr(y int) *int {
	if y <= 0 {
		return nil
	}
	return &y
}
func yearFrom(v string) int {
	v = strings.TrimSpace(v)
	if len(v) < 4 {
		return 0
	}
	y, e := strconv.Atoi(v[:4])
	if e != nil || y < 1800 || y > 2200 {
		return 0
	}
	return y
}
func floatPtr(raw string) *float64 {
	v, e := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if e != nil || v < 0 {
		return nil
	}
	return &v
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	return out
}
func findCover(root, dir, stem, thumb string) string {
	candidates := []string{filepath.Join(dir, stem+"-poster.jpg"), filepath.Join(dir, stem+"-poster.png"), filepath.Join(dir, "poster.jpg"), filepath.Join(dir, "poster.png"), filepath.Join(dir, "folder.jpg"), filepath.Join(dir, "folder.png"), filepath.Join(dir, "cover.jpg"), filepath.Join(dir, "cover.png"), filepath.Join(dir, "movie.jpg"), filepath.Join(dir, "movie.png")}
	if strings.TrimSpace(thumb) != "" && !strings.Contains(thumb, "://") {
		p := thumb
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, filepath.FromSlash(p))
		}
		candidates = append(candidates, p)
	}
	for _, p := range candidates {
		rel, err := filepath.Rel(root, p)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if st, e := os.Stat(p); e == nil && !st.IsDir() {
			ext := strings.ToLower(filepath.Ext(p))
			if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
				return p
			}
		}
	}
	return ""
}

func (s ScanResult) Error() error {
	if s.Err != nil {
		return s.Err
	}
	if !s.Complete {
		return fmt.Errorf("library scan incomplete")
	}
	return nil
}
