package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCollectionPlanCompleteSelectiveOwnedOfflineAndAggregation(t *testing.T) {
	a := testApp(t)
	_ = a.put("actor_policies", Object{"id": "A", "name": "A", "policy": "complete"})
	_ = a.put("actor_policies", Object{"id": "B", "name": "B", "policy": "complete"})
	_ = a.put("actor_policies", Object{"id": "C", "name": "C", "policy": "selective", "target": 3})
	_ = a.put("actor_policies", Object{"id": "F", "name": "F", "policy": "follow"})
	// A stale/offline device remains local ownership because its file is still
	// registered and not marked missing.
	_ = a.put("works", Object{"id": "code:OWN-1", "code": "OWN-1", "actors": []string{"C"}, "device_id": "offline-device", "files": []Object{{"missing": false}}})
	_ = a.put("works", Object{"id": "code:OWN-0", "code": "OWN-0", "actors": []string{"C"}, "files": []Object{{"missing": false}}})
	_ = a.put("catalog", Object{"id": "javdb:SHARED-1", "source": "javdb", "code": "SHARED-1", "title": "Shared", "actors": []string{"A", "B"}, "rating": 4.6, "source_url": "https://example.test/shared"})
	_ = a.put("catalog", Object{"id": "fanza:SHARED-1", "source": "fanza", "code": "SHARED-1", "title": "Shared lower rating", "actors": []string{"B"}, "rating": 4.1})
	_ = a.put("catalog", Object{"id": "javdb:OWN-1", "source": "javdb", "code": "OWN-1", "title": "Already owned", "actors": []string{"C"}, "rating": 5.0})
	_ = a.put("catalog", Object{"id": "javdb:SEL-2", "source": "javdb", "code": "SEL-2", "title": "Top pick", "actors": []string{"C"}, "rating": 4.9})
	_ = a.put("catalog", Object{"id": "javdb:SEL-3", "source": "javdb", "code": "SEL-3", "title": "Second pick", "actors": []string{"C"}, "rating": 4.2})
	_ = a.put("catalog", Object{"id": "javdb:SEL-4", "source": "javdb", "code": "SEL-4", "title": "Over target", "actors": []string{"C"}, "rating": 4.0})
	_ = a.put("catalog", Object{"id": "javdb:BEST-5", "source": "javdb", "code": "BEST-5", "title": "Best of C collection", "actors": []string{"A", "C"}, "rating": 5.0})
	_ = a.put("catalog", Object{"id": "javdb:FOL-6", "source": "javdb", "code": "FOL-6", "title": "Follow only", "actors": []string{"F"}, "rating": 5.0})
	_ = a.put("catalog_syncs", Object{"id": "javdb:A", "actor": "A", "source": "javdb", "complete": false, "last_sync": "2026-10-22T01:00:00Z"})
	_ = a.put("catalog_syncs", Object{"id": "old:B", "actor": "B", "source": "old", "complete": false, "last_sync": "2026-10-21T02:00:00Z", "error": "old failure"})
	_ = a.put("catalog_syncs", Object{"id": "javdb:B", "actor": "B", "source": "javdb", "complete": true, "last_sync": "2026-10-22T02:00:00Z"})
	w := callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET collection-plan failed: %d %s", w.Code, w.Body.String())
	}
	o := responseObject(t, w)
	items := asObjects(o["items"])
	if len(items) != 2 {
		t.Fatalf("expected shared complete item and one selective gap item, got %#v", items)
	}
	byCode := map[string]Object{}
	for _, item := range items {
		byCode[str(item["code"])] = item
	}
	shared := byCode["SHARED-1"]
	if shared == nil || len(stringSlice(shared["actors"])) != 2 || len(stringSlice(shared["reasons"])) != 2 || str(shared["status"]) != "planned" {
		t.Fatalf("shared item was not deduped with actor/reason aggregation: %#v", shared)
	}
	if byCode["OWN-1"] != nil || byCode["SEL-4"] != nil || byCode["BEST-5"] != nil || byCode["FOL-6"] != nil || byCode["SEL-2"] == nil || byCode["SEL-3"] != nil {
		t.Fatalf("ownership, selective target, compilation, or follow rules failed: %#v", byCode)
	}
	actors := asObjects(o["actors"])
	actorByName := map[string]Object{}
	for _, actor := range actors {
		actorByName[str(actor["name"])] = actor
	}
	if !flag(actorByName["A"]["needs_sync"]) || flag(actorByName["B"]["needs_sync"]) || int(num(actorByName["C"]["owned"])) != 2 || int(num(actorByName["C"]["known"])) != 4 || int(num(actorByName["C"]["missing"])) != 3 || int(num(actorByName["C"]["planned"])) != 1 {
		t.Fatalf("actor summaries were incorrect: %#v", actors)
	}
	if _, ok := actorByName["F"]; ok || int(num(toObject(o["summary"])["actors"])) != 3 {
		t.Fatalf("follow actor should be excluded from plan summaries: %#v", o["summary"])
	}
	summary := toObject(o["summary"])
	if int(num(summary["planned"])) != 2 || int(num(summary["queued"])) != 0 || int(num(summary["ignored"])) != 0 {
		t.Fatalf("unexpected summary counts: %#v", summary)
	}
}

