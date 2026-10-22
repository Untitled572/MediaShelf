package agent

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var videoExt = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".m4v": true, ".webm": true}
var codePattern = regexp.MustCompile(`(?i)([a-z]{2,8}[-_ ]?\d{2,8})`)

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
	Kind             string    `json:"kind"`
	CollectionStatus string    `json:"collection_status,omitempty"`
	Files            []File    `json:"files"`
	Progress         *Progress `json:"progress,omitempty"`
	coverPath        string
}
type File struct {
	ID           string `json:"id,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	LibraryID    string `json:"library_id"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	ModifiedAt   string `json:"modified_at"`
	Version      int    `json:"version,omitempty"`
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
	Code          string   `xml:"uniqueid"`
	ID            string   `xml:"id"`
	Actors        []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
	Genres []string `xml:"genre"`
	Rating string   `xml:"rating"`
	Thumb  string   `xml:"thumb"`
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
		data := nfoData{}
		// movie.nfo describes a folder-level release and groups every video in it.
		folderNFO := filepath.Join(dir, "movie.nfo")
		folderMeta, folderOK := readNFO(folderNFO)
		if !folderOK {
			folderMeta, _ = readNFO(filepath.Join(dir, stem+".nfo"))
		}
		data = folderMeta
		code := normalizeCode(firstNonBlank(data.Code, data.ID, codeFromText(stem)))
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
			work = &Work{Code: code, Title: title, Year: yearPtr(data.Year), Actors: []string{}, Tags: unique(data.Genres), Kind: kind, Files: []File{}}
			work.coverPath = findCover(dir)
			for _, a := range data.Actors {
				if strings.TrimSpace(a.Name) != "" {
					work.Actors = append(work.Actors, strings.TrimSpace(a.Name))
				}
			}
			groups[key] = work
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
		work.Files = append(work.Files, File{LibraryID: libraryID, RelativePath: filepath.ToSlash(rel), Size: st.Size(), ModifiedAt: st.ModTime().Format(time.RFC3339), Missing: false})
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
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}
func codeFromText(s string) string {
	m := codePattern.FindStringSubmatch(s)
	if len(m) > 1 {
		return m[1]
	}
	return ""
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
func findCover(dir string) string {
	for _, name := range []string{"poster.jpg", "poster.png", "folder.jpg", "folder.png", "cover.jpg", "cover.png", "movie.jpg", "movie.png"} {
		p := filepath.Join(dir, name)
		if st, e := os.Stat(p); e == nil && !st.IsDir() {
			return p
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
