# start-radar-silent.ps1
# Starts the Radar server in the background SILENTLY (no terminal window).
# Designed to run from a logon Startup VBS wrapper, but works manually too:
#   powershell -NoProfile -ExecutionPolicy Bypass -File .\start-radar-silent.ps1
#
# Keeps the --prometheus-single-cluster flag (resolves the "Historical cluster scope
# could not be verified" metric-reconstruction warning) and does not open the browser
# (-no-browser): instead of a desktop GUI, the web UI stays available at
# http://localhost:9280. Port 9280 serves both the web UI and the MCP endpoint
# (http://localhost:9280/mcp) consumed by OpenCode.

$radarExe = Join-Path $env:LOCALAPPDATA 'radar\radar.exe'
$radarArgs = @('--prometheus-single-cluster', '-no-browser')

# Avoid duplicate instances: if a radar process is already running, do nothing
# (prevents a binding conflict on port 9280).
if (Get-Process -Name 'radar' -ErrorAction SilentlyContinue) {
    exit 0
}

# -WindowStyle Hidden -> starts with no visible console window (no startup "flash").
Start-Process -FilePath $radarExe -ArgumentList $radarArgs -WindowStyle Hidden
