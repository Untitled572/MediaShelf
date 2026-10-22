package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func validProgress(v any) bool {
	p, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"compression", "cutting", "translation"} {
		s := str(p[k])
		if s != "pending" && s != "in_progress" && s != "done" && s != "not_needed" {
			return false
		}
		if flag(p["verified"]) && s != "done" && s != "not_needed" {
			return false
		}
	}
	return true
}
func (a *App) fileProgress(w http.ResponseWriter, r *http.Request, workID, fileID string) {
	if r.Method != "PATCH" {
		fail(w, 405, "PATCH required")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	if !validProgress(o["progress"]) {
		fail(w, 400, "进度无效或尚未满足验收条件")
		return
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	work, e := a.get("works", workID)
	if e != nil {
		fail(w, 404, "作品不存在")
		return
	}
	fs := asObjects(work["files"])
	found := false
	for _, f := range fs {
		if str(f["id"]) == fileID {
			f["progress"] = o["progress"]
			found = true
		}
	}
	if !found {
		fail(w, 404, "文件不存在")
		return
	}
	work["files"] = fs
	work["progress"] = aggregateProgress(fs)
	a.saveResponse(w, "works", work)
}
func redactJob(j Object) Object {
	out := toObject(j)
	if str(out["type"]) == "qbit" {
		p := toObject(out["payload"])
		delete(p, "config")
		out["payload"] = p
	}
	return out
}
func (a *App) jobList(w http.ResponseWriter) {
	js, e := a.list("jobs")
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	for i, j := range js {
		js[i] = redactJob(j)
	}
	write(w, 200, js)
}
func cleanResult(v any) any {
	if s, ok := v.(string); ok && strings.HasPrefix(s, "{") {
		o := decodeObject(s)
		if o != nil {
			return o
		}
	}
	return v
}

func decodeObject(s string) Object {
	o := Object{}
	if json.Unmarshal([]byte(s), &o) != nil {
		return nil
	}
	return o
}

// Importing a new copy cannot inherit a previous version's verification.
func aggregateProgress(files []Object) Object {
	result := defaultProgress()
	available := []Object{}
	for _, f := range files {
		if !flag(f["missing"]) && !flag(f["scoped_out"]) {
			available = append(available, f)
		}
	}
	if len(available) == 0 {
		return result
	}
	verified := true
	for _, k := range []string{"compression", "cutting", "translation"} {
		done, active, notNeeded := true, false, true
		for _, f := range available {
			p := toObject(f["progress"])
			state := str(p[k])
			if state != "done" && state != "not_needed" {
				done = false
			}
			if state != "not_needed" {
				notNeeded = false
			}
			if state == "in_progress" {
				active = true
			}
			if !flag(p["verified"]) {
				verified = false
			}
		}
		if notNeeded {
			result[k] = "not_needed"
		} else if done {
			result[k] = "done"
		} else if active {
			result[k] = "in_progress"
		}
	}
	result["verified"] = verified
	return result
}
