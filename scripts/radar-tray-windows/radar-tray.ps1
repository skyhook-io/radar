# radar-tray.ps1 - Radar status indicator in the Windows notification area (tray).
# Launched automatically at logon (via VBS in the Startup folder) with a hidden window.
# INTERNAL: requires Windows PowerShell 5.1 (powershell.exe) - NotifyIcon is unreliable
# under pwsh 7 when launched from a console.
#
# States (dot color):
#   green  = server up AND cluster reachable (GET /api/namespaces 200)
#   red    = server up but cluster NOT reachable (e.g. VPN/SSO tunnel down)
#   gray   = server down / not running (port 9280 does not answer)
# Tooltip shows the current cluster (from /api/contexts isCurrent).
# Menu: Open Radar  |  Start Radar  |  Change cluster (submenu)  |  Check for updates  |  Exit

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

# Single indicator instance (named mutex)
$mutex = New-Object System.Threading.Mutex($false, 'Global\RadarTrayOpenCode')
if (-not $mutex.WaitOne(0, $false)) { exit 0 }

$script:base     = 'http://localhost:9280'
$script:URL      = $script:base
$script:launcher = Join-Path $env:USERPROFILE '.radar\start-radar-silent.ps1'
$script:state    = $null                # down | clusterdown | up
$script:lastTip  = ''
$script:tc       = 0
$script:forceRefresh = $true
$script:updatePID    = $null            # PID of the in-flight update (async, does not block the UI)
$script:updateResult = Join-Path $env:USERPROFILE '.radar\last-update-result.txt'

# --- API helpers ---
# Server up? (TCP connect to port 9280)
function Test-Radar {
    try {
        $c = New-Object System.Net.Sockets.TcpClient
        $iar = $c.BeginConnect('127.0.0.1', 9280, $null, $null)
        $ok = $iar.AsyncWaitHandle.WaitOne(1500, $false)
        if ($ok) { $c.EndConnect($iar); $c.Close(); return $true }
        $c.Close(); return $false
    } catch { return $false }
}

# Cluster reachable? GET /api/namespaces must return 200
function Test-Cluster {
    try {
        $r = Invoke-WebRequest -Uri ($script:base + '/api/namespaces') -UseBasicParsing -TimeoutSec 6
        return ($r.StatusCode -eq 200)
    } catch { return $false }
}

function Get-Contexts {
    try { return @(Invoke-RestMethod -Uri ($script:base + '/api/contexts') -TimeoutSec 6) }
    catch { return @() }
}

# Turn an "arn:aws:eks:...:cluster/<name>" into a friendly "<name>"
function Friendly-Name([string]$arn) {
    if ($arn -match 'cluster/([^/]+)$') { return $Matches[1] }
    return $arn
}

# Current cluster: from the isCurrent context (fallback: last context in settings.json)
function Get-CurrentCluster {
    try {
        $ctxs = Get-Contexts
        if ($ctxs) {
            $cur = $ctxs | Where-Object { $_.isCurrent } | Select-Object -First 1
            if ($cur) { return (Friendly-Name $cur.name) }
        }
    } catch { }
    try {
        $p = Join-Path $env:USERPROFILE '.radar\settings.json'
        if (Test-Path $p) {
            $s = Get-Content $p -Raw | ConvertFrom-Json
            $n = $s.lastDesktopContext.name
            if ($n) { return (Friendly-Name $n) }
        }
    } catch { }
    return $null
}

