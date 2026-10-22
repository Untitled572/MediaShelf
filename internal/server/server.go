package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Object map[string]any
type App struct {
	db         *sql.DB
	password   string
	mu         sync.Mutex
	scanMu     sync.Mutex
	downloadMu sync.Mutex
	dataDir    string
	webDir     string
	// actorProfileAPIBase and actorProfileHTTPClient are test seams. Production
	// requests use fixed Wikimedia hosts and the configured outbound proxy.
	actorProfileAPIBase    string
	actorProfileHTTPClient *http.Client
}

func str(v any) string       { s, _ := v.(string); return s }
func num(v any) float64      { n, _ := v.(float64); return n }
func flag(v any) bool        { b, _ := v.(bool); return b }
func id() string             { b := make([]byte, 18); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func now() string            { return time.Now().UTC().Format(time.RFC3339) }
func env(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
func New(data, password, web string) (*App, error) {
	minimum := 12
	if os.Getenv("MEDIASHELF_ALLOW_SHORT_PASSWORD") == "true" {
		minimum = 6
	}
	if len(password) < minimum {
		return nil, fmt.Errorf("MEDIASHELF_PASSWORD must have at least %d characters", minimum)
	}
	if err := os.MkdirAll(filepath.Join(data, "covers"), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "mediashelf.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS documents (kind TEXT NOT NULL,id TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(kind,id)); CREATE TABLE IF NOT EXISTS auth_sessions (token_hash TEXT PRIMARY KEY, expires_at TEXT NOT NULL); PRAGMA user_version=1;`); err != nil {
		db.Close()
		return nil, err
	}
	return &App{db: db, password: password, dataDir: data, webDir: web}, nil
}
func Run() error {
	a, e := New(env("MEDIASHELF_DATA_DIR", "data"), os.Getenv("MEDIASHELF_PASSWORD"), env("MEDIASHELF_WEB_DIR", "web/dist"))
	if e != nil {
		return e
	}
	defer a.db.Close()
	addr := env("MEDIASHELF_ADDR", "127.0.0.1:8765")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Background(ctx)
	log.Printf("MediaShelf listening on %s", addr)
	s := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	return s.ListenAndServe()
}
func (a *App) put(kind string, o Object) error {
	if str(o["id"]) == "" {
		o["id"] = id()
	}
	b, e := json.Marshal(o)
	if e != nil {
		return e
	}
	_, e = a.db.Exec("INSERT INTO documents(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", kind, o["id"], string(b))
	return e
}
func (a *App) get(kind, key string) (Object, error) {
	var b string
	e := a.db.QueryRow("SELECT body FROM documents WHERE kind=? AND id=?", kind, key).Scan(&b)
	if e != nil {
		return nil, e
	}
	o := Object{}
	e = json.Unmarshal([]byte(b), &o)
	return o, e
}
func (a *App) list(kind string) ([]Object, error) {
	out, err := a.listAll(kind)
	if err != nil || kind != "works" {
		return out, err
	}
	visible := make([]Object, 0, len(out))
	for _, work := range out {
		if !exclusivelyScopedOut(work) {
			visible = append(visible, visibleWork(work))
		}
	}
	return visible, nil
}

// listAll is used by backup and scan merging, where archived work documents
// must remain recoverable and retain their manual metadata.
func (a *App) listAll(kind string) ([]Object, error) {
	rows, e := a.db.Query("SELECT body FROM documents WHERE kind=? ORDER BY id", kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			return nil, e
		}
		o := Object{}
		if e = json.Unmarshal([]byte(s), &o); e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) { write(w, status, Object{"error": s}) }
func read(w http.ResponseWriter, r *http.Request) (Object, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	defer r.Body.Close()
	o := Object{}
	d := json.NewDecoder(r.Body)
	if e := d.Decode(&o); e != nil {
		fail(w, 400, "无效 JSON: "+e.Error())
		return nil, false
	}
	if o == nil {
		fail(w, 400, "需要 JSON 对象")
		return nil, false
	}
	return o, true
}
func (a *App) Handler() http.Handler { return http.HandlerFunc(a.serve) }
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.URL.Path == "/healthz" {
		write(w, 200, Object{"status": "ok"})
		return
	}
	if r.URL.Path == "/api/v1/login" {
		w.Header().Set("Cache-Control", "no-store")
		a.login(w, r)
		return
	}
	if r.URL.Path == "/api/v1/session" {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !a.authorized(r) {
			fail(w, http.StatusUnauthorized, "请登录")
			return
		}
		var expiresAt string
		err := a.db.QueryRow("SELECT expires_at FROM auth_sessions WHERE token_hash=?", digest(bearerToken(r))).Scan(&expiresAt)
		if err != nil {
			fail(w, http.StatusUnauthorized, "请登录")
			return
		}
		write(w, http.StatusOK, Object{"expires_at": expiresAt})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/agent/") {
		a.agentAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !a.authorized(r) {
			fail(w, 401, "请登录")
			return
		}
		a.api(w, r)
		return
	}
	// Covers are authenticated; browser images use a short-lived session query token.
	if strings.HasPrefix(r.URL.Path, "/covers/") {
		if !a.authorized(r) {
			fail(w, 401, "请登录")
			return
		}
		http.StripPrefix("/covers/", http.FileServer(http.Dir(filepath.Join(a.dataDir, "covers")))).ServeHTTP(w, r)
		return
	}
	clean := filepath.Clean(filepath.FromSlash(r.URL.Path))
	full := filepath.Join(a.webDir, clean)
	if _, e := os.Stat(full); e == nil && r.URL.Path != "/" {
		http.FileServer(http.Dir(a.webDir)).ServeHTTP(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.webDir, "index.html"))
}
func (a *App) authorized(r *http.Request) bool {
	token := bearerToken(r)
	if token == "" && strings.HasPrefix(r.URL.Path, "/covers/") {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return false
	}
	var raw string
	if err := a.db.QueryRow("SELECT expires_at FROM auth_sessions WHERE token_hash=?", digest(token)).Scan(&raw); err != nil {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, raw)
	return err == nil && time.Now().Before(expiresAt)
}
func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	if subtle.ConstantTimeCompare([]byte(digest(str(o["password"]))), []byte(digest(a.password))) != 1 {
		time.Sleep(300 * time.Millisecond)
		fail(w, 401, "密码错误")
		return
	}
	token := id()
	expiresAt := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if _, err := a.db.Exec("DELETE FROM auth_sessions WHERE expires_at<=?", time.Now().UTC().Format(time.RFC3339)); err != nil {
		fail(w, http.StatusInternalServerError, "会话创建失败")
		return
	}
	if _, err := a.db.Exec("INSERT INTO auth_sessions(token_hash,expires_at) VALUES(?,?)", digest(token), expiresAt); err != nil {
		fail(w, http.StatusInternalServerError, "会话创建失败")
		return
	}
	write(w, http.StatusOK, Object{"token": token, "expires_at": expiresAt})
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/"), "/")
	if a.discovery(w, r, p) {
		return
	}
	kind := p[0]
	key := ""
	if len(p) > 1 {
		key = p[1]
	}
	switch kind {
	case "works":
		if len(p) == 4 && p[2] == "files" {
			a.fileProgress(w, r, key, p[3])
			return
		}
		a.worksAPI(w, r, key)
	case "actors":
		a.actorsAPI(w, r, key)
	case "devices":
		a.devicesAPI(w, r)
	case "libraries", "tools":
		if r.Method == "POST" && len(p) == 3 {
			o, e := a.get(kind, key)
			if e != nil {
				fail(w, 404, "配置不存在")
				return
			}
			action := p[2]
			typ := "scan"
			payload := Object{"library_id": key}
			if kind == "tools" {
				typ = "launch"
				payload = Object{"tool_id": key}
			}
			if (kind == "tools" && action != "launch") || (kind == "libraries" && action != "scan") {
				fail(w, 404, "未知操作")
				return
			}
			a.enqueue(w, str(o["device_id"]), typ, payload)
			return
		}
		a.configAPI(w, r, kind, key)
	case "jobs":
		if r.Method != "GET" {
			fail(w, 405, "GET required")
			return
		}
		a.jobList(w)
	case "settings":
		if r.Method == "GET" {
			o, _ := a.get("settings", "main")
			if o == nil {
				o = Object{}
			}
			write(w, 200, o)
			return
		}
		if r.Method == "PATCH" {
			o, ok := read(w, r)
			if !ok {
				return
			}
			o["id"] = "main"
			a.saveResponse(w, "settings", o)
			return
		}
		fail(w, 405, "Method not allowed")
	case "backup":
		a.backup(w, r)
	case "restore":
		a.restore(w, r)
	default:
		a.integrationsAPI(w, r, p)
	}
}
func (a *App) saveResponse(w http.ResponseWriter, kind string, o Object) {
	if e := a.put(kind, o); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 200, o)
}
func (a *App) listResponse(w http.ResponseWriter, kind string) {
	v, e := a.list(kind)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 200, v)
}
func (a *App) configAPI(w http.ResponseWriter, r *http.Request, kind, key string) {
	if kind == "libraries" {
		a.scanMu.Lock()
		defer a.scanMu.Unlock()
	}
	if r.Method == "GET" {
		a.listResponse(w, kind)
		return
	}
	if r.Method != "POST" && r.Method != "PATCH" {
		fail(w, 405, "Method not allowed")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	if key != "" {
		o["id"] = key
	}
	if kind == "libraries" {
		// These fields belong to the scanner. Preserve them across UI edits
		// and ignore attempts to supply them in a library update.
		if existing, err := a.get("libraries", str(o["id"])); err == nil {
			for _, field := range []string{"scan_root", "root_scoped_ids"} {
				if value, exists := existing[field]; exists {
					o[field] = value
				} else {
					delete(o, field)
				}
			}
		} else {
			delete(o, "scan_root")
			delete(o, "root_scoped_ids")
		}
	}
	if str(o["device_id"]) == "" || str(o["path"]) == "" {
		fail(w, 400, "需要设备和路径")
		return
	}
	if _, e := a.get("devices", str(o["device_id"])); e != nil {
		fail(w, 400, "设备不存在")
		return
	}
	a.saveResponse(w, kind, o)
}
func (a *App) devicesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		ds, _ := a.list("devices")
		for _, d := range ds {
			delete(d, "token_hash")
			t, _ := time.Parse(time.RFC3339, str(d["last_seen"]))
			d["online"] = time.Since(t) < 90*time.Second
		}
		write(w, 200, ds)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "Method not allowed")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	token := id()
	d := Object{"id": id(), "name": str(o["name"]), "token_hash": digest(token), "last_seen": ""}
	if e := a.put("devices", d); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 201, Object{"id": d["id"], "token": token})
}
func (a *App) enqueue(w http.ResponseWriter, device, typ string, payload Object) {
	d, e := a.get("devices", device)
	if e != nil {
		fail(w, 404, "设备不存在")
		return
	}
	t, _ := time.Parse(time.RFC3339, str(d["last_seen"]))
	if time.Since(t) > 90*time.Second {
		fail(w, 409, "设备离线，请连接后重试")
		return
	}
	j := Object{"id": id(), "device_id": device, "type": typ, "payload": payload, "status": "pending", "result": "", "created_at": now()}
	a.saveResponse(w, "jobs", j)
}
func (a *App) worksAPI(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method == "GET" {
		if key != "" {
			o, e := a.get("works", key)
			if e != nil {
				fail(w, 404, "作品不存在")
				return
			}
			if exclusivelyScopedOut(o) {
				fail(w, 404, "作品不在当前片库范围")
				return
			}
			write(w, 200, visibleWork(o))
			return
		}
		items, e := a.list("works")
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		out := []Object{}
		q := strings.ToLower(r.URL.Query().Get("q"))
		for _, o := range items {
			if exclusivelyScopedOut(o) {
				continue
			}
			b, _ := json.Marshal(o)
			if q != "" && !strings.Contains(strings.ToLower(string(b)), q) {
				continue
			}
			if actor := r.URL.Query().Get("actor"); actor != "" && !contains(o["actors"], actor) {
				continue
			}
			if year := r.URL.Query().Get("year"); year != "" && fmt.Sprint(o["year"]) != year {
				continue
			}
			if status := r.URL.Query().Get("status"); status != "" && str(o["collection_status"]) != status {
				continue
			}
			out = append(out, o)
		}
		write(w, 200, out)
		return
	}
	if r.Method != "PATCH" || key == "" {
		fail(w, 405, "Method not allowed")
		return
	}
	o, e := a.get("works", key)
	if e != nil {
		fail(w, 404, "作品不存在")
		return
	}
	patch, ok := read(w, r)
	if !ok {
		return
	}
	if v, exists := patch["rating"]; exists && v != nil {
		n, ok := v.(float64)
		if !ok || n < 0 || n > 5 || n*2 != float64(int(n*2)) {
			fail(w, 400, "评分须为 0–5，步长 0.5")
			return
		}
	}
	if v, exists := patch["progress"]; exists {
		pr, ok := v.(map[string]any)
		if !ok {
			fail(w, 400, "进度无效")
			return
		}
		for _, k := range []string{"compression", "cutting", "translation"} {
			s := str(pr[k])
			if s != "pending" && s != "in_progress" && s != "done" && s != "not_needed" {
				fail(w, 400, "进度状态无效")
				return
			}
			if flag(pr["verified"]) && s != "done" && s != "not_needed" {
				fail(w, 400, "所有必要步骤完成后才能验收")
				return
			}
		}
	}
	manual := toObject(o["manual_fields"])
	for _, k := range []string{"rating", "progress", "title", "actors", "tags", "collection_status"} {
		if v, ok := patch[k]; ok {
			o[k] = v
			manual[k] = true
		}
	}
	o["manual_fields"] = manual
	if pr, ok := patch["progress"]; ok {
		fs := asObjects(o["files"])
		for _, f := range fs {
			if !flag(f["missing"]) && !flag(f["scoped_out"]) {
				f["progress"] = pr
			}
		}
		o["files"] = fs
	}
	a.saveResponse(w, "works", o)
}

