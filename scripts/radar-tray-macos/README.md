# Radar silent autostart + menu-bar helper (macOS) — MVP

macOS port of the [Windows tray helper](../radar-tray-windows/). Runs the [Radar](https://github.com/skyhook-io/radar)
K8s UI server headlessly in the background (no terminal window, no admin) and adds a **menu-bar
indicator** with cluster switching and a version-check / auto-update command.

> **Status:** MVP scaffold, **not yet tested on a real Mac**. The Radar server itself is
> cross-platform and needs no changes; only the helper is being ported.

## Files

| File | Role |
|------|------|
| `start-radar.sh` | Starts the `radar` binary silently (`--prometheus-single-cluster -no-browser`), de-duplicates. |
| `radar-update.sh` | Version check + auto-update for macOS (downloads the darwin tar.gz for the local arch, removes quarantine, backs up, replaces, restarts, verifies port 9280) + log rotation. |
| `radar-tray.py` | Menu-bar indicator (Python `rumps` + AppKit): green/red/gray dot, cluster in the title, menu (Open, Start, Change cluster, Check for updates, Quit). |
| `com.radar.tray.plist` | LaunchAgent: launches the menu-bar app at login (`RunAtLoad`), keeps it alive. |
| `README.md` | This file. |

## Install / setup (on the Mac)

1. **Radar binary** — install the server binary to `~/.radar/radar`:
   ```bash
   mkdir -p ~/.radar
   # download radar_v<tag>_darwin_<arm64|amd64>.tar.gz from the GitHub release,
   # extract it, and place the `radar` binary at ~/.radar/radar
   ```
2. **Scripts** — copy `start-radar.sh` and `radar-update.sh` to `~/.radar/` and make them executable:
   ```bash
   chmod +x ~/.radar/start-radar.sh ~/.radar/radar-update.sh
   ```
3. **Menu-bar app** — copy `radar-tray.py` to `~/.radar/` and install `rumps` (PyObjC) **for the same
   Python that will run the app** (important for the LaunchAgent, see note below):
   ```bash
   pip3 install --user rumps
   /usr/bin/python3 -c 'import rumps'   # confirm rumps is importable by the login interpreter
   ```
   Quick manual start while testing:
   ```bash
   python3 ~/.radar/radar-tray.py
   ```
4. **(Optional) autostart at login** — copy `com.radar.tray.plist` to `~/Library/LaunchAgents/`,
   edit the **three `/CHANGE/ME` paths** (script path + the two log paths) — they must be
   **absolute** — then:
   ```bash
   launchctl load ~/Library/LaunchAgents/com.radar.tray.plist
   ```
   To unload/remove: `launchctl unload ...` and delete the plist. The plist runs `/usr/bin/python3`;
   if `rumps` was installed for a different interpreter, point `ProgramArguments` at the exact
   Python that has it (e.g. `/opt/homebrew/bin/python3`), or the logged-in tray will keep
   restarting without its dependency.

## Manual control
```bash
bash ~/.radar/start-radar.sh        # start Radar silently
bash ~/.radar/radar-update.sh       # check + auto-update (prints result)
```

## How it works (same logic as the Windows port)
- States polled every 5 s: 🟢 green (server + cluster reachable via `GET /api/namespaces` 200),
  🔴 red (server up, cluster unreachable → e.g. VPN/SSO down), ⚪ gray (server down).
- Cluster from `GET /api/contexts` (`isCurrent`) shown in the menu-bar title; switching via
  `POST /api/contexts/<name>` (no restart), with the real outcome surfaced on failure.
- Auto-update compares `radar --version` with the GitHub latest release, downloads
  `radar_v<tag>_darwin_<arch>.tar.gz`, clears the quarantine attribute, backs up, replaces,
  restarts and verifies port 9280.

## macOS-specific notes / gotchas
- **No Windows-style file lock**: a running executable can be replaced on macOS, so the update is
  simpler than on Windows (no forced stop required; we still stop for a clean port-9280 bind).
- **Gatekeeper/quarantine**: downloaded binaries carry `com.apple.quarantine`; the update script
  strips it (`xattr -d`), otherwise macOS may block execution.
- **Two architectures**: `uname -m` selects `arm64` vs `amd64` asset.
- **Port auto-detected**: the server port is read from `~/.radar/mcp-port` (Radar's actual API/MCP
  port), falling back to the default `9280` when the file is missing. Tray probes and the updater's
  readiness check both use it, so a custom port is handled automatically.
- This scaffold uses colored dot **PNGs generated at runtime** via AppKit (the macOS menu bar
  renders template icons; verify they display if you prefer monochrome).

## Untested areas to validate on the Mac
1. `rumps` menu-bar rendering of colored icons + the `Change cluster` submenu rebuild.
2. `nc` port check availability on macOS (Netcat ships with macOS; fallback to a Python check if missing).
3. Quarantine removal path in `radar-update.sh`.
4. LaunchAgent loads and keeps the tray alive (log paths must be writable).
