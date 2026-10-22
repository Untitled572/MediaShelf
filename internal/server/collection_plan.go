package server

import (
	"net/http"
	"sort"
	"strings"
)

const collectionPlanStateKind = "collection_plan_items"

type collectionPlanActor struct {
	name, policy  string
	lastSyncError string
	target        int
	owned         map[string]bool
	known         map[string]string
	lastSync      string
	needsSync     bool
}

func collectionCodeKey(code string) string {
	if normalized, known := normalizeWorkCode(code); known {
		return normalized
	}
	return strings.ToLower(strings.TrimSpace(code))
}

func collectionCandidatePriority(candidate Object) int {
	switch str(candidate["status"]) {
	case "submitted":
		return 5
	case "submitting":
		return 4
	case "uncertain":
		return 3
	case "ignored":
		return 2
	default:
		return 1
	}
}

func (a *App) collectionPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	policies := actorPolicies(a)
	aliases := aliasMap(policies)
	actors := map[string]*collectionPlanActor{}
	for _, p := range policies {
		name := strings.TrimSpace(str(p["name"]))
		if name == "" {
			name = strings.TrimSpace(str(p["id"]))
		}
		if name == "" {
			continue
		}
		key := actorKey(name)
		policy := strings.ToLower(str(p["policy"]))
		if policy != "complete" && policy != "selective" && policy != "follow" {
			policy = "follow"
		}
		if policy == "follow" {
			continue
		}
		actors[key] = &collectionPlanActor{name: name, policy: policy, target: actorTarget(p), owned: map[string]bool{}, known: map[string]string{}}
	}
	works := mustList(a, "works")
	ownedCodes := recommendationOwnedCodeIndex(works)
	for _, work := range works {
		if !hasAvailableWorkFile(work) {
			continue
		}
		code := strings.TrimSpace(str(work["code"]))
		if code == "" {
			continue
		}
		ck := collectionCodeKey(code)
		for _, actor := range actors {
			if hasActor(work, actor.name, aliases) {
				actor.owned[ck] = true
			}
		}
	}

	catalog := mustList(a, "catalog")
	// Collapse provider duplicates by normalized work code, retaining the best
	// rated metadata while merging every associated actor.
	byCode := map[string]Object{}
	for _, row := range catalog {
		code := strings.TrimSpace(str(row["code"]))
		if code == "" {
			continue
		}
		key := collectionCodeKey(code)
		current := byCode[key]
		if current == nil {
			current = Object{}
			for k, v := range row {
				current[k] = v
			}
			current["actors"] = []string{}
			byCode[key] = current
		} else if num(row["rating"]) > num(current["rating"]) {
			for _, field := range []string{"title", "year", "rating", "source_url", "source", "thumbnail_url", "cover_url"} {
				if v, ok := row[field]; ok && v != nil && v != "" {
					current[field] = v
				}
			}
		}
		merged := stringSlice(current["actors"])
		for _, name := range append(stringSlice(row["actors"]), str(row["catalog_actor"])) {
			canonical := canonicalActor(name, aliases)
			if canonical != "" && !contains(merged, canonical) {
				merged = append(merged, canonical)
			}
		}
		current["actors"] = merged
	}

	for _, row := range byCode {
		if isCompilation(row) {
			continue
		}
		code := str(row["code"])
		ck := collectionCodeKey(code)
		for _, actor := range actors {
			if hasActor(row, actor.name, aliases) {
				actor.known[ck] = code
			}
		}
	}
	for _, sync := range mustList(a, "catalog_syncs") {
		name := canonicalActor(str(sync["actor"]), aliases)
		if actor := actors[actorKey(name)]; actor != nil {
			if str(sync["last_sync"]) > actor.lastSync {
				actor.lastSync = str(sync["last_sync"])
				actor.lastSyncError = str(sync["error"])
				actor.needsSync = !flag(sync["complete"])
			}
		}
	}
	for _, actor := range actors {
		if actor.lastSync == "" {
			actor.needsSync = true
		}
	}

	manual := map[string]string{}
	for _, row := range mustList(a, collectionPlanStateKind) {
		manual[collectionCodeKey(str(row["code"]))] = str(row["status"])
	}
	candidates := mustList(a, "candidates")
	candidateByCode := map[string]Object{}
	for _, candidate := range candidates {
		code := str(candidate["code"])
		if code == "" {
			continue
		}
		ck := collectionCodeKey(code)
		current := candidateByCode[ck]
		if current == nil || collectionCandidatePriority(candidate) > collectionCandidatePriority(current) ||
			(collectionCandidatePriority(candidate) == collectionCandidatePriority(current) && str(candidate["id"]) < str(current["id"])) {
			candidateByCode[ck] = candidate
		}
	}

	selectedByCode := map[string]Object{}
	for _, actor := range actors {
		available := make([]Object, 0)
		for codeKey, row := range byCode {
			if isCompilation(row) || !hasActor(row, actor.name, aliases) || actor.owned[codeKey] || ownedCodes.contains(str(row["code"])) {
				continue
			}
			available = append(available, row)
		}
		sort.Slice(available, func(i, j int) bool {
			if num(available[i]["rating"]) != num(available[j]["rating"]) {
				return num(available[i]["rating"]) > num(available[j]["rating"])
			}
			return str(available[i]["code"]) < str(available[j]["code"])
		})
		limit := len(available)
		if actor.policy == "selective" {
			gap := actor.target - len(actor.owned)
			if gap < 0 {
				gap = 0
			}
			if limit > gap {
				limit = gap
			}
		}
		for _, row := range available[:limit] {
			code := str(row["code"])
			ck := collectionCodeKey(code)
			item := selectedByCode[ck]
			if item == nil {
				item = Object{"id": "code:" + strings.ToUpper(ck), "code": code, "title": str(row["title"]), "year": row["year"], "rating": row["rating"], "catalog_id": row["id"], "actors": []string{}, "reasons": []string{}, "status": "planned", "source_url": str(row["source_url"])}
				selectedByCode[ck] = item
			}
			if !contains(item["actors"], actor.name) {
				item["actors"] = append(stringSlice(item["actors"]), actor.name)
			}
			reason := actor.name + "：收全策略缺口"
			if actor.policy == "selective" {
				reason = actor.name + "：精选策略目标缺口内的高评分作品"
				if num(row["rating"]) <= 0 {
					reason = actor.name + "：精选目标候选（来源评分待补充）"
				}
			}
			if !contains(item["reasons"], reason) {
				item["reasons"] = append(stringSlice(item["reasons"]), reason)
			}
		}
	}

	items := make([]Object, 0, len(selectedByCode))
	counts := map[string]int{"planned": 0, "queued": 0, "ignored": 0}
	for codeKey, item := range selectedByCode {
		candidate := candidateByCode[codeKey]
		if candidate != nil {
			item["candidate_id"] = candidate["id"]
			item["candidate_status"] = candidate["status"]
		}
		status := "planned"
		if candidate != nil {
			switch str(candidate["status"]) {
			case "submitted", "submitting", "uncertain":
				status = "queued"
			case "ignored":
				status = "ignored"
			}
		}
		switch manual[codeKey] {
		case "ignored":
			if status != "queued" {
				status = "ignored"
			}
		case "planned":
			if status == "ignored" {
				status = "planned"
			}
		}
		item["status"] = status
		counts[status]++
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return str(items[i]["code"]) < str(items[j]["code"]) })

	actorRows := make([]Object, 0, len(actors))
	for _, actor := range actors {
		planned := 0
		for _, item := range items {
			if contains(item["actors"], actor.name) && str(item["status"]) != "ignored" && str(item["status"]) != "queued" {
				planned++
			}
		}
		missing := 0
		for codeKey, code := range actor.known {
			if !actor.owned[codeKey] && !ownedCodes.contains(code) {
				missing++
			}
		}
		actorRows = append(actorRows, Object{"name": actor.name, "policy": actor.policy, "target": actor.target, "owned": len(actor.owned), "known": len(actor.known), "known_owned": len(actor.known) - missing, "missing": missing, "planned": planned, "needs_sync": actor.needsSync, "last_synced_at": actor.lastSync, "last_sync_error": actor.lastSyncError})
	}
	sort.Slice(actorRows, func(i, j int) bool { return str(actorRows[i]["name"]) < str(actorRows[j]["name"]) })
	write(w, http.StatusOK, Object{"actors": actorRows, "items": items, "summary": Object{"actors": len(actorRows), "planned": counts["planned"], "queued": counts["queued"], "ignored": counts["ignored"]}, "generated_at": now()})
}

func (a *App) patchCollectionPlan(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPatch {
		fail(w, http.StatusMethodNotAllowed, "PATCH required")
		return
	}
	if !strings.HasPrefix(key, "code:") || strings.TrimSpace(strings.TrimPrefix(key, "code:")) == "" {
		fail(w, http.StatusBadRequest, "invalid collection plan item id")
		return
	}
	body, ok := read(w, r)
	if !ok {
		return
	}
	status := str(body["status"])
	if status != "ignored" && status != "planned" {
		fail(w, http.StatusBadRequest, "status must be ignored or planned")
		return
	}
	code := strings.TrimPrefix(key, "code:")
	canonicalID := "code:" + strings.ToUpper(collectionCodeKey(code))
	if err := a.put(collectionPlanStateKind, Object{"id": canonicalID, "code": code, "status": status, "updated_at": now()}); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, Object{"id": canonicalID, "code": code, "status": status})
}
