param(
    [Parameter(Mandatory = $true)]
    [string]$AgentPath,

    [string]$ConfigPath,

    [switch]$Remove
)

$startupFolder = [Environment]::GetFolderPath('Startup')
$shortcutPath = Join-Path $startupFolder 'MediaShelf Agent.lnk'

if ($Remove) {
    if (Test-Path -LiteralPath $shortcutPath) {
        Remove-Item -LiteralPath $shortcutPath -Force
    }
    Write-Host 'Removed the MediaShelf Agent startup shortcut.'
    exit 0
}

$agentFullPath = (Resolve-Path -LiteralPath $AgentPath -ErrorAction Stop).Path
if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = Join-Path (Split-Path -Parent $agentFullPath) 'agent.json'
}
$configFullPath = (Resolve-Path -LiteralPath $ConfigPath -ErrorAction Stop).Path

$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $agentFullPath
$shortcut.Arguments = '--config "' + $configFullPath + '" --tray'
$shortcut.WorkingDirectory = Split-Path -Parent $agentFullPath
$shortcut.Description = 'Start the MediaShelf device agent in the tray.'
$shortcut.Save()

Write-Host 'Created the MediaShelf Agent shortcut in the current user Startup folder.'
