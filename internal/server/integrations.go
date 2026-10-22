package server

import (
	"context"
	"encoding/json"
	"fmt"
	"mediashelf/internal/integrations"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func toObject(v any) Object {
	b, _ := json.Marshal(v)
	o := Object{}
	_ = json.Unmarshal(b, &o)
	if o == nil {
		o = Object{}
	}
	return o
}
func (a *App) integrationsAPI(w http.ResponseWriter, r *http.Request, p []string) {
	kind := p[0]
	key := ""
	if len(p) > 1 {
		key = p[1]
	}
	switch kind {
	case "downloaders":
		a.downloadersAPI(w, r, p)
	case "feeds":
		if len(p) == 3 && p[2] == "refresh" && r.Method == "POST" {
			a.refreshFeed(w, r, key)
			return
		}
		if r.Method == "GET" {
			a.listResponse(w, "feeds")
			return
		}
		if r.Method == "POST" || r.Method == "PATCH" {
			o, ok := read(w, r)
			if !ok {
				return
			}
			u, e := url.Parse(str(o["url"]))
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				fail(w, 400, "RSS 地址必须是 HTTP(S)")
				return
			}
			if key != "" {
				o["id"] = key
			}
			a.saveResponse(w, "feeds", o)
			return
		}
		fail(w, 405, "Method not allowed")
	case "candidates":
		if r.Method == "GET" {
			xs, _ := a.list("candidates")
			for _, o := range xs {
				o["owned"] = a.owned(str(o["code"]))
				o["in_queue"] = str(o["status"]) == "submitted"
			}
			write(w, 200, xs)
			return
		}
		c, e := a.get("candidates", key)
		if e != nil {
			fail(w, 404, "候选不存在")
			return
		}
		if len(p) == 3 && p[2] == "download" && r.Method == "POST" {
			a.downloadCandidate(w, r, c)
			return
		}
		if r.Method == "PATCH" {
			o, ok := read(w, r)
			if !ok {
				return
			}
			if str(o["status"]) != "ignored" {
				fail(w, 400, "只能手动忽略候选")
				return
			}
			c["status"] = "ignored"
			a.saveResponse(w, "candidates", c)
			return
		}
		fail(w, 405, "Method not allowed")
	case "catalog":
		if r.Method == "GET" {
			xs, _ := a.list("catalog")
			out := []Object{}
			for _, x := range xs {
				if actor := r.URL.Query().Get("actor"); actor != "" && !contains(x["actors"], actor) && str(x["catalog_actor"]) != actor {
					continue
				}
				x["owned"] = a.owned(str(x["code"]))
				out = append(out, x)
			}
			write(w, 200, out)
			return
		}
		if key == "sync" && r.Method == "POST" {
			a.syncCatalog(w, r)
			return
		}
		fail(w, 405, "Method not allowed")
	case "recommendations", "actor-recommendations":
		a.recommend(w, kind)
	case "sources":
		if r.Method == "GET" {
			xs, _ := a.list("sources")
			for _, x := range xs {
				x["cookie_configured"] = str(x["cookie"]) != ""
				delete(x, "cookie")
			}
			write(w, 200, xs)
			return
		}
		if r.Method == "POST" || r.Method == "PATCH" {
			o, ok := read(w, r)
			if !ok {
				return
			}
			if proxy := str(o["proxy_url"]); proxy != "" {
				u, e := url.Parse(proxy)
				if e != nil || u.Host == "" || u.User != nil {
					fail(w, 400, "代理 URL 不支持内嵌凭据，请使用本机代理端口")
					return
				}
			}
			o["id"] = str(o["source"])
			if _, present := o["cookie"]; !present {
				old, _ := a.get("sources", str(o["id"]))
				o["cookie"] = old["cookie"]
			}
			if str(o["id"]) == "" {
				fail(w, 400, "需要来源名称")
				return
			}
			if e := a.put("sources", o); e != nil {
				fail(w, 500, e.Error())
				return
			}
			o["cookie_configured"] = str(o["cookie"]) != ""
			delete(o, "cookie")
			write(w, 200, o)
			return
		}
		fail(w, 405, "Method not allowed")
	case "catalog-syncs":
		a.listResponse(w, "catalog_syncs")
	default:
		fail(w, 404, "接口不存在")
	}
}
func (a *App) owned(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	// Preserve the common-case indexed lookup. The full scan below is needed
	// only when the submitted spelling differs from the stored work ID or its
	// exact record has no available files.
	if w, e := a.get("works", "code:"+strings.ToUpper(code)); e == nil && hasAvailableWorkFile(w) {
		return true
	}
	works, e := a.list("works")
	if e != nil {
		return false
	}
	for _, w := range works {
		if !sameWorkCode(str(w["code"]), code) {
			continue
		}
		if hasAvailableWorkFile(w) {
			return true
		}
	}
	return false
}