# Switch cluster: POST /api/contexts/<name>, then VERIFY the real outcome (rejections surfaced).
# Captures the previous context so a failure toast can report where we actually still are.
function Switch-RadarContext([string]$ctxName) {
    $friendly = Friendly-Name $ctxName
    $before = $null
    try {
        $b = (Get-Contexts | Where-Object { $_.isCurrent } | Select-Object -First 1)
        if ($b) { $before = $b.name }
    } catch { }
    $postErr = $null
    try {
        $enc = [uri]::EscapeDataString($ctxName)
        # NB: on an unreachable cluster the POST may return 500 but the context still changes
        try { $null = Invoke-RestMethod -Method Post -Uri ($script:base + '/api/contexts/' + $enc) -TimeoutSec 40 }
        catch { $postErr = $_.Exception.Message }
        Start-Sleep -Milliseconds 1200
        $ctxs = Get-Contexts
        Update-State $ctxs
        Populate-ClusterMenu $ctxs
        $cur   = $ctxs | Where-Object { $_.isCurrent } | Select-Object -First 1
        $now   = if ($cur) { $cur.name } else { $null }
        $target = if ($cur) { Friendly-Name $cur.name } else { $friendly }
        if ($now -ne $ctxName) {
            # The context did not become the requested one -> the switch was rejected/failed.
            $still = if ($before) { (Friendly-Name $before) } else { $target }
            $detail = if ($postErr) { " ($postErr)" } else { '' }
            $tray.ShowBalloonTip(6000, 'Radar', "Cluster switch FAILED (still on $still).$detail", [System.Windows.Forms.ToolTipIcon]::Error)
        } else {
            $up = Test-Cluster
            $esito = if ($up) { 'connected' } else { 'unreachable' }
            $icon  = if ($up) { [System.Windows.Forms.ToolTipIcon]::Info } else { [System.Windows.Forms.ToolTipIcon]::Warning }
            $tray.ShowBalloonTip(4000, 'Radar', "Cluster: $target - $esito", $icon)
        }
    } catch {
        $tray.ShowBalloonTip(5000, 'Radar', 'Cluster switch error: ' + $_.Exception.Message, [System.Windows.Forms.ToolTipIcon]::Error)
    }
    $script:forceRefresh = $true
}

# Compute and apply the state (gray/red/green) and detect the end of an async update.
function Update-State([object[]]$ctxs = @()) {
    # When the async update process exits, surface its result.
    if ($script:updatePID) {
        if (-not (Get-Process -Id $script:updatePID -ErrorAction SilentlyContinue)) {
            Start-Sleep -Milliseconds 600
            $msg = 'No result available.'
            if (Test-Path $script:updateResult) { $msg = (Get-Content $script:updateResult -Raw).Trim() }
            if ($msg.Length -gt 140) { $msg = $msg.Substring(0, 140) + '...' }
            $tray.ShowBalloonTip(6000, 'Radar update', $msg, [System.Windows.Forms.ToolTipIcon]::Info)
            $script:updatePID = $null
        }
    }
    if (-not (Test-Radar)) {
        if ($script:state -ne 'down') {
            Set-Status $script:iconOff 'Radar: DOWN'
            $script:state = 'down'
        }
        return
    }
    if (-not $ctxs -or $ctxs.Count -eq 0) { $ctxs = Get-Contexts }
    $cur = $ctxs | Where-Object { $_.isCurrent } | Select-Object -First 1
    $cl  = if ($cur) { Friendly-Name $cur.name } else { Get-CurrentCluster }
    $up  = Test-Cluster
    if ($up) {
        $tip = 'Radar: ACTIVE' + $(if ($cl) { ' - ' + $cl } else { '' })
        if ($script:state -ne 'up' -or $script:lastTip -ne $tip) {
            Set-Status $script:iconUp $tip
            $script:state = 'up'; $script:lastTip = $tip
        }
    } else {
        $tip = 'Radar: cluster unreachable' + $(if ($cl) { ' (' + $cl + ')' } else { '' })
        if ($script:state -ne 'clusterdown' -or $script:lastTip -ne $tip) {
            Set-Status $script:iconDown $tip
            $script:state = 'clusterdown'; $script:lastTip = $tip
        }
    }
}

# (Re)build the "Change cluster" submenu dynamically.
function Populate-ClusterMenu([object[]]$ctxs) {
    $clusterMenu.MenuItems.Clear()
    if (-not $ctxs -or $ctxs.Count -eq 0) {
        $mi = New-Object System.Windows.Forms.MenuItem('(no contexts)')
        $mi.Enabled = $false
        $null = $clusterMenu.MenuItems.Add($mi)
        return
    }
    foreach ($c in $ctxs) {
        $mi = New-Object System.Windows.Forms.MenuItem((Friendly-Name $c.name))
        $mi.Checked = [bool]$c.isCurrent
        $mi.Tag = $c.name
        $mi.Add_Click({ Switch-RadarContext $this.Tag })
        $null = $clusterMenu.MenuItems.Add($mi)
    }
}

