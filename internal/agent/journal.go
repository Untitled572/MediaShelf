package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PendingResult records a completed local action until the server confirms it.
// Job payloads are deliberately excluded so downloader credentials are not
// written to disk.
type PendingResult struct {
	Status string `json:"status"`
	Result string `json:"result"`
}

type CoverCacheEntry struct {
	Size             int64  `json:"size"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
	URL              string `json:"url"`
}

type journalFile struct {
	Results map[string]PendingResult   `json:"results"`
	Covers  map[string]CoverCacheEntry `json:"covers"`
}

type ResultJournal struct {
	mu      sync.Mutex
	path    string
	entries map[string]PendingResult
	covers  map[string]CoverCacheEntry
}

func OpenResultJournal(path string) (*ResultJournal, error) {
	if path == "" {
		return nil, errors.New("result journal path is required")
	}
	j := &ResultJournal{path: path, entries: map[string]PendingResult{}, covers: map[string]CoverCacheEntry{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		var data journalFile
		if err = json.Unmarshal(b, &data); err != nil {
			return nil, fmt.Errorf("decode result journal: %w", err)
		}
		if data.Results != nil || data.Covers != nil {
			j.entries = data.Results
			j.covers = data.Covers
		} else if err = json.Unmarshal(b, &j.entries); err != nil {
			return nil, fmt.Errorf("decode legacy result journal: %w", err)
		}
		if j.entries == nil {
			j.entries = map[string]PendingResult{}
		}
		if j.covers == nil {
			j.covers = map[string]CoverCacheEntry{}
		}
	}
	return j, nil
}

func (j *ResultJournal) Get(id string) (PendingResult, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	v, ok := j.entries[id]
	return v, ok
}
func (j *ResultJournal) Pending() map[string]PendingResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make(map[string]PendingResult, len(j.entries))
	for id, v := range j.entries {
		out[id] = v
	}
	return out
}
func (j *ResultJournal) Save(id string, v PendingResult) error {
	if id == "" {
		return errors.New("job ID is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries[id] = v
	return j.persistLocked()
}
func (j *ResultJournal) Remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.entries[id]; !ok {
		return nil
	}
	delete(j.entries, id)
	return j.persistLocked()
}
func (j *ResultJournal) CachedCover(path string, size int64, modified time.Time) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.covers[coverPathKey(path)]
	if !ok || entry.Size != size || entry.ModifiedUnixNano != modified.UnixNano() || entry.URL == "" {
		return "", false
	}
	return entry.URL, true
}
func (j *ResultJournal) CacheCover(path string, size int64, modified time.Time, coverURL string) error {
	if coverURL == "" {
		return errors.New("cover URL is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.covers[coverPathKey(path)] = CoverCacheEntry{Size: size, ModifiedUnixNano: modified.UnixNano(), URL: coverURL}
	return j.persistLocked()
}
func coverPathKey(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = filepath.Clean(abs)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(path)))
	return hex.EncodeToString(sum[:])
}

func (j *ResultJournal) persistLocked() error {
	b, err := json.MarshalIndent(journalFile{Results: j.entries, Covers: j.covers}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(j.path), 0700); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = replaceJournalFile(tmp, j.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
