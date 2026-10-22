# MediaShelf Windows agent

Build from the repository root with `go build ./cmd/agent`, then copy
`agent.example.json` to `agent.json`, fill in the device token and local
absolute paths, and run `agent.exe --config agent.json`.

The agent heartbeats and polls every 20 seconds, retries network failures, and
scans locally allowlisted libraries at the configured interval (60 minutes by
default). `--once` performs one heartbeat/poll cycle. `--scan-library` scans
all locally allowlisted libraries that are registered to this device before
polling. Add `--tray` for a Windows notification icon with status, 打开 MediaShelf,
立即扫描, and 退出 actions.

Tool entries may include an `args` array for fixed arguments passed to an
allowlisted `.exe` or `.com`. Arguments for `.bat` and `.cmd` tools are disabled.
The optional `journal_path` must be an absolute path; when omitted, the journal
is created beside the config file.

Results are kept in `agent-journal.json` beside the config until the server
accepts them. This lets the agent retry a failed result upload without running
the same job again. The optional `scripts/install-agent.ps1` creates or removes
a shortcut in the current user's Startup folder; it does not edit the registry
and only changes the startup configuration when you run it.

GUI tools launch in the agent's current Windows session, so the agent must run
in the interactive user session for their windows to appear. The agent never
moves or deletes media, and qBittorrent jobs cannot delete torrent files.
