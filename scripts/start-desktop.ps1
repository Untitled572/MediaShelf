param(
    [string]$SshTarget = 'l31155@ut572.eu.org',
    [int]$SshPort = 222,
    [int]$LocalPort = 18765,
    [int]$RemotePort = 18765,
    [string]$ConfigPath,
    [switch]$NoBrowser
)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
if (-not $ConfigPath) { $ConfigPath = Join-Path $root 'data/production-agent.json' }
$configFile = (Resolve-Path -LiteralPath $ConfigPath).Path
$agentExe = (Resolve-Path -LiteralPath (Join-Path $root 'dist/mediashelf-agent.exe')).Path
$base = "http://127.0.0.1:$LocalPort"
$ready = $false
try { $ready = (Invoke-WebRequest "$base/healthz" -TimeoutSec 3).StatusCode -eq 200 } catch {}
if (-not $ready) {
    $sshArgs = @('-p', $SshPort, '-o', 'BatchMode=yes', '-o', 'ExitOnForwardFailure=yes', '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3', '-N', '-L', "127.0.0.1:${LocalPort}:127.0.0.1:${RemotePort}", $SshTarget)
    $tunnel = Start-Process ssh.exe -ArgumentList $sshArgs -WindowStyle Hidden -PassThru
    for ($attempt = 0; $attempt -lt 10; $attempt++) {
        Start-Sleep -Milliseconds 500
        try { $ready = (Invoke-WebRequest "$base/healthz" -TimeoutSec 2).StatusCode -eq 200 } catch {}
        if ($ready -or $tunnel.HasExited) { break }
    }
    if (-not $ready) { throw 'SSH tunnel failed. Check the SSH key and remote MediaShelf service.' }
}
$running = Get-CimInstance Win32_Process -Filter "Name='mediashelf-agent.exe'" | Where-Object { $_.ExecutablePath -eq $agentExe -and $_.CommandLine.Contains($configFile) }
if (-not $running) {
    Start-Process -FilePath $agentExe -ArgumentList @('--config', ('"' + $configFile + '"'), '--tray') -WorkingDirectory $root -WindowStyle Hidden
}
if (-not $NoBrowser) { Start-Process $base }
Write-Output "MediaShelf ready: $base"
