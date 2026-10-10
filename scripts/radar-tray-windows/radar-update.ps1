# radar-update.ps1 - checks the Radar version and performs the auto-update (if a newer one exists).
# Internal: requires Windows PowerShell 5.1 (powershell.exe).
#
# Flow:
#   installed version -> latest GitHub release -> if newer: download, extract,
#   back up the current binaries, stop Radar, replace both binaries, restart and
#   VERIFY that the server comes back online. On error it restores the backups and restarts.
#
# A release asset contains a single binary (kubectl-radar.exe); the same identical binary
# is also used as radar.exe, so BOTH names must be replaced.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File .\radar-update.ps1
# -silent: only writes the result file (no console output), used by the tray menu.

param([switch]$silent)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$installDir = Join-Path $env:LOCALAPPDATA 'radar'
$radarExe   = Join-Path $installDir 'radar.exe'
$backupDir  = Join-Path $env:USERPROFILE '.radar\backup'
$resultFile = Join-Path $env:USERPROFILE '.radar\last-update-result.txt'
$launcher   = Join-Path $env:USERPROFILE '.radar\start-radar-silent.ps1'

# Rotate/clean old logs and temp files so they never fill the disk.
function Clear-StaleRadar {
    $now = Get-Date
    # stale install/download/AI temp folders older than 7 days
    foreach ($d in @('radar-install-*', 'radar-update-*', 'radar-ai-*')) {
        Get-Item (Join-Path $env:TEMP $d) -ErrorAction SilentlyContinue |
            Where-Object { $now.Subtract($_.LastWriteTime).TotalDays -gt 7 } |
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }
    # explicit radar logs in Temp\opencode older than 30 days
    Get-ChildItem (Join-Path $env:TEMP 'opencode') -Filter 'radar-*.log' -ErrorAction SilentlyContinue |
        Where-Object { $now.Subtract($_.LastWriteTime).TotalDays -gt 30 } |
        Remove-Item -Force -ErrorAction SilentlyContinue
    # interrupted partial downloads (.tmp) in ~/.radar/updates older than 2 days
    Get-ChildItem (Join-Path $env:USERPROFILE '.radar\updates') -Filter '*.tmp' -ErrorAction SilentlyContinue |
        Where-Object { $now.Subtract($_.LastWriteTime).TotalDays -gt 2 } |
        Remove-Item -Force -ErrorAction SilentlyContinue
    # keep the result file under ~2 KB (last characters only)
    if (Test-Path $resultFile) {
        $t = Get-Content $resultFile -Raw
        if ($t.Length -gt 2048) { $t.Substring($t.Length - 2048) | Set-Content $resultFile -Encoding UTF8 }
    }
}
Clear-StaleRadar

function Write-Result([string]$m) {
    $m | Set-Content -Path $resultFile -Encoding UTF8
    if (-not $silent) { Write-Output $m }
}

# Checks that the Radar server answers on port 9280 (MCP + web UI).
function Test-RadarUp {
    try {
        $c = New-Object System.Net.Sockets.TcpClient
        $iar = $c.BeginConnect('127.0.0.1', 9280, $null, $null)
        $ok = $iar.AsyncWaitHandle.WaitOne(1000, $false)
        if ($ok) { $c.EndConnect($iar); $c.Close(); return $true }
        $c.Close(); return $false
    } catch { return $false }
}

function Compare-Version([string]$a, [string]$b) {
    $pa = @($a -split '\.' | ForEach-Object { try { [int]$_ } catch { 0 } })
    $pb = @($b -split '\.' | ForEach-Object { try { [int]$_ } catch { 0 } })
    $n  = [Math]::Max($pa.Count, $pb.Count)
    for ($i = 0; $i -lt $n; $i++) {
        $x = if ($i -lt $pa.Count) { $pa[$i] } else { 0 }
        $y = if ($i -lt $pb.Count) { $pb[$i] } else { 0 }
        if ($x -gt $y) { return 1 }
        if ($x -lt $y) { return -1 }
    }
    return 0
}

# Bookkeeping so an error can safely restore the backups and restart the server.
$stopped  = $false
$backedUp = $false
$backups  = @{}