func hasAvailableWorkFile(work Object) bool {
	for _, f := range asObjects(work["files"]) {
		if !flag(f["missing"]) {
			return true
		}
	}
	return false
}
func (a *App) downloadersAPI(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) == 1 {
		if r.Method == "GET" {
			xs, _ := a.list("downloaders")
			for _, x := range xs {
				x["password_configured"] = str(x["password"]) != ""
				delete(x, "password")
			}
			write(w, 200, xs)
			return
		}
		if r.Method == "POST" {
			o, ok := read(w, r)
			if !ok {
				return
			}
			if _, e := integrations.NewQBittorrent(integrations.QBConfig{URL: str(o["url"])}); e != nil {
				fail(w, 400, e.Error())
				return
			}
			if str(o["id"]) != "" && str(o["password"]) == "" {
				old, _ := a.get("downloaders", str(o["id"]))
				o["password"] = old["password"]
			}
			if str(o["management_mode"]) == "uncategorized" {
				tag := downloaderTag(o)
				if len(tag) > 100 || strings.ContainsAny(tag, ",|\r\n") {
					fail(w, 400, "管理标签必须为单个标签，不能包含逗号、竖线或换行")
					return
				}
				o["managed_tag"] = tag
			}
			if e := a.put("downloaders", o); e != nil {
				fail(w, 500, e.Error())
				return
			}
			delete(o, "password")
			write(w, 200, o)
			return
		}
		fail(w, 405, "Method not allowed")
		return
	}
	d, e := a.get("downloaders", p[1])
	if e != nil {
		fail(w, 404, "下载器不存在")
		return
	}
	if len(p) != 3 {
		fail(w, 404, "接口不存在")
		return
	}
	op := p[2]
	params := Object{}
	if op != "torrents" && op != "categories" {
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		var ok bool
		params, ok = read(w, r)
		if !ok {
			return
		}
	}
	if op == "add" && a.owned(str(params["code"])) && !flag(params["allow_existing"]) {
		fail(w, 409, "本地已有此作品，请先检查版本")
		return
	}
	if op == "add" {
		a.downloadMu.Lock()
		defer a.downloadMu.Unlock()
		ts, err := a.qbit(r.Context(), d, "torrents", Object{})
		if err != nil {
			fail(w, 502, "查重失败："+err.Error())
			return
		}
		if queueDuplicate(ts, str(params["url"]), str(params["code"])) {
			fail(w, 409, "下载队列已有相同作品或种子")
			return
		}
	}
	result, e := a.qbit(r.Context(), d, op, params)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	write(w, 200, result)
}
func stringSlice(v any) []string {
	if s, ok := v.(string); ok {
		return strings.Split(s, "|")
	}
	b, _ := json.Marshal(v)
	var out []string
	_ = json.Unmarshal(b, &out)
	return out
}
func downloaderTag(d Object) string {
	if str(d["management_mode"]) != "uncategorized" {
		return ""
	}
	if tag := strings.TrimSpace(str(d["managed_tag"])); tag != "" {
		return tag
	}
	return "MediaShelf"
}

func appendManagedTag(tags, required string) string {
	for _, tag := range strings.Split(tags, ",") {
		if strings.TrimSpace(tag) == required {
			return tags
		}
	}
	if strings.TrimSpace(tags) == "" {
		return required
	}
	return tags + "," + required
}