func exclusivelyScopedOut(work Object) bool {
	files := asObjects(work["files"])
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !flag(f["scoped_out"]) {
			return false
		}
	}
	return true
}

func visibleWork(work Object) Object {
	files := asObjects(work["files"])
	visible := make([]Object, 0, len(files))
	for _, file := range files {
		if !flag(file["scoped_out"]) {
			visible = append(visible, file)
		}
	}
	work["files"] = visible
	return work
}

func contains(v any, s string) bool {
	switch x := v.(type) {
	case []any:
		for _, v := range x {
			if str(v) == s {
				return true
			}
		}
	case []string:
		for _, v := range x {
			if v == s {
				return true
			}
		}
	}
	return false
}
func (a *App) actorsAPI(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method == "PATCH" && key != "" {
		o, ok := read(w, r)
		if !ok {
			return
		}
		current, _ := a.get("actor_policies", key)
		if current == nil {
			current = Object{"policy": "follow", "target": 2}
		}
		if raw, exists := o["avatar_url"]; exists {
			if str(raw) != "" && avatarURL(str(raw)) == "" {
				fail(w, 400, "头像地址必须为 HTTP(S) 或已上传的图片")
				return
			}
			o["avatar_url"] = avatarURL(str(raw))
		}
		for k, v := range o {
			current[k] = v
		}
		o = current
		s := str(o["policy"])
		if s != "complete" && s != "selective" && s != "follow" {
			fail(w, 400, "策略无效")
			return
		}
		o["id"] = key
		o["name"] = key
		if num(o["target"]) < 1 {
			o["target"] = 2
		}
		a.saveResponse(w, "actor_policies", o)
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "Method not allowed")
		return
	}
	ws, _ := a.list("works")
	actors := map[string]int{}
	for _, o := range ws {
		b, _ := json.Marshal(o["actors"])
		var names []string
		_ = json.Unmarshal(b, &names)
		for _, s := range names {
			actors[s]++
		}
	}
	policies, _ := a.list("actor_policies")
	avatars := actorAvatars(ws, policies)
	for _, p := range policies {
		n := str(p["name"])
		if _, ok := actors[n]; !ok {
			actors[n] = 0
		}
	}
	out := []Object{}
	for n, c := range actors {
		o, _ := a.get("actor_policies", n)
		if o == nil {
			o = Object{"name": n, "policy": "follow", "target": 2}
		}
		o["count"] = c
		o["avatar_url"] = avatars[n]
		out = append(out, o)
	}
	write(w, 200, out)
}
func (a *App) backup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "GET required")
		return
	}
	result := Object{"schema_version": 1, "created_at": now()}
	for _, kind := range []string{"works", "actor_policies", "actor_profiles", "catalog", "feeds", "candidates", "settings", "libraries", "tools"} {
		v, e := a.listAll(kind)
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		result[kind] = v
	}
	w.Header().Set("Content-Disposition", "attachment; filename=mediashelf-backup.json")
	write(w, 200, result)
}
func (a *App) restore(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	if num(o["schema_version"]) != 1 {
		fail(w, 400, "不支持的备份版本")
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	for _, kind := range []string{"works", "actor_policies", "actor_profiles", "catalog", "feeds", "candidates", "settings", "libraries", "tools"} {
		raw, exists := o[kind]
		if kind == "actor_profiles" && !exists {
			continue // schema-v1 backups created before actor profiles remain valid.
		}
		arr, ok := raw.([]any)
		if !ok {
			fail(w, 400, "备份缺少 "+kind)
			return
		}
		for _, v := range arr {
			doc, ok := v.(map[string]any)
			if !ok || str(doc["id"]) == "" {
				fail(w, 400, "备份记录无效")
				return
			}
			b, _ := json.Marshal(doc)
			if _, e = tx.Exec("INSERT INTO documents(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", kind, doc["id"], string(b)); e != nil {
				fail(w, 500, e.Error())
				return
			}
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	write(w, 200, Object{"restored": true, "note": "凭据和封面文件需单独恢复；设备需重新配对"})
}

var _ = io.EOF
var _ = strconv.Itoa
