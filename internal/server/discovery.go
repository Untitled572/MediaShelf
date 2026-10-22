package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mediashelf/internal/integrations"
	"net/http"
	"sort"
	"strings"
	"time"
)

// discovery handles catalog search, actor selection, aliases, enrichment and
// preference-based discovery routes. It returns false for routes it does not own.
func (a *App) discovery(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) == 1 && p[0] == "images" {
		a.serveRemoteImage(w, r)
		return true
	}
	if len(p) == 2 && p[0] == "catalog" && p[1] == "refresh" {
		a.refreshPublicRecommendations(w, r)
		return true
	}
	if len(p) == 1 && p[0] == "recommendation-status" {
		if r.Method != http.MethodGet {
			fail(w, 405, "GET required")
			return true
		}
		catalog, e := a.list("catalog")
		if e != nil {
			fail(w, 500, e.Error())
			return true
		}
		checks, e := a.list("source_checks")
		if e != nil {
			fail(w, 500, e.Error())
			return true
		}
		write(w, 200, Object{"catalog_count": len(catalog), "source_checks": checks})
		return true
	}
	if len(p) == 1 && p[0] == "search" {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
			return true
		}
		o, ok := read(w, r)
		if !ok {
			return true
		}
		query := strings.TrimSpace(str(o["query"]))
		source := normalizeSource(str(o["source"]))
		if query == "" || source == "" {
			fail(w, http.StatusBadRequest, "需要查询词和数据源")
			return true
		}
		m, err := a.lookup(r.Context(), source, query)
		check := Object{"id": source, "source": source, "checked_at": now(), "ok": err == nil}
		if err != nil {
			check["error"] = err.Error()
		}
		_ = a.put("source_checks", check)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return true
		}
		x := toObject(m)
		x["id"] = catalogID(source, m.Code)
		x["fetched_at"] = now()
		if err = a.mergeCatalog(x); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return true
		}
		write(w, http.StatusOK, x)
		return true
	}
	if len(p) == 1 && (p[0] == "recommendations" || p[0] == "actor-recommendations") {
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "GET required")
		} else {
			a.preferenceRecommendations(w, p[0] == "actor-recommendations")
		}
		return true
	}
	if len(p) == 3 && p[0] == "actors" && p[2] == "selection" {
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "GET required")
		} else {
			a.actorSelection(w, p[1])
		}
		return true
	}
	if len(p) == 3 && p[0] == "actors" && p[2] == "profile" {
		switch r.Method {
		case http.MethodGet:
			a.getActorProfile(w, p[1])
		case http.MethodPost:
			a.refreshActorProfile(w, r, p[1])
		default:
			fail(w, http.StatusMethodNotAllowed, "GET or POST required")
		}
		return true
	}
	if len(p) == 4 && p[0] == "actors" && p[2] == "profile" && p[3] == "translate" {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
		} else {
			a.translateActorProfile(w, r, p[1])
		}
		return true
	}
	if len(p) == 3 && p[0] == "actors" && p[2] == "aliases" {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
		} else {
			a.updateActorAliases(w, r, p[1])
		}
		return true
	}
	if len(p) == 3 && p[0] == "catalog" && p[2] == "enrich" {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST required")
		} else {
			a.enrichCatalog(w, r, p[1])
		}
		return true
	}
	return false
}

func normalizeSource(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "javlib" {
		return "javlibrary"
	}
	return s
}

func catalogID(source, code string) string {
	return source + ":" + strings.ToUpper(strings.TrimSpace(code))
}