func TestCollectionPlanShowsCandidateQueueAndPersistsManualStatus(t *testing.T) {
	a := testApp(t)
	_ = a.put("actor_policies", Object{"id": "A", "name": "A", "policy": "complete"})
	_ = a.put("catalog", Object{"id": "javdb:ONE-11", "source": "javdb", "code": "ONE-11", "title": "One", "actors": []string{"A"}, "rating": 4.5})
	_ = a.put("catalog", Object{"id": "javdb:TWO-2", "source": "javdb", "code": "TWO-2", "title": "Two", "actors": []string{"A"}, "rating": 4.4})
	_ = a.put("catalog", Object{"id": "javdb:THREE-33", "source": "javdb", "code": "THREE-33", "title": "Three", "actors": []string{"A"}, "rating": 4.3})
	_ = a.put("candidates", Object{"id": "candidate-one", "code": "ONE-11", "status": "new"})
	_ = a.put("candidates", Object{"id": "candidate-one-submitted", "code": "ONE_11", "status": "submitted"})
	_ = a.put("candidates", Object{"id": "candidate-two", "code": "TWO-2", "status": "submitted"})
	_ = a.put("candidates", Object{"id": "candidate-three", "code": "THREE-33", "status": "new"})
	first := callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	items := asObjects(responseObject(t, first)["items"])
	states := map[string]string{}
	for _, item := range items {
		states[str(item["code"])] = str(item["status"])
	}
	if states["ONE-11"] != "queued" || states["TWO-2"] != "queued" || states["THREE-33"] != "planned" {
		t.Fatalf("candidate/queue states not reflected: %#v", states)
	}
	var oneID string
	for _, item := range items {
		if str(item["code"]) == "ONE-11" {
			oneID = str(item["id"])
			if str(item["candidate_id"]) != "candidate-one-submitted" || str(item["candidate_status"]) != "submitted" || oneID != "code:ONE11" {
				t.Fatalf("duplicate candidate choice or canonical id failed: %#v", item)
			}
		}
	}
	ignored := callDiscovery(t, a, http.MethodPatch, []string{"collection-plan", oneID}, Object{"status": "ignored"})
	if ignored.Code != http.StatusOK {
		t.Fatalf("ignore failed: %d %s", ignored.Code, ignored.Body.String())
	}
	second := callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	for _, item := range asObjects(responseObject(t, second)["items"]) {
		if str(item["code"]) == "ONE-11" && str(item["status"]) != "queued" {
			t.Fatalf("queued item must remain queued despite manual ignore: %#v", item)
		}
	}
	// The same persistence behavior applies to a nonqueued planned item.
	ignored = callDiscovery(t, a, http.MethodPatch, []string{"collection-plan", "code:THREE33"}, Object{"status": "ignored"})
	if ignored.Code != http.StatusOK {
		t.Fatalf("ignore planned item failed: %d %s", ignored.Code, ignored.Body.String())
	}
	second = callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	for _, item := range asObjects(responseObject(t, second)["items"]) {
		if str(item["code"]) == "THREE-33" && str(item["status"]) != "ignored" {
			t.Fatalf("ignored state did not persist: %#v", item)
		}
	}
	restore := callDiscovery(t, a, http.MethodPatch, []string{"collection-plan", "code:THREE33"}, Object{"status": "planned"})
	if restore.Code != http.StatusOK {
		t.Fatalf("restore failed: %d %s", restore.Code, restore.Body.String())
	}
	third := callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	for _, item := range asObjects(responseObject(t, third)["items"]) {
		if str(item["code"]) == "THREE-33" && str(item["status"]) != "planned" {
			t.Fatalf("planned state did not restore item: %#v", item)
		}
	}
}

func TestCollectionPlanSelectiveMissingScoreIsLabelled(t *testing.T) {
	a := testApp(t)
	_ = a.put("actor_policies", Object{"id": "A", "name": "A", "policy": "selective", "target": 1})
	_ = a.put("catalog", Object{"id": "one", "code": "ABC-123", "actors": []string{"A"}})
	w := callDiscovery(t, a, http.MethodGet, []string{"collection-plan"}, nil)
	items := asObjects(responseObject(t, w)["items"])
	if len(items) != 1 || !strings.Contains(strings.Join(stringSlice(items[0]["reasons"]), ""), "评分待补充") {
		t.Fatalf("unrated item was represented as highly rated: %v", items)
	}
}
