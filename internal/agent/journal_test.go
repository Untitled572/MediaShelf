package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestResultJournalPersistsAndRemovesPendingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-journal.json")
	j, err := OpenResultJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	want := PendingResult{Status: "completed", Result: "stored-result"}
	if err = j.Save("job-1", want); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenResultJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get("job-1")
	if !ok || got != want {
		t.Fatalf("reloaded result = %#v, %v; want %#v", got, ok, want)
	}
	if err = reloaded.Remove("job-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok = reloaded.Get("job-1"); ok {
		t.Fatal("removed journal entry remains in memory")
	}
	final, err := OpenResultJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Pending()) != 0 {
		t.Fatal("removed journal entry remains on disk")
	}
}

func TestCycleReplaysPendingResultWithoutRelaunchingJob(t *testing.T) {
	var resultPosts atomic.Int32
	var gotStatus, gotResult string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/poll":
			_ = json.NewEncoder(w).Encode([]Job{{ID: "job-1", Type: "launch", Payload: json.RawMessage(`{"tool_id":"not-allowlisted"}`)}})
		case "/api/v1/agent/jobs/job-1/result":
			var body struct {
				Status string `json:"status"`
				Result string `json:"result"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode result: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			attempt := resultPosts.Add(1)
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			gotStatus, gotResult = body.Status, body.Result
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "journal.json")
	journal, err := OpenResultJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	want := PendingResult{Status: "completed", Result: "already-finished"}
	if err = journal.Save("job-1", want); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ServerURL: srv.URL, Token: "test", Libraries: map[string]string{"lib": t.TempDir()}, JournalPath: path}
	runner := NewRunner(cfg, nil)
	runner.Journal = journal
	if err = runner.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resultPosts.Load() != 2 {
		t.Fatalf("result endpoint called %d times, want retry then replay", resultPosts.Load())
	}
	if gotStatus != want.Status || gotResult != want.Result {
		t.Fatalf("posted result %q/%q, want stored result %q/%q", gotStatus, gotResult, want.Status, want.Result)
	}
	if _, ok := journal.Get("job-1"); ok {
		t.Fatal("confirmed result was not removed from journal")
	}
}
