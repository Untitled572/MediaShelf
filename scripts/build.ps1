param([string]$Output = "dist")
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
  New-Item -ItemType Directory -Force $Output | Out-Null
  Push-Location web
  try { npm.cmd ci; if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }; npm.cmd run build; if ($LASTEXITCODE -ne 0) { throw 'web build failed' } } finally { Pop-Location }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
  go build -trimpath -o "$Output/mediashelf-server.exe" ./cmd/server
  if ($LASTEXITCODE -ne 0) { throw 'Server build failed' }
  go build -trimpath -o "$Output/mediashelf-agent.exe" ./cmd/agent
  if ($LASTEXITCODE -ne 0) { throw 'Agent build failed' }
} finally { Pop-Location }
