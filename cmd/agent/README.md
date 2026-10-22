# MediaShelf Windows agent

Build from the repository root with `go build ./cmd/agent`, then copy
`agent.example.json` to `agent.json`, fill in the device token and local
absolute paths, and run `agent.exe --config agent.json`.

The agent heartbeats and polls every 20 seconds, retries network failures, and
scans locally allowlisted libraries at the configured interval (60 minutes by
default). `--once` performs one heartbeat/poll cycle. `--scan-library` scans
all locally allowlisted libraries that are registered to this device before
polling.

This build is a console process; it does not install a Windows service or tray
icon. GUI tools launch in the agent's current Windows session, so the agent
must run in the interactive user session for their windows to appear. The
agent never moves or deletes media, and qBittorrent jobs cannot delete torrent
files.