try {
    # Local version: the reference to decide whether an update is really needed (avoids needless network calls).
    $verLine = (& $radarExe --version) 2>&1 | Out-String
    $instVer = '0.0.0'
    if ($verLine -match 'radar\s+v?(\d+(?:\.\d+){1,3})') { $instVer = $Matches[1] }

    # Source of truth for the newest upstream version: comparing it with the local one decides whether to proceed.
    $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/skyhook-io/radar/releases/latest' -Headers @{ 'User-Agent' = 'opencode' } -TimeoutSec 60
    $latestTag = $release.tag_name -replace '^v', ''

    if ((Compare-Version $instVer $latestTag) -ge 0) {
        Write-Result "Radar already up to date: installed $instVer = latest $latestTag."
        exit 0
    }

    # Radar publishes one zip per release holding a single executable: download and extract it
    # BEFORE touching the installed binaries.
    $assetName = "radar_v$latestTag`_windows_amd64.zip"
    $asset = $release.assets | Where-Object { $_.name -eq $assetName } | Select-Object -First 1
    if (-not $asset) { throw "Installation asset not found: $assetName" }

    Write-Result "Downloading Radar $latestTag from GitHub..."
    $tmp  = Join-Path $env:TEMP ('radar-update-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    $zip  = Join-Path $tmp $assetName

    # Robust download: curl (streaming, retries, long timeout). Falls back to Invoke-WebRequest.
    $curl = Join-Path $env:SystemRoot 'System32\curl.exe'
    if (Test-Path $curl) {
        & $curl -L --fail --retry 4 --retry-delay 3 --connect-timeout 30 --max-time 900 --silent --output $zip $asset.browser_download_url
        if ($LASTEXITCODE -ne 0) { throw "Download failed (curl exit $LASTEXITCODE). Retry: likely a network/proxy issue." }
    } else {
        Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -UseBasicParsing -TimeoutSec 900
    }
    if (-not (Test-Path $zip) -or (Get-Item $zip).Length -eq 0) { throw "Downloaded file is empty/missing." }

    $exDir = Join-Path $tmp 'ex'
    Expand-Archive -Path $zip -DestinationPath $exDir -Force
    $newBin = Get-ChildItem $exDir -Filter 'kubectl-radar.exe' -Recurse | Select-Object -First 1
    if (-not $newBin) { throw "kubectl-radar.exe not found in the archive." }

    # Back up the current binaries so an error can restore them.
    New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
    foreach ($f in @('radar.exe', 'kubectl-radar.exe')) {
        $p = Join-Path $installDir $f
        if (Test-Path $p) {
            $bak = Join-Path $backupDir ("$f.$instVer.bak")
            Copy-Item $p $bak -Force
            $backups[$f] = $bak
        }
    }
    $backedUp = $true

    # The running binary is locked by Windows: it must be stopped to be replaced; BOTH names
    # must be updated because they are the same executable.
    Get-Process -Name 'radar' -ErrorAction SilentlyContinue | Stop-Process -Force
    $stopped = $true
    Start-Sleep -Milliseconds 500
    Copy-Item $newBin.FullName (Join-Path $installDir 'kubectl-radar.exe') -Force
    Copy-Item $newBin.FullName (Join-Path $installDir 'radar.exe') -Force

    # Restart silently and verify the server comes back online (MCP + web UI).
    Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File',('"' + $launcher + '"')) -WindowStyle Hidden
    $online = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 500
        if (Test-RadarUp) { $online = $true; break }
    }

    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue

    if ($online) {
        Write-Result "Radar updated: $instVer -> $latestTag. Server restarted and online."
    } else {
        Write-Result "Radar updated: $instVer -> $latestTag, but the server does not appear to be back up. Run start-radar-silent.ps1."
    }
    exit 0
}
catch {
    # On error, restore the backups and restart: never leave Radar stopped or partially updated.
    $restored = $false
    $restarted = $false
    if ($backedUp) {
        foreach ($f in @('radar.exe', 'kubectl-radar.exe')) {
            if ($backups.ContainsKey($f) -and (Test-Path $backups[$f])) {
                Copy-Item $backups[$f] (Join-Path $installDir $f) -Force -ErrorAction SilentlyContinue
            }
        }
        $restored = $true
    }
    if ($stopped -or $backedUp) {
        Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File',('"' + $launcher + '"')) -WindowStyle Hidden
        $restarted = $true
    }
    $actionNote = if ($restored -or $restarted) { ' Binaries restored and server restarted.' }
                 else { ' No changes were applied to the binaries.' }
    Write-Result ("Error during version check/update: " + $_.Exception.Message + "." + $actionNote)
    exit 1
}