func (a *App) lookup(ctx context.Context, source, code string) (integrations.Metadata, error) {
	cfg, _ := a.get("sources", source)
	proxyURL, err := a.outboundProxy()
	if err != nil {
		return integrations.Metadata{}, err
	}
	if source == "javdbui" {
		client, err := integrations.NewJavDBUIClient(integrations.CatalogConfig{BaseURL: str(cfg["base_url"]), ProxyURL: proxyURL})
		if err != nil {
			return integrations.Metadata{}, err
		}
		return client.Lookup(ctx, code)
	}
	c, err := integrations.NewCatalogClient(integrations.CatalogConfig{
		Source: source, BaseURL: str(cfg["base_url"]), ProxyURL: proxyURL, Cookie: str(cfg["cookie"]),
	})
	if err != nil {
		return integrations.Metadata{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.Lookup(ctx, code)
}

func (a *App) mergeCatalog(incoming Object) error {
	id := str(incoming["id"])
	old, _ := a.get("catalog", id)
	if old != nil {
		for _, key := range []string{"title", "year", "actors", "tags", "rating", "source_rating", "rating_count", "rating_raw", "rating_count_raw", "rating_scale", "cover_url", "thumbnail_url", "source_url"} {
			if isEmpty(incoming[key]) {
				incoming[key] = old[key]
			}
		}
	}
	return a.put("catalog", incoming)
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	if n, ok := v.(float64); ok {
		return n == 0
	}
	return false
}

func actorPolicies(a *App) map[string]Object {
	rows, _ := a.list("actor_policies")
	out := make(map[string]Object, len(rows))
	for _, p := range rows {
		name := actorKey(str(p["name"]))
		if name == "" {
			name = actorKey(str(p["id"]))
		}
		if name != "" {
			out[name] = p
		}
	}
	return out
}

func actorKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func actorNames(v any) []string {
	return stringSlice(v)
}

func aliasMap(policies map[string]Object) map[string]string {
	result := map[string]string{}
	for key, p := range policies {
		canonical := str(p["name"])
		if canonical == "" {
			canonical = str(p["id"])
		}
		result[key] = canonical
		for _, alias := range actorNames(p["aliases"]) {
			if k := actorKey(alias); k != "" {
				result[k] = canonical
			}
		}
	}
	return result
}

func canonicalActor(s string, aliases map[string]string) string {
	if canonical := aliases[actorKey(s)]; canonical != "" {
		return canonical
	}
	return strings.TrimSpace(s)
}

func hasActor(work Object, canonical string, aliases map[string]string) bool {
	target := actorKey(canonical)
	for _, name := range actorNames(work["actors"]) {
		if actorKey(canonicalActor(name, aliases)) == target {
			return true
		}
	}
	if actorKey(canonicalActor(str(work["catalog_actor"]), aliases)) == target {
		return true
	}
	return false
}

func (a *App) actorSelection(w http.ResponseWriter, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		fail(w, http.StatusBadRequest, "需要演员名称")
		return
	}
	policies := actorPolicies(a)
	aliases := aliasMap(policies)
	canonical := canonicalActor(name, aliases)
	policy := policies[actorKey(canonical)]
	if policy == nil {
		policy = Object{"name": canonical, "policy": "follow", "target": 2}
	}
	localRows, _ := a.list("works")
	local := []Object{}
	for _, x := range localRows {
		if hasActor(x, canonical, aliases) {
			local = append(local, x)
		}
	}
	cat, _ := a.list("catalog")
	suggested := []Object{}
	sources := map[string]bool{}
	owned := 0
	byCode := map[string]Object{}
	for _, x := range cat {
		if !hasActor(x, canonical, aliases) {
			continue
		}
		if s := str(x["source"]); s != "" {
			sources[s] = true
		}
		key := strings.ToUpper(strings.TrimSpace(str(x["code"])))
		if key == "" {
			key = str(x["id"])
		}
		if old := byCode[key]; old == nil || num(x["rating"]) > num(old["rating"]) {
			byCode[key] = x
		}
	}
	for _, x := range byCode {
		if a.owned(str(x["code"])) {
			owned++
			continue
		}
		z := toObject(x)
		z["owned"] = false
		compilation := isCompilation(x)
		z["is_compilation"] = compilation
		z["selection_reason"] = selectionReason(x, compilation)
		suggested = append(suggested, z)
	}
	sort.SliceStable(suggested, func(i, j int) bool {
		ci, cj := flag(suggested[i]["is_compilation"]), flag(suggested[j]["is_compilation"])
		if ci != cj {
			return !ci
		}
		return num(suggested[i]["rating"]) > num(suggested[j]["rating"])
	})
	for i, item := range suggested {
		recommended := str(policy["policy"]) != "selective" && !flag(item["is_compilation"])
		if str(policy["policy"]) == "selective" && i < actorTarget(policy) && !flag(item["is_compilation"]) {
			recommended = true
		}
		item["recommended"] = recommended
	}
	sourceList := make([]string, 0, len(sources))
	for source := range sources {
		sourceList = append(sourceList, source)
	}
	sort.Strings(sourceList)
	total := owned + len(suggested)
	coverage := Object{"owned": owned, "total": total, "partial": total > owned && owned > 0, "complete": total > 0 && owned == total}
	// A provider sync is complete only when every known source sync is marked complete.
	syncs, _ := a.list("catalog_syncs")
	synced := map[string]bool{}
	for _, s := range syncs {
		if actorKey(canonicalActor(str(s["actor"]), aliases)) == actorKey(canonical) && flag(s["complete"]) {
			synced[str(s["source"])] = true
		}
	}
	coverage["complete"] = len(sourceList) > 0 && len(synced) > 0
	for _, source := range sourceList {
		if !synced[source] {
			coverage["complete"] = false
		}
	}
	write(w, http.StatusOK, Object{"policy": str(policy["policy"]), "target": actorTarget(policy), "local": local, "suggested": suggested, "coverage": coverage, "sources": sourceList})
}

func actorTarget(p Object) int {
	t := int(num(p["target"]))
	if t < 1 {
		t = 2
	}
	if t > 100 {
		t = 100
	}
	return t
}

var compilationHints = []string{"compilation", "best of", "collection", "総集編", "総集", "ベスト", "合集", "精选", "精選", "合辑", "合輯", "精选集", "多人"}

func isCompilation(x Object) bool {
	s := strings.ToLower(str(x["title"]) + " " + strings.Join(actorNames(x["tags"]), " "))
	for _, hint := range compilationHints {
		if strings.Contains(s, strings.ToLower(hint)) {
			return true
		}
	}
	return false
}

func selectionReason(x Object, compilation bool) string {
	if compilation {
		return "合集或多人标签，建议人工确认"
	}
	if num(x["rating"]) >= 4 {
		return "普通单体作品且评分较高"
	}
	return "普通作品，按演员目录顺序查看"
}

func (a *App) updateActorAliases(w http.ResponseWriter, r *http.Request, name string) {
	canonical := strings.TrimSpace(name)
	if canonical == "" {
		fail(w, http.StatusBadRequest, "需要规范演员名")
		return
	}
	o, ok := read(w, r)
	if !ok {
		return
	}
	requested := actorNames(o["aliases"])
	if len(requested) == 0 {
		fail(w, http.StatusBadRequest, "aliases 必须包含至少一个名称")
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
	policies, err := txList(tx, "actor_policies")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	policyByName := map[string]Object{}
	for _, p := range policies {
		key := actorKey(str(p["name"]))
		if key == "" {
			key = actorKey(str(p["id"]))
		}
		policyByName[key] = p
	}
	canonicalKey := actorKey(canonical)
	policy := policyByName[canonicalKey]
	if policy == nil {
		policy = Object{"id": canonical, "name": canonical, "policy": "follow", "target": 2}
	}
	known := map[string]string{canonicalKey: canonical}
	for _, p := range policies {
		pn := str(p["name"])
		if pn == "" {
			pn = str(p["id"])
		}
		known[actorKey(pn)] = pn
		for _, al := range actorNames(p["aliases"]) {
			known[actorKey(al)] = pn
		}
	}
	aliases := actorNames(policy["aliases"])
	for _, raw := range requested {
		alias := strings.TrimSpace(raw)
		k := actorKey(alias)
		if k == "" {
			continue
		}
		if k == canonicalKey {
			continue
		}
		if other := known[k]; other != "" && actorKey(other) != canonicalKey {
			fail(w, http.StatusConflict, fmt.Sprintf("%q 已属于演员 %s", alias, other))
			return
		}
		aliases = appendUnique(aliases, alias)
	}
	if len(aliases) == 0 {
		fail(w, http.StatusBadRequest, "没有有效别名")
		return
	}
	policy["id"], policy["name"], policy["aliases"] = canonical, canonical, aliases
	pb, _ := json.Marshal(policy)
	if _, err = tx.Exec("INSERT INTO documents(kind,id,body) VALUES('actor_policies',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", canonical, string(pb)); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, kind := range []string{"works", "catalog"} {
		rows, e := txList(tx, kind)
		if e != nil {
			fail(w, http.StatusInternalServerError, e.Error())
			return
		}
		for _, row := range rows {
			changed := false
			names := actorNames(row["actors"])
			updated := []string{}
			for _, actor := range names {
				if containsActor(requested, actor) || actorKey(actor) == canonicalKey {
					actor = canonical
					changed = true
				}
				updated = appendUnique(updated, actor)
			}
			if changed {
				row["actors"] = updated
			}
			if kind == "catalog" && containsActor(requested, str(row["catalog_actor"])) {
				row["catalog_actor"] = canonical
				changed = true
			}
			if changed {
				b, _ := json.Marshal(row)
				if _, err = tx.Exec("UPDATE documents SET body=? WHERE kind=? AND id=?", string(b), kind, row["id"]); err != nil {
					fail(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, Object{"name": canonical, "aliases": aliases, "merged": true})
}

func containsActor(list []string, name string) bool {
	key := actorKey(name)
	for _, item := range list {
		if actorKey(item) == key && key != "" {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func txList(tx *sql.Tx, kind string) ([]Object, error) {
	rows, err := tx.Query("SELECT body FROM documents WHERE kind=? ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		o := Object{}
		if err = json.Unmarshal([]byte(raw), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (a *App) enrichCatalog(w http.ResponseWriter, r *http.Request, id string) {
	old, err := a.get("catalog", id)
	if err != nil {
		fail(w, http.StatusNotFound, "目录作品不存在")
		return
	}
	source := normalizeSource(str(old["source"]))
	code := str(old["code"])
	if source == "" || code == "" {
		fail(w, http.StatusBadRequest, "目录作品缺少来源或番号")
		return
	}
	m, err := a.lookup(r.Context(), source, code)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	old = mergeEnrichedCatalogMetadata(old, m)
	old["enriched_at"] = now()
	if err = a.put("catalog", old); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, old)
}

func mergeEnrichedCatalogMetadata(old Object, m integrations.Metadata) Object {
	old["actors"] = mergeStrings(actorNames(old["actors"]), m.Actors)
	old["tags"] = mergeStrings(actorNames(old["tags"]), m.Tags)
	for _, key := range []string{"title", "year", "rating", "source_rating", "rating_count", "rating_raw", "rating_count_raw", "rating_scale", "thumbnail_url", "source_url"} {
		if isEmpty(old[key]) && !isEmpty(metadataField(m, key)) {
			old[key] = metadataField(m, key)
		}
	}
	// Enrichment fetches a provider's detail page, where cover_url is the full
	// cover. Older catalog rows may have stored the smaller list image there.
	if !isEmpty(m.CoverURL) {
		old["cover_url"] = m.CoverURL
	}
	return old
}

func metadataField(m integrations.Metadata, key string) any {
	x := toObject(m)
	return x[key]
}

func mergeStrings(a []string, b []string) []string {
	out := append([]string{}, a...)
	for _, s := range b {
		s = strings.TrimSpace(s)
		if s != "" {
			out = appendUnique(out, s)
		}
	}
	return out
}

func (a *App) preferenceRecommendations(w http.ResponseWriter, actorOnly bool) {
	works, err := a.list("works")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ownedCodes := recommendationOwnedCodeIndex(works)
	catalog, err := a.list("catalog")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	policies := actorPolicies(a)
	aliases := aliasMap(policies)
	actorPreference, tagPreference := preferenceWeights(works, aliases)
	knownActors := map[string]bool{}
	for _, work := range works {
		for _, name := range actorNames(work["actors"]) {
			knownActors[actorKey(canonicalActor(name, aliases))] = true
		}
	}
	ignored := map[string]bool{}
	for _, c := range mustList(a, "candidates") {
		if str(c["status"]) == "ignored" || str(c["status"]) == "submitted" || str(c["status"]) == "submitting" {
			ignored[strings.ToUpper(str(c["code"]))] = true
		}
	}
	items := []Object{}
	seenCodes := map[string]bool{}
	coactors := map[string]float64{}
	coactorWorks := map[string]map[string]bool{}
	for _, x := range catalog {
		code := strings.ToUpper(str(x["code"]))
		if code == "" {
			continue
		}
		actors := actorNames(x["actors"])
		if actor := str(x["catalog_actor"]); actor != "" {
			actors = appendUnique(actors, actor)
		}
		sharesKnownActor := false
		for _, name := range actors {
			if knownActors[actorKey(canonicalActor(name, aliases))] {
				sharesKnownActor = true
				break
			}
		}
		if ownedCodes.contains(code) || ignored[code] {
			if sharesKnownActor {
				for _, name := range actors {
					canon := canonicalActor(name, aliases)
					k := actorKey(canon)
					if k == "" || knownActors[k] {
						continue
					}
					if coactorWorks[k] == nil {
						coactorWorks[k] = map[string]bool{}
					}
					if !coactorWorks[k][code] {
						coactorWorks[k][code] = true
						coactors[k]++
					}
				}
			}
			continue
		}
		if seenCodes[code] {
			continue
		}
		seenCodes[code] = true
		score := num(x["rating"]) * .25 // Provider-normalized 0-5 rating.
		reasons := []string{}
		if sharesKnownActor {
			for _, name := range actors {
				k := actorKey(canonicalActor(name, aliases))
				if weight := actorPreference[k]; weight > 0 {
					score += weight
					reasons = appendUnique(reasons, "偏好演员："+canonicalActor(name, aliases))
				}
			}
		}
		for _, tag := range actorNames(x["tags"]) {
			if weight := tagPreference[actorKey(tag)]; weight > 0 {
				score += weight * .5
				reasons = appendUnique(reasons, "偏好标签："+tag)
			}
		}
		if num(x["rating"]) > 0 {
			reasons = appendUnique(reasons, fmt.Sprintf("来源折算评分 %.1f/5", num(x["rating"])))
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "尚无足够个人偏好数据")
		}
		if sharesKnownActor {
			for _, name := range actors {
				canon := canonicalActor(name, aliases)
				k := actorKey(canon)
				if k == "" || knownActors[k] {
					continue
				}
				if coactorWorks[k] == nil {
					coactorWorks[k] = map[string]bool{}
				}
				if !coactorWorks[k][code] {
					coactorWorks[k][code] = true
					coactors[k] += 1 + score
				}
			}
		}
		item := toObject(x)
		item["recommendation_score"] = score
		item["recommendation_reason"] = strings.Join(reasons, "；")
		items = append(items, item)
	}
	if actorOnly {
		out := []Object{}
		genders := a.actorGenderIndex(aliases)
		canonicalNames := map[string]string{}
		for _, x := range catalog {
			for _, n := range actorNames(x["actors"]) {
				canon := canonicalActor(n, aliases)
				if !knownActors[actorKey(canon)] {
					canonicalNames[actorKey(canon)] = canon
				}
			}
		}
		for k, score := range coactors {
			if n := canonicalNames[k]; n != "" && genders[actorKey(n)] == "female" {
				out = append(out, Object{"name": n, "score": score, "reason": "與收藏偏好演員共同出現在目錄作品中", "shared_works": len(coactorWorks[k])})
			}
		}
		sort.Slice(out, func(i, j int) bool { return num(out[i]["score"]) > num(out[j]["score"]) })
		if len(out) < 3 {
			selected := map[string]bool{}
			for _, item := range out {
				selected[actorKey(str(item["name"]))] = true
			}
			for _, actor := range a.providerActorRecommendations() {
				name := canonicalActor(str(actor["name"]), aliases)
				key := actorKey(name)
				if name == "" || selected[key] || genders[key] != "female" {
					continue
				}
				selected[key] = true
				out = append(out, Object{"name": name, "score": num(actor["score"]), "reason": str(actor["reason"])})
				if len(out) == 3 {
					break
				}
			}
		}
		if len(out) > 100 {
			out = out[:100]
		}
		write(w, http.StatusOK, out)
		return
	}
	// Apply selective targets without removing or changing anything in the library.
	sort.SliceStable(items, func(i, j int) bool {
		return num(items[i]["recommendation_score"]) > num(items[j]["recommendation_score"])
	})
	counts := map[string]int{}
	out := []Object{}
	for _, item := range items {
		eligible := len(actorNames(item["actors"])) == 0
		selectedActors := map[string]bool{}
		hasSelectiveActor := false
		for _, name := range actorNames(item["actors"]) {
			canon := canonicalActor(name, aliases)
			policy := policies[actorKey(canon)]
			if policy == nil || str(policy["policy"]) != "selective" {
				continue
			}
			hasSelectiveActor = true
			k := actorKey(canon)
			if counts[k] < actorTarget(policy) {
				eligible = true
				selectedActors[k] = true
			}
		}
		if !hasSelectiveActor && len(actorNames(item["actors"])) > 0 {
			eligible = true
		}
		if !eligible {
			continue
		}
		for k := range selectedActors {
			counts[k]++
		}
		out = append(out, Object{"work": item, "score": item["recommendation_score"], "reason": item["recommendation_reason"]})
		if len(out) == 100 {
			break
		}
	}
	write(w, http.StatusOK, out)
}

func (a *App) actorGenderIndex(aliases map[string]string) map[string]string {
	evidence := map[string]map[string]bool{}
	add := func(rawName, gender string) {
		name := canonicalActor(rawName, aliases)
		key := actorKey(name)
		gender = normalizeActorGender(gender)
		if key == "" || (gender != "female" && gender != "male") {
			return
		}
		if evidence[key] == nil {
			evidence[key] = map[string]bool{}
		}
		evidence[key][gender] = true
	}
	for _, profile := range mustList(a, "actor_profiles") {
		if str(profile["gender_source"]) == "wikidata" {
			add(str(profile["name"]), str(profile["gender"]))
		}
	}
	for _, policy := range mustList(a, "actor_policies") {
		add(str(policy["name"]), str(policy["gender"]))
	}
	if cached, err := a.get("provider_actor_recommendations", "javdbui"); err == nil {
		for _, actor := range asObjects(cached["actors"]) {
			if str(actor["gender_source"]) == "javdbui" {
				add(str(actor["name"]), str(actor["gender"]))
			}
		}
	}
	out := map[string]string{}
	for key, genders := range evidence {
		switch {
		case genders["female"] && !genders["male"]:
			out[key] = "female"
		case genders["male"] && !genders["female"]:
			out[key] = "male"
		default:
			out[key] = "unknown"
		}
	}
	return out
}

func normalizeActorGender(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "female":
		return "female"
	case "male":
		return "male"
	default:
		return "unknown"
	}
}

type recommendationOwnedCodes struct {
	indexedIDs map[string]bool
	knownCodes map[string]bool
	rawCodes   map[string]bool
}

func recommendationOwnedCodeIndex(works []Object) recommendationOwnedCodes {
	owned := recommendationOwnedCodes{
		indexedIDs: make(map[string]bool, len(works)),
		knownCodes: make(map[string]bool, len(works)),
		rawCodes:   make(map[string]bool, len(works)),
	}
	for _, work := range works {
		if !hasAvailableWorkFile(work) {
			continue
		}
		code := strings.TrimSpace(str(work["code"]))
		if code == "" {
			continue
		}
		owned.rawCodes[strings.ToLower(code)] = true
		if normalized, known := normalizeWorkCode(code); known {
			owned.knownCodes[normalized] = true
		}
		if id := str(work["id"]); id != "" {
			owned.indexedIDs[id] = true
		}
	}
	return owned
}

func (owned recommendationOwnedCodes) contains(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	if owned.indexedIDs["code:"+strings.ToUpper(code)] {
		return true
	}
	if normalized, known := normalizeWorkCode(code); known && owned.knownCodes[normalized] {
		return true
	}
	return owned.rawCodes[strings.ToLower(code)]
}

func mustList(a *App, kind string) []Object {
	x, _ := a.list(kind)
	return x
}

func preferenceWeights(works []Object, aliases map[string]string) (map[string]float64, map[string]float64) {
	actors, tags := map[string]float64{}, map[string]float64{}
	for _, w := range works {
		rating, ok := w["rating"].(float64)
		if !ok {
			continue
		}
		weight := rating - 2.5
		if weight == 0 {
			continue
		}
		for _, name := range actorNames(w["actors"]) {
			actors[actorKey(canonicalActor(name, aliases))] += weight
		}
		for _, tag := range actorNames(w["tags"]) {
			tags[actorKey(tag)] += weight
		}
	}
	for k, v := range actors {
		if v < 0 {
			delete(actors, k)
		}
	}
	for k, v := range tags {
		if v < 0 {
			delete(tags, k)
		}
	}
	return actors, tags
}
