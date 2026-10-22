package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) agentAuth(r *http.Request) (Object, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ds, e := a.list("devices")
	if e != nil {
		return nil, e
	}
	for _, d := range ds {
		if subtle.ConstantTimeCompare([]byte(str(d["token_hash"])), []byte(digest(token))) == 1 {
			return d, nil
		}
	}
	return nil, fmt.Errorf("device authentication failed")
}
func (a *App) agentAPI(w http.ResponseWriter, r *http.Request) {
	d, e := a.agentAuth(r)
	if e != nil {
		fail(w, 401, "设备认证失败")
		return
	}
	dev := str(d["id"])
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agent/")
	switch path {
	case "heartbeat":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		d["last_seen"] = now()
		if e = a.put("devices", d); e != nil {
			fail(w, 500, e.Error())
			return
		}
		write(w, 200, Object{"ok": true})
	case "config":
		ls, _ := a.list("libraries")
		ts, _ := a.list("tools")
		filter := func(in []Object) []Object {
			out := []Object{}
			for _, o := range in {
				if str(o["device_id"]) == dev {
					out = append(out, o)
				}
			}
			return out
		}
		write(w, 200, Object{"device_id": dev, "libraries": filter(ls), "tools": filter(ts)})
	case "poll":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		a.scanMu.Lock()
		defer a.scanMu.Unlock()
		js, _ := a.list("jobs")
		out := []Object{}
		for _, j := range js {
			if str(j["device_id"]) != dev || str(j["status"]) != "pending" {
				continue
			}
			t, _ := time.Parse(time.RFC3339, str(j["created_at"]))
			if time.Since(t) > 2*time.Minute {
				j["status"] = "expired"
				j["result"] = "任务已过期，请重新发起"
				_ = a.put("jobs", j)
				continue
			}
			j["status"] = "running"
			_ = a.put("jobs", j)
			out = append(out, j)
		}
		write(w, 200, out)
	case "covers":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if e != nil {
			fail(w, 400, "封面过大")
			return
		}
		ct := http.DetectContentType(b)
		ext := ""
		if ct == "image/jpeg" {
			ext = ".jpg"
		}
		if ct == "image/png" {
			ext = ".png"
		}
		if ext == "" {
			fail(w, 400, "仅支持 JPEG/PNG")
			return
		}
		name := digest(string(b)) + ext
		if e = os.WriteFile(filepath.Join(a.dataDir, "covers", name), b, 0600); e != nil {
			fail(w, 500, e.Error())
			return
		}
		write(w, 200, Object{"url": "/covers/" + name})
	case "scan":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		o, ok := read(w, r)
		if !ok {
			return
		}
		a.importScan(w, dev, o)
	default:
		parts := strings.Split(path, "/")
		if len(parts) == 3 && parts[0] == "jobs" && parts[2] == "result" && r.Method == "POST" {
			j, e := a.get("jobs", parts[1])
			if e != nil || str(j["device_id"]) != dev {
				fail(w, 404, "任务不存在")
				return
			}
			o, ok := read(w, r)
			if !ok {
				return
			}
			s := str(o["status"])
			if s != "done" && s != "failed" && s != "success" && s != "completed" {
				fail(w, 400, "结果状态无效")
				return
			}
			if s == "success" || s == "completed" {
				s = "done"
			}
			j["status"] = s
			j["result"] = cleanResult(o["result"])
			if str(j["type"]) == "qbit" {
				p := toObject(j["payload"])
				delete(p, "config")
				j["payload"] = p
			}
			j["finished_at"] = now()
			a.saveResponse(w, "jobs", j)
			return
		}
		fail(w, 404, "接口不存在")
	}
}
func defaultProgress() Object {
	return Object{"compression": "pending", "cutting": "pending", "translation": "pending", "verified": false}
}

func hasNFOQuality(work Object) bool {
	if nfo, ok := work["has_nfo"].(bool); ok {
		return nfo
	}
	// Older agents do not send has_nfo. Infer NFO-backed metadata from fields
	// that the filename-only fallback cannot produce.
	if year := num(work["year"]); year > 0 {
		return true
	}
	if len(asObjects(work["actors"])) > 0 || len(asObjects(work["tags"])) > 0 {
		return true
	}
	if work["source_rating"] != nil || str(work["source_url"]) != "" {
		return true
	}
	return false
}

func scanFieldHasValue(work Object, key string) bool {
	switch key {
	case "title", "source_url", "kind":
		return strings.TrimSpace(str(work[key])) != ""
	case "year":
		return num(work[key]) > 0
	case "actors", "tags":
		return len(asObjects(work[key])) > 0 || len(stringSlice(work[key])) > 0
	case "source_rating":
		return work[key] != nil
	default:
		return work[key] != nil
	}
}

