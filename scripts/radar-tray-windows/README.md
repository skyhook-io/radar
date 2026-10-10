# Radar silent autostart + tray helper (Windows)

Headless, silent management of the [Radar](https://github.com/skyhook-io/radar) K8s UI server on Windows,
plus a system-tray indicator with cluster switching and a version-check / auto-update command.

Everything runs **in the background, without any terminal window** (no console "flash" at startup),
**without admin rights**, and **without the Radar desktop app** — you keep using the web UI on
`http://localhost:9280` (which is also the MCP endpoint consumed by OpenCode via `opencode.json`).

---

## Context / why this exists

Radar ships as a single binary (`radar.exe` / `kubectl-radar.exe`). On this machine it was launched
manually from a terminal (`radar`), which:

- required opening a terminal every day,
- kept the terminal open for the whole session (the process ran in the foreground),
- flashed console windows on startup.

These scripts remove all of that: Radar starts silently at Windows logon, stays alive in the background,
and a tray icon reflects its state and lets you switch clusters or update it.

---

## Files

| File | Role |
|------|------|
| `start-radar-silent.ps1` | Launches `radar.exe` hidden with the correct flags. De-duplicates (exits `0` if already running). |
| `start-radar-silent.vbs` | Windows **Startup** wrapper (hidden, `WScript.Shell.Run(...,0,False)`) that runs the `.ps1` at logon. No admin. |
| `radar-tray.ps1` | System-tray indicator: 3-state dot, tooltip with the connected cluster, context menu (open UI, start, **change cluster**, check updates, exit). |
| `radar-tray-start.vbs` | Windows **Startup** wrapper for the tray indicator. |
| `radar-update.ps1` | Version check against the GitHub latest release + optional auto-update (download, backup, replace, silent restart) + log/tmp cleanup. |

> **Where they live in production:** the `.ps1` files are installed in `C:\Users\<you>\.radar\`,
> the two `.vbs` files in `...\Start Menu\Programs\Startup\`. Paths are hard-coded inside the scripts —
> adjust them if you install elsewhere.

---

## How it works

### 1. Silent autostart (`start-radar-silent.*`)
A VBS in the Startup folder runs the PowerShell script in a **completely hidden window**
(`/WindowStyle Hidden` + `WScript.Shell.Run(..., style 0, ...)`), so nothing flashes on screen at logon.
The script starts Radar as a background process:

```powershell
Start-Process radar.exe -ArgumentList @('--prometheus-single-cluster','-no-browser') -WindowStyle Hidden
```

- `--prometheus-single-cluster` — avoids the *"Historical cluster scope could not be verified"* metric
  reconstruction warning (the Prometheus/Kubecost backend contains a single cluster).
- `-no-browser` — nothing opens at login; you open `http://localhost:9280` when you need it.
- De-duplication: if a `radar` process already exists the script exits `0` and does nothing
  (prevents a port-9280 conflict).

### 2. State detection (`radar-tray.ps1`)
The tray indicator polls Radar every 5 seconds and shows a colored dot:

| Color | Meaning | Detection |
|-------|---------|-----------|
| 🟢 green | Server up **and** cluster reachable | `GET /api/namespaces` → `200` |
| 🔴 red | Server up but **cluster NOT reachable** (e.g. VPN/SSO tunnel down) | `GET /api/namespaces` → `503`/error |
| ⚪ gray | Server down / not running | TCP connect to port 9280 fails |

The hover tooltip shows the **connected cluster**, read from `GET /api/contexts` (`isCurrent`), e.g.
`Radar: ACTIVE - <cluster-name>`.

#### Radar HTTP API endpoints used
These were discovered by inspecting the Radar web UI asset:
- `GET /api/contexts` — list of kubeconfig contexts with `isCurrent`.
- `POST /api/contexts/<url-encoded-name>` — switch cluster **without restarting**.
- `GET /api/namespaces` — used as a connectivity probe (`200` = cluster reachable).
- `GET /health` — returns only the SPA `index.html` (**not** useful for a health probe).

> Note on switching to an **unreachable** cluster: the `POST` may return `500`, but the context
> **still changes** (`isCurrent` updates). That is expected — the tray then shows the 🔴 red state,
> which is exactly the desired "cluster not reachable" signal.

### 3. Cluster switching (right-click → "Change cluster")
The context menu has a dynamic **"Change cluster"** submenu populated from `/api/contexts`
(current one is checked). Clicking an entry calls the switch endpoint, then **immediately** refreshes
the icon/tooltip/menu and shows a definitive toast, e.g. `Cluster: <cluster-name> - connected`.

### 4. Version check + auto-update (`radar-update.ps1`)
Triggered from the tray menu ("Check for updates…") or by running the script directly:

1. Read installed version: `radar.exe --version`.
2. Query the **latest GitHub release** of `skyhook-io/radar` (`GET /releases/latest`).
3. If already latest → report and exit `0` (no-op).
4. Otherwise:
   - Download the asset `radar_v<tag>_windows_amd64.zip` using **`curl.exe`** (`--retry 4 --max-time 900`)
     with an `Invoke-WebRequest` fallback (large timeouts — GitHub can be slow behind corporate proxies).
   - Extract, **back up** the current binaries to `~/.radar/backup/`,
   - **stop** Radar, replace both binaries, **restart silently** (MCP + web UI stay up).
5. **Log/tmp rotation** (`Clear-StaleRadar`): removes stale `Temp\radar-install-*`, `radar-update-*`,
   `radar-ai-*` (>7 days), old `radar-*.log` in `Temp\opencode` (>30 days), interrupted `.tmp` downloads
   in `~/.radar/updates` (>2 days), caps the result file at 2 KB.

---

## Key technical findings (so you don't have to rediscover them)

- **Radar is a server, not a desktop window.** `radar.exe` serves both the web UI and the MCP endpoint
  on port `9280` (`http://localhost:9280/mcp`). The "desktop" wrapper is only a WebView2 shell.
- **Single-binary layout.** The release asset `radar_v<tag>_windows_amd64.zip` contains **one** binary,
  `kubectl-radar.exe`. The install folder keeps the **same identical binary twice**, renamed as both
  `kubectl-radar.exe` and `radar.exe` (same byte size, 133773312). An update must replace **both** names.
- **`NotifyIcon` tooltip refresh gotcha.** Changing only `NotifyIcon.Text` does **not** update the hover
  tooltip on Windows (the shell caches it). You must **regenerate the icon** — the helper toggles
  `Visible` off/on + reassigns `Icon` and `Text` — for the tooltip to refresh.
- **Use Windows PowerShell 5.1 (`powershell.exe`), not pwsh 7**, for the tray: `NotifyIcon` is unreliable
  under pwsh 7 when launched from a console. WinForms + Drawing are loaded explicitly.
- **AWS profile note.** Different clusters may use different AWS profiles, and some require a VPN/SSO
  tunnel; when that tunnel is absent, Radar returns `503` on `/api/namespaces` → the tray correctly turns
  **red**.
- **No admin required.** Everything uses the user Startup folder / user-scoped paths. Task Scheduler was
  avoided because registering a task needs admin on this enterprise machine ("Accesso negato").

---

## Setup / install (one time)

1. Put the three `.ps1` files in `C:\Users\<you>\.radar\`.
2. Put the two `.vbs` files in the Startup folder:
   `C:\Users\<you>\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\`.
3. (Optional, if your Radar install dir differs) update the hard-coded paths inside the scripts.
4. Log out/in (or run the `.vbs` once) — Radar starts hidden and the tray icon appears.

### Manual control
```powershell
# Start Radar silently (background, no terminal):
powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\<you>\.radar\start-radar-silent.ps1

# Launch the tray indicator:
powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\<you>\.radar\radar-tray.ps1

# Check for updates (blocking, prints result):
powershell -NoProfile -ExecutionPolicy Bypass -File C:\Users\<you>\.radar\radar-update.ps1
```

### Uninstalling / disabling
- Remove the two `.vbs` files from the Startup folder.
- To also stop Radar: `Stop-Process -Name radar`.

### Port & configuration

The helpers target Radar's HTTP API on its default port **`9280`** (web UI + MCP endpoint
`http://localhost:9280/mcp`). On this version the port is assumed to be **`9280`**: if you run the
server on a custom port, update `$script:base` in `radar-tray.ps1` and the port used by the
updater's readiness probe in `radar-update.ps1`.

> Note: the macOS companion (`scripts/radar-tray-macos/`) auto-detects the port from
> `~/.radar/mcp-port` instead; the Windows helper keeps the documented `9280` default for now.

---

## Compatibility

- Windows 10/11, user-scoped (no admin).
- Windows PowerShell 5.1 (`powershell.exe`) required for the tray.
- Radar `>= 1.15.0` (single-binary server / CLI layout).
- Update source: GitHub `skyhook-io/radar` releases.