# --- Icons ---
function New-TrayIcon([System.Drawing.Color]$color) {
    $bmp = New-Object System.Drawing.Bitmap(16, 16)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $b = New-Object System.Drawing.SolidBrush($color)
    $g.FillEllipse($b, 1, 1, 14, 14)
    $g.Dispose(); $b.Dispose()
    $ic = [System.Drawing.Icon]::FromHandle($bmp.GetHicon())
    $bmp.Dispose()
    return $ic
}
$script:iconUp    = New-TrayIcon ([System.Drawing.Color]::LimeGreen)
$script:iconDown  = New-TrayIcon ([System.Drawing.Color]::Firebrick)   # cluster unreachable
$script:iconOff   = New-TrayIcon ([System.Drawing.Color]::Gray)       # server down

# --- Tray and menu construction ---
$tray = New-Object System.Windows.Forms.NotifyIcon
$tray.Icon = $script:iconOff
$tray.Text = 'Radar: starting...'
$tray.Visible = $true

$open   = New-Object System.Windows.Forms.MenuItem('Open Radar (web UI)')
$start  = New-Object System.Windows.Forms.MenuItem('Start Radar')
$script:clusterMenu = New-Object System.Windows.Forms.MenuItem('Change cluster')
$check  = New-Object System.Windows.Forms.MenuItem('Check for updates...')
$sep    = New-Object System.Windows.Forms.MenuItem('-')
$quit   = New-Object System.Windows.Forms.MenuItem('Exit')
$tray.ContextMenu = (New-Object System.Windows.Forms.ContextMenu)
$null = $tray.ContextMenu.MenuItems.AddRange(@($open, $start, $clusterMenu, $sep, $check, $quit))

$open.Add_Click({ Start-Process $script:URL })
$tray.Add_DoubleClick({ Start-Process $script:URL })

$start.Add_Click({
    # Avoid racing with the async update (Radar is stopped while the ~133MB binaries are replaced).
    if ($script:updatePID) {
        $tray.ShowBalloonTip(2500, 'Radar', 'Update in progress: start disabled until it finishes.', [System.Windows.Forms.ToolTipIcon]::Info)
        return
    }
    if (-not (Test-Radar)) {
        # Quoted path so it also works when %USERPROFILE% contains spaces.
        Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File',('"' + $script:launcher + '"')) -WindowStyle Hidden
        Start-Sleep -Milliseconds 400
        $script:forceRefresh = $true
    }
})

$check.Add_Click({
    if ($script:updatePID) {
        $tray.ShowBalloonTip(2500, 'Radar', 'Check/update already in progress...', [System.Windows.Forms.ToolTipIcon]::Info)
        return
    }
    $tray.ShowBalloonTip(2000, 'Radar', 'Checking for updates...', [System.Windows.Forms.ToolTipIcon]::Info)
    try { Remove-Item $script:updateResult -Force -ErrorAction SilentlyContinue } catch { }
    $updateScript = Join-Path $env:USERPROFILE '.radar\radar-update.ps1'
    # Async (no -Wait): keeps the tray message-pump responsive; the result is shown when done.
    $p = Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File',('"' + $updateScript + '"'),'-silent') -WindowStyle Hidden -PassThru
    $script:updatePID = $p.Id
})

$quit.Add_Click({
    $tray.Visible = $false
    $tray.Dispose()
    [System.Windows.Forms.Application]::Exit()
})

# Update the icon+tooltip by regenerating the icon (forces the shell to show the new tooltip -
# changing NotifyIcon.Text alone does not refresh the hover tooltip on Windows).
# NotifyIcon.Text has a length limit: truncate so a status refresh cannot fail.
function Set-Status($icon, $tip) {
    if ($tip -and $tip.Length -gt 63) { $tip = $tip.Substring(0, 60) + '...' }
    $tray.Visible = $false
    $tray.Icon = $icon
    $tray.Text = $tip
    $tray.Visible = $true
}

$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 5000
$timer.Add_Tick({
    Update-State
    $script:tc++
    if (($script:tc % 3 -eq 0) -or $script:forceRefresh) {
        Populate-ClusterMenu (Get-Contexts)
        $script:forceRefresh = $false
    }
})

# Immediate initial state + first menu population
Update-State
Populate-ClusterMenu (Get-Contexts)

# START the polling timer (without this the tray never refreshes after startup)
$timer.Start()

[System.Windows.Forms.Application]::Run()

$mutex.ReleaseMutex()