func useIncomingScanField(incoming Object, key string, incomingHasNFO bool, existing Object, existingHasNFO bool) bool {
	if !scanFieldHasValue(incoming, key) {
		return false
	}
	if key == "kind" {
		return true
	}
	if key == "title" {
		return incomingHasNFO || !existingHasNFO || !scanFieldHasValue(existing, key)
	}
	return incomingHasNFO || !scanFieldHasValue(existing, key)
}

func asObjects(v any) []Object {
	b, _ := json.Marshal(v)
	out := []Object{}
	_ = json.Unmarshal(b, &out)
	return out
}
func (a *App) importScan(w http.ResponseWriter, dev string, o Object) {
	lib := str(o["library_id"])
	l, e := a.get("libraries", lib)
	if e != nil || str(l["device_id"]) != dev {
		fail(w, 403, "片库不属于设备")
		return
	}
	items, ok := o["works"].([]any)
	if !ok {
		fail(w, 400, "works 必须为数组")
		return
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	all, e := a.list("works")
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	byKey := map[string]Object{}
	for _, x := range all {
		byKey[str(x["id"])] = x
	}
	seen := map[string]bool{}
	updates := map[string]Object{}
	for _, raw := range items {
		incoming, ok := raw.(map[string]any)
		if !ok {
			fail(w, 400, "作品格式无效")
			return
		}
		files := asObjects(incoming["files"])
		if len(files) == 0 {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(str(incoming["code"])))
		workID := "code:" + code
		if code == "" {
			workID = "file:" + digest(dev+"|"+lib+"|"+str(files[0]["relative_path"]))
		}
		existing := byKey[workID]
		incomingHasNFO := hasNFOQuality(incoming)
		if existing == nil {
			existing = Object{"id": workID, "code": code, "title": incoming["title"], "year": incoming["year"], "actors": incoming["actors"], "tags": incoming["tags"], "rating": nil, "source_rating": incoming["source_rating"], "source_url": incoming["source_url"], "kind": incoming["kind"], "collection_status": "owned", "progress": defaultProgress(), "files": []Object{}, "has_nfo": incomingHasNFO}
		}
		manual := toObject(existing["manual_fields"])
		existingHasNFO := hasNFOQuality(existing)
		for _, k := range []string{"title", "year", "actors", "tags", "source_rating", "source_url", "kind"} {
			if flag(manual[k]) || !useIncomingScanField(incoming, k, incomingHasNFO, existing, existingHasNFO) {
				continue
			}
			existing[k] = incoming[k]
		}
		if !flag(manual["cover_url"]) && str(incoming["cover_url"]) != "" && (incomingHasNFO || !existingHasNFO || str(existing["cover_url"]) == "") {
			existing["cover_url"] = incoming["cover_url"]
		}
		mergeActorThumbs(existing, incoming)
		existing["has_nfo"] = existingHasNFO || incomingHasNFO
		efs := asObjects(existing["files"])
		for _, f := range files {
			rel := str(f["relative_path"])
			clean := filepath.Clean(filepath.FromSlash(rel))
			if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Contains(rel, ":") {
				fail(w, 400, "非法文件相对路径")
				return
			}
			f["device_id"] = dev
			f["library_id"] = lib
			f["id"] = digest(dev + "|" + lib + "|" + strings.ToLower(rel))
			f["missing"] = false
			seen[str(f["id"])] = true
			found := false
			for i, old := range efs {
				if str(old["id"]) == str(f["id"]) {
					if old["progress"] != nil {
						f["progress"] = old["progress"]
					}
					efs[i] = f
					found = true
					break
				}
			}
			if !found {
				f["progress"] = defaultProgress()
				efs = append(efs, f)
			}
		}
		existing["files"] = efs
		existing["last_scanned"] = now()
		updates[workID] = existing
		byKey[workID] = existing
	}
	if flag(o["complete"]) {
		for k, x := range byKey {
			fs := asObjects(x["files"])
			changed := false
			for _, f := range fs {
				if str(f["device_id"]) == dev && str(f["library_id"]) == lib && !seen[str(f["id"])] {
					f["missing"] = true
					changed = true
				}
			}
			if changed {
				x["files"] = fs
				updates[k] = x
			}
		}
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	for _, x := range updates {
		x["progress"] = aggregateProgress(asObjects(x["files"]))
		b, _ := json.Marshal(x)
		if _, e = tx.Exec("INSERT INTO documents(kind,id,body) VALUES('works',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", x["id"], string(b)); e != nil {
			fail(w, 500, e.Error())
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 200, Object{"imported": len(items), "complete": flag(o["complete"])})
}
