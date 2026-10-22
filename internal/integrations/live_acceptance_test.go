package integrations

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestLiveQBittorrentAcceptance runs only against an explicitly configured
// isolated test instance. Its magnets contain synthetic hashes, no trackers,
// and remain paused except for the immediate start/stop API check.
func TestLiveQBittorrentAcceptance(t *testing.T) {
	if os.Getenv("QBIT_ACCEPTANCE_ISOLATED") != "YES" {
		t.Skip("live test requires QBIT_ACCEPTANCE_ISOLATED=YES")
	}
	base := os.Getenv("QBIT_ACCEPTANCE_URL")
	username := os.Getenv("QBIT_ACCEPTANCE_USER")
	password := os.Getenv("QBIT_ACCEPTANCE_PASSWORD")
	if base == "" || (username == "") != (password == "") {
		t.Fatal("live qBittorrent acceptance URL and either both credentials or neither are required")
	}

	ctx := context.Background()
	q, err := NewQBittorrent(QBConfig{URL: base, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(ctx); err != nil {
		t.Fatalf("live qBittorrent login failed: %v", err)
	}
	if !strings.HasPrefix(strings.TrimPrefix(q.version, "v"), "5.3.") {
		t.Fatalf("isolated acceptance requires qBittorrent 5.3; server reported %q", q.version)
	}
	initialTorrents, err := q.ListTorrents(ctx, "")
	if err != nil {
		t.Fatalf("verify isolated instance before changing preferences: %v", err)
	}
	if len(initialTorrents) != 0 {
		t.Fatalf("refusing acceptance writes: isolated instance already has %d tasks", len(initialTorrents))
	}

	prefs, _ := json.Marshal(map[string]bool{"dht": false, "pex": false, "lsd": false})
	if err = q.request(ctx, "POST", "app/setPreferences", url.Values{"json": {string(prefs)}}, nil); err != nil {
		t.Fatalf("disable DHT/PEX/LSD in isolated instance: %v", err)
	}
	const category = "mediashelf-acceptance"
	const savePath = "/downloads/acceptance-preserve"
	cats, err := q.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cats[category]; !exists {
		if err = q.request(ctx, "POST", "torrents/createCategory", url.Values{"category": {category}, "savePath": {savePath}}, nil); err != nil {
			t.Fatalf("create isolated acceptance category: %v", err)
		}
	}

	const autoHash = "0123456789abcdef0123456789abcdef01234567"
	const manualHash = "1234567890abcdef1234567890abcdef12345678"
	hashes := []string{autoHash, manualHash}
	t.Cleanup(func() {
		_ = q.Delete(context.Background(), hashes, false)
	})
	auto := true
	if err = q.Add(ctx, AddTorrent{URL: "magnet:?xt=urn:btih:" + autoHash + "&dn=MediaShelfAcceptanceAuto", Category: category, Paused: true, AutoTMM: &auto}); err != nil {
		t.Fatalf("add paused categorized synthetic magnet: %v", err)
	}
	noAuto := false
	if err = q.Add(ctx, AddTorrent{URL: "magnet:?xt=urn:btih:" + manualHash + "&dn=MediaShelfAcceptanceManual", SavePath: savePath, Paused: true, AutoTMM: &noAuto}); err != nil {
		t.Fatalf("add paused uncategorized synthetic magnet: %v", err)
	}

	torrents, err := q.ListTorrents(ctx, "")
	if err != nil {
		t.Fatalf("list acceptance torrents: %v", err)
	}
	byHash := make(map[string]Torrent, len(torrents))
	for _, torrent := range torrents {
		byHash[strings.ToLower(torrent.Hash)] = torrent
	}
	if got, ok := byHash[autoHash]; !ok || !got.AutoTMM || got.Category != category {
		t.Fatalf("categorized automatic torrent state = %#v, present=%v", got, ok)
	}
	if got, ok := byHash[manualHash]; !ok || got.AutoTMM || got.Category != "" || !strings.EqualFold(strings.TrimRight(got.SavePath, "/"), savePath) {
		t.Fatalf("uncategorized manual torrent state = %#v, present=%v", got, ok)
	}

	if err = q.SetRunning(ctx, []string{autoHash}, true); err != nil {
		t.Fatalf("start synthetic torrent: %v", err)
	}
	if err = q.SetRunning(ctx, []string{autoHash}, false); err != nil {
		t.Fatalf("pause synthetic torrent: %v", err)
	}
	if err = q.SetTags(ctx, []string{manualHash}, "mediashelf-acceptance"); err != nil {
		t.Fatalf("set acceptance tag: %v", err)
	}
	const downloadLimit, uploadLimit int64 = 123456, 654321
	if err = q.SetLimits(ctx, []string{manualHash}, downloadLimit, uploadLimit); err != nil {
		t.Fatalf("set acceptance speed limits: %v", err)
	}
	var info []map[string]json.RawMessage
	if err = q.request(ctx, "GET", "torrents/info?hashes="+url.QueryEscape(manualHash), nil, &info); err != nil {
		t.Fatalf("query configured limits: %v", err)
	}
	if len(info) != 1 {
		t.Fatalf("limit query returned %d torrents, want 1", len(info))
	}
	for field, want := range map[string]int64{"dl_limit": downloadLimit, "up_limit": uploadLimit} {
		var got int64
		if err = json.Unmarshal(info[0][field], &got); err != nil || got != want {
			t.Fatalf("%s = %d, unmarshal error %v, want %d", field, got, err, want)
		}
	}
	var tags string
	if err = json.Unmarshal(info[0]["tags"], &tags); err != nil || !strings.Contains(tags, "mediashelf-acceptance") {
		t.Fatalf("acceptance tag missing (error %v)", err)
	}

	if err = q.Delete(ctx, hashes, false); err != nil {
		t.Fatalf("remove acceptance torrents while preserving files: %v", err)
	}
	remaining, err := q.ListTorrents(ctx, "")
	if err != nil {
		t.Fatalf("verify acceptance torrents removed: %v", err)
	}
	for _, torrent := range remaining {
		if strings.EqualFold(torrent.Hash, autoHash) || strings.EqualFold(torrent.Hash, manualHash) {
			t.Fatalf("acceptance torrent %q remains after removal", torrent.Hash)
		}
	}
}
