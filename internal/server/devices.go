package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func publicDevice(device Object) Object {
	out := toObject(device)
	delete(out, "token_hash")
	seen, err := time.Parse(time.RFC3339, str(out["last_seen"]))
	out["online"] = err == nil && time.Since(seen) < 90*time.Second
	return out
}
func deviceFields(body Object, existing Object) (Object, error) {
	out := toObject(existing)
	if out == nil {
		out = Object{}
	}
	for key := range body {
		if key != "name" && key != "host" {
			return nil, fmt.Errorf("不支持修改设备字段 %s", key)
		}
	}
	if value, ok := body["name"]; ok {
		name, valid := value.(string)
		name = strings.TrimSpace(name)
		if !valid || name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("设备名称须为 1–120 个字符")
		}
		out["name"] = name
	}
	if str(out["name"]) == "" {
		return nil, fmt.Errorf("请填写设备名称")
	}
	if value, ok := body["host"]; ok {
		host, valid := value.(string)
		host = strings.ToLower(strings.TrimSpace(host))
		if !valid {
			return nil, fmt.Errorf("设备域名或 IP 格式无效")
		}
		if host != "" {
			if ip := net.ParseIP(host); ip != nil {
				host = ip.String()
			} else {
				if len(host) > 253 || strings.ContainsAny(host, "/:?#@\\\r\n\t ") {
					return nil, fmt.Errorf("仅填写域名或 IP，不包含协议、端口或路径")
				}
				host = strings.TrimSuffix(host, ".")
				for _, label := range strings.Split(host, ".") {
					if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
						return nil, fmt.Errorf("设备域名格式无效")
					}
					for _, ch := range label {
						if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
							return nil, fmt.Errorf("设备域名格式无效")
						}
					}
				}
			}
		}
		out["host"] = host
	}
	return out, nil
}

func (a *App) devicesAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	// Serialize lifecycle changes with authenticated agent operations. An old
	// heartbeat or scan cannot recreate a device or its rows after deletion.
	if r.Method == http.MethodDelete {
		a.downloadMu.Lock()
		defer a.downloadMu.Unlock()
	}
	a.devicesMu.Lock()
	defer a.devicesMu.Unlock()
	key := ""
	if len(parts) > 1 {
		key = parts[1]
	}
	if len(parts) > 3 || len(parts) == 3 && (parts[2] != "deletion-preview" || r.Method != http.MethodGet) {
		fail(w, 404, "设备接口不存在")
		return
	}
	if key == "" && r.Method == http.MethodGet {
		devices, err := a.list("devices")
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		out := []Object{}
		for _, device := range devices {
			out = append(out, publicDevice(device))
		}
		write(w, 200, out)
		return
	}
	if key == "" && r.Method == http.MethodPost {
		body, ok := read(w, r)
		if !ok {
			return
		}
		device, err := deviceFields(body, Object{})
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		token := id()
		device["id"] = id()
		device["token_hash"] = digest(token)
		device["last_seen"] = ""
		if err := a.put("devices", device); err != nil {
			fail(w, 500, err.Error())
			return
		}
		write(w, 201, Object{"id": device["id"], "token": token, "name": device["name"], "host": device["host"]})
		return
	}
	if key == "" {
		fail(w, 405, "Method not allowed")
		return
	}
	device, err := a.get("devices", key)
	if err != nil {
		fail(w, 404, "设备不存在")
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		write(w, 200, publicDevice(device))
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPatch {
		body, ok := read(w, r)
		if !ok {
			return
		}
		updated, err := deviceFields(body, device)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if err := a.put("devices", updated); err != nil {
			fail(w, 500, err.Error())
			return
		}
		write(w, 200, publicDevice(updated))
		return
	}
	if r.Method != http.MethodDelete && !(len(parts) == 3 && r.Method == http.MethodGet) {
		fail(w, 405, "Method not allowed")
		return
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	plan, err := deviceDeletionPlan(tx, key)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		write(w, 200, Object{"device": publicDevice(device), "counts": plan.counts})
		return
	}
	for _, row := range plan.remove {
		if _, err = tx.Exec("DELETE FROM documents WHERE kind=? AND id=?", row[0], row[1]); err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	for _, work := range plan.update {
		var encoded []byte
		encoded, err = json.Marshal(work)
		if err == nil {
			_, err = tx.Exec("UPDATE documents SET body=? WHERE kind='works' AND id=?", string(encoded), work["id"])
		}
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
	}
	if _, err = tx.Exec("DELETE FROM documents WHERE kind='devices' AND id=?", key); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	write(w, 200, Object{"deleted": true, "id": key, "counts": plan.counts})
}

type deviceDeletePlan struct {
	remove [][2]string
	update []Object
	counts map[string]int
}

func deviceDeletionPlan(tx *sql.Tx, deviceID string) (deviceDeletePlan, error) {
	plan := deviceDeletePlan{counts: map[string]int{"libraries": 0, "tools": 0, "downloaders": 0, "jobs": 0, "files": 0, "works_deleted": 0, "works_updated": 0, "candidates": 0}}
	libraries := map[string]bool{}
	downloaders := map[string]bool{}
	for _, kind := range []string{"libraries", "tools", "downloaders", "jobs", "candidates"} {
		rows, err := txList(tx, kind)
		if err != nil {
			return plan, err
		}
		for _, row := range rows {
			owned := str(row["device_id"]) == deviceID
			if kind == "candidates" && downloaders[str(row["downloader_id"])] {
				owned = true
			}
			if !owned {
				continue
			}
			rowID := str(row["id"])
			plan.remove = append(plan.remove, [2]string{kind, rowID})
			plan.counts[kind]++
			if kind == "libraries" {
				libraries[rowID] = true
			}
			if kind == "downloaders" {
				downloaders[rowID] = true
			}
		}
	}
	works, err := txList(tx, "works")
	if err != nil {
		return plan, err
	}
	for _, work := range works {
		ownedWork := str(work["device_id"]) == deviceID || libraries[str(work["library_id"])]
		files := asObjects(work["files"])
		remaining := []Object{}
		removed := 0
		for _, file := range files {
			owned := false
			if fileDevice := str(file["device_id"]); fileDevice != "" {
				owned = fileDevice == deviceID
			} else if lib := str(file["library_id"]); lib != "" {
				owned = libraries[lib]
			} else {
				owned = ownedWork
			}
			if owned {
				removed++
				continue
			}
			remaining = append(remaining, file)
		}
		if removed == 0 && !ownedWork {
			continue
		}
		plan.counts["files"] += removed
		if len(remaining) == 0 {
			plan.remove = append(plan.remove, [2]string{"works", str(work["id"])})
			plan.counts["works_deleted"]++
			continue
		}
		work["files"] = remaining
		work["progress"] = aggregateProgress(remaining)
		if str(work["device_id"]) == deviceID {
			delete(work, "device_id")
		}
		if libraries[str(work["library_id"])] {
			delete(work, "library_id")
		}
		plan.update = append(plan.update, work)
		plan.counts["works_updated"]++
	}
	return plan, nil
}
