package server

import (
	"context"
	"fmt"
	"mediashelf/internal/integrations"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Refresh imports only public metadata. It never enqueues a download.
func (a *App) refreshPublicRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST required")
		return
	}
	input, ok := read(w, r)
	if !ok {
		return
	}
	source := str(input["source"])
	if source == "" {
		source = "javdbui"
	}
	if source != "javdbui" {
		fail(w, 400, "该来源不支持站点推荐更新")
		return
	}
	cfg, _ := a.get("sources", source)
	proxyURL, err := a.outboundProxy()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	client, err := integrations.NewJavDBUIClient(integrations.CatalogConfig{BaseURL: str(cfg["base_url"]), ProxyURL: proxyURL})
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	pages := int(num(input["pages"]))
	if pages < 1 {
		pages = 2
	}
	if pages > 3 {
		pages = 3
	}
	type result struct {
		slot   int
		movies []integrations.Metadata
		actors *integrations.JavDBUIActorRecommendations
		err    error
	}
	results := make(chan result, pages+2)
	go func() { movies, e := client.Recommendations(ctx); results <- result{slot: 0, movies: movies, err: e} }()
	for page := 1; page <= pages; page++ {
		go func(p int) { movies, e := client.Latest(ctx, p); results <- result{slot: p, movies: movies, err: e} }(page)
	}
	go func() {
		actors, e := client.RecommendedActors(ctx)
		results <- result{slot: pages + 1, actors: &actors, err: e}
	}()
	movies := []integrations.Metadata{}
	seen := map[string]bool{}
	errors := []string{}
	var actors *integrations.JavDBUIActorRecommendations
	ordered := make([]result, pages+2)
	for i := 0; i < pages+2; i++ {
		v := <-results
		ordered[v.slot] = v
	}
	for _, v := range ordered {
		if v.err != nil {
			errors = append(errors, v.err.Error())
			continue
		}
		if v.actors != nil {
			actors = v.actors
		}
		for _, movie := range v.movies {
			code := strings.ToUpper(movie.Code)
			if code != "" && !seen[code] {
				movies = append(movies, movie)
				seen[code] = true
			}
		}
	}
	// Enrich a bounded number of real provider records with actors and tags.
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	enriched := 0
	for i := 0; i < len(movies) && i < 16; i++ {
		if movies[i].ProviderID == "" {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			detail, e := client.FetchDetails(ctx, movies[index].ProviderID)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				errors = append(errors, e.Error())
				return
			}
			if !sameWorkCode(detail.Code, movies[index].Code) {
				errors = append(errors, "详情编号与目录编号不匹配")
				return
			}
			movies[index] = detail
			enriched++
		}(i)
	}
	wg.Wait()
	imported := 0
	for _, movie := range movies {
		x := toObject(movie)
		x["id"] = catalogID(source, movie.Code)
		x["fetched_at"] = now()
		if err := a.mergeCatalog(x); err != nil {
			fail(w, 500, err.Error())
			return
		}
		imported++
	}
	actorCount := 0
	if actors != nil {
		cards := []Object{}
		names := map[string]bool{}
		groups := []struct {
			label string
			items []integrations.JavDBUIActor
		}{{"站点推荐演员", actors.RecommendActors}, {"站点月度演员", actors.MonthlyActors}, {"站点新演员", actors.NewActors}}
		for _, group := range groups {
			for rank, actor := range group.items {
				key := actorKey(actor.Name)
				if key == "" || names[key] {
					continue
				}
				names[key] = true
				gender := javDBUIActorGender(actor.Gender)
				cards = append(cards, Object{"name": actor.Name, "avatar_url": actor.AvatarURL, "provider_id": actor.ID, "gender": gender, "gender_source": "javdbui", "reason": "JavDB UI · " + group.label, "source": "javdbui", "score": 1 - float64(rank)/float64(len(group.items)+1)})
			}
		}
		if err := a.put("provider_actor_recommendations", Object{"id": "javdbui", "actors": cards, "fetched_at": now()}); err != nil {
			fail(w, 500, err.Error())
			return
		}
		actorCount = len(cards)
	}
	check := Object{"id": source, "source": source, "checked_at": now(), "ok": len(errors) == 0, "partial": imported > 0, "imported": imported}
	if len(errors) > 0 {
		check["error"] = strings.Join(errors, "; ")
	}
	_ = a.put("source_checks", check)
	if imported == 0 && actorCount == 0 {
		fail(w, 502, fmt.Sprintf("JavDB UI 更新失败：%s", strings.Join(errors, "; ")))
		return
	}
	write(w, 200, Object{"source": source, "imported": imported, "enriched": enriched, "actors": actorCount, "errors": errors})
}

func (a *App) syncJavDBUICatalog(w http.ResponseWriter, r *http.Request, actor string, input Object) {
	proxyURL, err := a.outboundProxy()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	config, _ := a.get("sources", "javdbui")
	client, err := integrations.NewJavDBUIClient(integrations.CatalogConfig{BaseURL: str(config["base_url"]), ProxyURL: proxyURL})
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	pages := int(num(input["pages"]))
	if pages < 1 {
		pages = 5
	}
	if pages > 50 {
		pages = 50
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, syncErr := client.ActorCatalog(ctx, actor, pages)
	status := Object{
		"id": "javdbui:" + actor, "actor": actor, "source": "javdbui", "complete": syncErr == nil && result.Complete,
		"last_sync": now(), "count": 0, "pages": result.Page,
	}
	count := 0
	for _, movie := range result.Works {
		x := toObject(movie)
		x["id"] = catalogID("javdbui", movie.Code)
		if movie.Code == "" {
			x["id"] = "javdbui:" + digest(movie.SourceURL)
		}
		x["catalog_actor"] = actor
		x["fetched_at"] = now()
		if !contains(x["actors"], actor) {
			actors := stringSlice(x["actors"])
			actors = append(actors, actor)
			x["actors"] = actors
		}
		if err := a.put("catalog", x); err != nil {
			status["complete"] = false
			status["count"] = count
			status["error"] = err.Error()
			_ = a.put("catalog_syncs", status)
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		count++
	}
	status["count"] = count
	status["has_more"] = result.HasMore
	if syncErr != nil {
		status["error"] = syncErr.Error()
		_ = a.put("catalog_syncs", status)
		fail(w, http.StatusBadGateway, fmt.Sprintf("已同步 %d 项，尚未完成：%s", count, syncErr))
		return
	}
	if err := a.put("catalog_syncs", status); err != nil {
		fail(w, http.StatusInternalServerError, "目录同步状态保存失败")
		return
	}
	write(w, http.StatusOK, status)
}

func (a *App) providerActorRecommendations() []Object {
	cached, err := a.get("provider_actor_recommendations", "javdbui")
	if err != nil {
		return []Object{}
	}
	aliases := aliasMap(actorPolicies(a))
	known := map[string]bool{}
	for _, work := range mustList(a, "works") {
		for _, name := range actorNames(work["actors"]) {
			known[actorKey(canonicalActor(name, aliases))] = true
		}
	}
	out := []Object{}
	for _, actor := range asObjects(cached["actors"]) {
		if !known[actorKey(canonicalActor(str(actor["name"]), aliases))] {
			out = append(out, actor)
		}
	}
	return out
}

// JavDBUI's live public actor cards identify known actresses with gender=0.
// Only that observed value is accepted as female; every other integer or a
// missing/null field remains unknown until another source confirms it.
func javDBUIActorGender(raw *int) string {
	if raw != nil && *raw == 0 {
		return "female"
	}
	return "unknown"
}
