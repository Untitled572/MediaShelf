package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

const maxBatchProgressWorks = 500

func (a *App) batchProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	input, ok := read(w, r)
	if !ok {
		return
	}
	ids, ok := batchProgressIDs(input["ids"])
	if !ok {
		fail(w, http.StatusBadRequest, "作品编号列表无效")
		return
	}
	progress, ok := batchProgressFields(input["progress"])
	if !ok {
		fail(w, http.StatusBadRequest, "进度无效")
		return
	}

	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	works := make([]Object, 0, len(ids))
	for _, workID := range ids {
		var raw string
		err := tx.QueryRow("SELECT body FROM documents WHERE kind=? AND id=?", "works", workID).Scan(&raw)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "作品不存在")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		work := Object{}
		if err := json.Unmarshal([]byte(raw), &work); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if exclusivelyScopedOut(work) {
			fail(w, http.StatusNotFound, "作品不在当前片库范围")
			return
		}
		works = append(works, work)
	}

	updatedFiles := 0
	for _, work := range works {
		files := asObjects(work["files"])
		if len(files) == 0 {
			merged := mergePartialProgress(work["progress"], progress)
			if !validProgress(map[string]any(merged)) {
				fail(w, http.StatusBadRequest, "进度无效或尚未满足验收条件")
				return
			}
			work["progress"] = merged
			continue
		}

		for _, file := range files {
			if flag(file["missing"]) || flag(file["scoped_out"]) {
				continue
			}
			merged := mergePartialProgress(file["progress"], progress)
			if !validProgress(map[string]any(merged)) {
				fail(w, http.StatusBadRequest, "进度无效或尚未满足验收条件")
				return
			}
			file["progress"] = merged
			updatedFiles++
		}
		work["files"] = files
		work["progress"] = aggregateProgress(files)
	}

	for _, work := range works {
		body, err := json.Marshal(work)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec("UPDATE documents SET body=? WHERE kind=? AND id=?", string(body), "works", str(work["id"])); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, Object{"updated_works": len(works), "updated_files": updatedFiles})
}

func batchProgressIDs(raw any) ([]string, bool) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > maxBatchProgressWorks {
		return nil, false
	}
	ids := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		id, ok := value.(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" || seen[id] {
			return nil, false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, true
}

func batchProgressFields(raw any) (Object, bool) {
	fields, ok := raw.(map[string]any)
	if !ok || len(fields) == 0 {
		return nil, false
	}
	valid := map[string]bool{"compression": true, "cutting": true, "translation": true, "verified": true}
	out := Object{}
	for key, value := range fields {
		if !valid[key] {
			return nil, false
		}
		if key == "verified" {
			if _, ok := value.(bool); !ok {
				return nil, false
			}
			out[key] = value
			continue
		}
		state, ok := value.(string)
		if !ok || (state != "pending" && state != "in_progress" && state != "done" && state != "not_needed") {
			return nil, false
		}
		out[key] = state
	}
	return out, true
}

func mergePartialProgress(current any, patch Object) Object {
	merged := toObject(current)
	if merged == nil {
		merged = defaultProgress()
	} else {
		for key, value := range defaultProgress() {
			if _, ok := merged[key]; !ok {
				merged[key] = value
			}
		}
	}
	for key, value := range patch {
		merged[key] = value
	}
	return merged
}