func (a *App) qbit(ctx context.Context, d Object, op string, params Object) (any, error) {
	if flag(params["delete_files"]) {
		return nil, fmt.Errorf("仅支持删除任务并保留数据文件")
	}
	tag := downloaderTag(d)
	if tag != "" {
		if op == "categories" {
			return Object{}, nil
		}
		if op == "torrents" {
			params["tag"] = tag
		}
		if op == "create_tags" {
			params["tags"] = tag
		}
		if op == "add" {
			params["tags"] = appendManagedTag(str(params["tags"]), tag)
		}
		if op == "action" {
			// Verify every hash against the scoped upstream list before any mutation.
			torrents, err := a.qbit(ctx, d, "torrents", Object{})
			if err != nil {
				return nil, err
			}
			allowed := map[string]bool{}
			for _, torrent := range asObjects(torrents) {
				allowed[strings.ToLower(str(torrent["hash"]))] = true
			}
			hashes := stringSlice(params["hashes"])
			if len(hashes) == 0 {
				return nil, fmt.Errorf("请选择任务")
			}
			for _, hash := range hashes {
				if !allowed[strings.ToLower(hash)] {
					return nil, fmt.Errorf("只能管理带有 %s 标签的任务", tag)
				}
			}
		}
	}
	if op == "add" {
		if str(d["management_mode"]) == "categorical" {
			if str(params["category"]) == "" {
				return nil, fmt.Errorf("本机自动管理模式需要选择分类")
			}
			params["auto_tmm"] = true
			delete(params, "save_path")
		} else {
			params["category"] = ""
			params["auto_tmm"] = false
		}
	}
	if device := str(d["device_id"]); device != "" {
		if op == "add" && tag != "" {
			if _, err := a.qbit(ctx, d, "create_tags", Object{}); err != nil {
				return nil, err
			}
		}
		return a.remoteQbit(ctx, d, op, params)
	}
	q, e := integrations.NewQBittorrent(integrations.QBConfig{URL: str(d["url"]), Username: str(d["username"]), Password: str(d["password"])})
	if e != nil {
		return nil, e
	}
	if e = q.Login(ctx); e != nil {
		return nil, e
	}
	switch op {
	case "torrents":
		return q.ListTorrentsFiltered(ctx, "", tag)
	case "categories":
		return q.ListCategories(ctx)
	case "create_tags":
		e = q.CreateTags(ctx, tag)
	case "add":
		if tag != "" {
			if e = q.CreateTags(ctx, tag); e != nil {
				return nil, e
			}
		}
		e = q.Add(ctx, integrations.AddTorrent{URL: str(params["url"]), SavePath: str(params["save_path"]), Category: str(params["category"]), Tags: str(params["tags"]), Paused: false, AutoTMM: boolPointer(params["auto_tmm"])})
	case "action":
		hashes := stringSlice(params["hashes"])
		if len(hashes) == 0 {
			return nil, fmt.Errorf("请选择任务")
		}
		switch str(params["action"]) {
		case "start":
			e = q.SetRunning(ctx, hashes, true)
		case "stop", "pause":
			e = q.SetRunning(ctx, hashes, false)
		case "delete":
			e = q.Delete(ctx, hashes, flag(params["delete_files"]))
		case "category":
			e = q.SetCategory(ctx, hashes, str(params["category"]))
		case "tags":
			e = q.SetTags(ctx, hashes, str(params["tags"]))
		case "limits":
			e = q.SetLimits(ctx, hashes, int64(num(params["download_limit"])), int64(num(params["upload_limit"])))
		default:
			e = fmt.Errorf("未知下载操作")
		}
	default:
		e = fmt.Errorf("未知下载接口")
	}
	if e != nil {
		return nil, e
	}
	return Object{"ok": true}, nil
}
func (a *App) remoteQbit(ctx context.Context, d Object, op string, params Object) (any, error) {
	device, e := a.get("devices", str(d["device_id"]))
	if e != nil {
		return nil, fmt.Errorf("设备不存在")
	}
	t, _ := time.Parse(time.RFC3339, str(device["last_seen"]))
	if time.Since(t) > 90*time.Second {
		return nil, fmt.Errorf("设备离线")
	}
	jid := id()
	j := Object{"id": jid, "device_id": d["device_id"], "type": "qbit", "status": "pending", "created_at": now(), "payload": Object{"request_id": jid, "downloader_id": d["id"], "operation": op, "config": Object{"url": d["url"], "username": d["username"], "password": d["password"]}, "params": params}}
	if e = a.put("jobs", j); e != nil {
		return nil, e
	}
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("设备请求超时，请在任务记录中确认结果后再重试")
		case <-ticker.C:
			x, _ := a.get("jobs", jid)
			switch str(x["status"]) {
			case "done":
				r := toObject(x["result"])
				return r["data"], nil
			case "failed", "expired":
				return nil, fmt.Errorf("设备请求失败: %v", x["result"])
			}
		}
	}
}
func (a *App) refreshFeed(w http.ResponseWriter, r *http.Request, key string) {
	f, e := a.get("feeds", key)
	if e != nil {
		fail(w, 404, "订阅不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	rules := integrations.FeedRules{}
	if x := str(f["include"]); x != "" {
		rules.Include = []string{x}
	}
	if x := str(f["exclude"]); x != "" {
		rules.Exclude = []string{x}
	}
	items, e := integrations.FetchFeed(ctx, str(f["url"]), rules)
	if e != nil {
		f["last_error"] = e.Error()
		_ = a.put("feeds", f)
		fail(w, 502, e.Error())
		return
	}
	for _, it := range items {
		o := toObject(it)
		o["id"] = digest(it.URL)
		o["source"] = f["name"]
		o["status"] = "new"
		old, _ := a.get("candidates", str(o["id"]))
		if old != nil {
			o["status"] = old["status"]
		}
		if e = a.put("candidates", o); e != nil {
			fail(w, 500, e.Error())
			return
		}
	}
	f["last_refresh"] = now()
	_ = a.put("feeds", f)
	write(w, 200, Object{"count": len(items)})
}
func (a *App) downloadCandidate(w http.ResponseWriter, r *http.Request, c Object) {
	o, ok := read(w, r)
	if !ok {
		return
	}
	a.downloadMu.Lock()
	defer a.downloadMu.Unlock()
	latest, _ := a.get("candidates", str(c["id"]))
	if str(latest["status"]) == "submitted" || str(latest["status"]) == "submitting" || str(latest["status"]) == "uncertain" {
		fail(w, 409, "候选已提交，请检查下载队列")
		return
	}
	if a.owned(str(c["code"])) {
		fail(w, 409, "本地已有该作品，请检查版本")
		return
	}
	d, e := a.get("downloaders", str(o["downloader_id"]))
	if e != nil {
		fail(w, 400, "下载器不存在")
		return
	}
	ts, err := a.qbit(r.Context(), d, "torrents", Object{})
	if err != nil {
		fail(w, 502, "查重失败："+err.Error())
		return
	}
	if queueDuplicate(ts, str(c["url"]), str(c["code"])) {
		fail(w, 409, "下载队列已有相同作品或种子")
		return
	}
	c["status"] = "submitting"
	if e = a.put("candidates", c); e != nil {
		fail(w, 500, e.Error())
		return
	}
	o["url"] = c["url"]
	result, e := a.qbit(r.Context(), d, "add", o)
	if e != nil {
		c["status"] = "uncertain"
		c["error"] = "提交结果不确定，请检查下载器后重试"
		_ = a.put("candidates", c)
		fail(w, 502, e.Error())
		return
	}
	c["status"] = "submitted"
	c["downloader_id"] = d["id"]
	_ = a.put("candidates", c)
	write(w, 200, result)
}
func (a *App) syncCatalog(w http.ResponseWriter, r *http.Request) {
	o, ok := read(w, r)
	if !ok {
		return
	}
	actor := strings.TrimSpace(str(o["actor"]))
	source := str(o["source"])
	if source == "javlib" {
		source = "javlibrary"
	}
	if actor == "" {
		fail(w, 400, "需要演员名称或 ID")
		return
	}
	config, _ := a.get("sources", source)
	client, e := integrations.NewCatalogClient(integrations.CatalogConfig{Source: source, BaseURL: str(config["base_url"]), ProxyURL: str(config["proxy_url"]), Cookie: str(config["cookie"])})
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	status := Object{"id": source + ":" + actor, "actor": actor, "source": source, "complete": false, "last_sync": now(), "count": 0}
	count := 0
	target := actor
	visited := map[string]bool{}
	for page := 1; page <= 50; page++ {
		result, err := client.Catalog(r.Context(), target, 1)
		if err != nil {
			status["error"] = err.Error()
			_ = a.put("catalog_syncs", status)
			fail(w, 502, fmt.Sprintf("已同步 %d 项，尚未完成：%s", count, err))
			return
		}
		for _, m := range result.Works {
			x := toObject(m)
			x["id"] = source + ":" + m.Code
			if m.Code == "" {
				x["id"] = source + ":" + digest(m.SourceURL)
			}
			x["catalog_actor"] = actor
			x["fetched_at"] = now()
			if !contains(x["actors"], actor) {
				names := stringSlice(x["actors"])
				x["actors"] = append(names, actor)
			}
			if e = a.put("catalog", x); e != nil {
				fail(w, 500, e.Error())
				return
			}
			count++
		}
		status["count"] = count
		status["pages"] = page
		if !result.HasMore {
			status["complete"] = result.Complete
			break
		}
		if result.NextURL == "" || visited[result.NextURL] {
			status["error"] = "分页链接缺失或重复，目录未完成"
			break
		}
		visited[result.NextURL] = true
		target = result.NextURL
		select {
		case <-r.Context().Done():
			fail(w, 408, "同步取消")
			return
		case <-time.After(700 * time.Millisecond):
		}
	}
	_ = a.put("catalog_syncs", status)
	write(w, 200, status)
}
func (a *App) recommend(w http.ResponseWriter, kind string) {
	ws, _ := a.list("works")
	catalog, _ := a.list("catalog")
	tags := map[string]float64{}
	actors := map[string]float64{}
	known := map[string]bool{}
	for _, x := range ws {
		weight := 1.0
		if v, ok := x["rating"].(float64); ok {
			weight = (v - 2.5) / 2.5
		}
		for _, s := range stringSlice(x["actors"]) {
			actors[s] += weight
			known[s] = true
		}
		for _, s := range stringSlice(x["tags"]) {
			tags[s] += weight
		}
	}
	ignored, _ := a.list("candidates")
	ignore := map[string]bool{}
	for _, c := range ignored {
		if str(c["status"]) == "ignored" || str(c["status"]) == "submitted" {
			ignore[str(c["code"])] = true
		}
	}
	out := []Object{}
	actorScores := map[string]float64{}
	seen := map[string]bool{}
	for _, x := range catalog {
		code := str(x["code"])
		if code == "" || seen[code] || a.owned(code) || ignore[code] {
			continue
		}
		seen[code] = true
		score := 0.0
		reasons := []string{}
		for _, s := range stringSlice(x["actors"]) {
			if actors[s] > 0 {
				score += actors[s]
				reasons = append(reasons, "收藏偏好演员："+s)
			}
		}
		for _, s := range stringSlice(x["tags"]) {
			if tags[s] > 0 {
				score += tags[s] * 0.5
				reasons = append(reasons, "偏好标签："+s)
			}
		}
		rating := num(x["rating"])
		if rating > 0 {
			score += rating * 0.3
			reasons = append(reasons, fmt.Sprintf("来源折算评分 %.1f/5", rating))
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "关注演员目录中的未收藏作品")
		}
		out = append(out, Object{"work": x, "reason": strings.Join(reasons, "；"), "score": score})
		for _, s := range stringSlice(x["actors"]) {
			if !known[s] {
				actorScores[s] += score + 1
			}
		}
	}
	if kind == "actor-recommendations" {
		out = []Object{}
		for s, n := range actorScores {
			out = append(out, Object{"name": s, "score": n, "reason": "与现有收藏偏好相关的目录作品"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return num(out[i]["score"]) > num(out[j]["score"]) })
	if len(out) > 100 {
		out = out[:100]
	}
	write(w, 200, out)
}

func boolPointer(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
