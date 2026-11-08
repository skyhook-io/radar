#!/usr/bin/env bash
# Launches a Radar Desktop AppImage on a headless X server and checks that the
# app starts: its local API answers and the window's webview loaded the UI.
#
# Usage: scripts/appimage/smoke.sh <AppImage> <output dir>
#
# Needs Xvfb, xdotool, ImageMagick (import), curl and procps. Uses a fresh
# HOME, so no kubeconfig: Radar starts disconnected, which is enough to load
# the UI. Writes the app log and a screenshot of its window to <output dir>.
# Run as root (a CI container), it creates an unprivileged user to run Radar.
set -euo pipefail

usage="usage: scripts/appimage/smoke.sh <AppImage> <output dir>"
appimage="${1:?$usage}"
out="${2:?$usage}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
appimage="$(cd "$(dirname "$appimage")" && pwd)/$(basename "$appimage")"

run_as=()
if [[ "$(id -u)" == 0 ]]; then
  id smoke >/dev/null 2>&1 || useradd -m smoke
  run_as=(runuser -u smoke --)
  chmod 0777 "$out"
fi

home="$("${run_as[@]}" mktemp -d)"
runtime_dir="$("${run_as[@]}" mktemp -d)"
display=":99"

Xvfb "$display" -screen 0 1400x900x24 >"$out/xvfb.log" 2>&1 &
xvfb_pid=$!
radar_pid=""
cleanup() {
  [[ -n "$radar_pid" ]] && kill "$radar_pid" 2>/dev/null || true
  kill "$xvfb_pid" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 20); do
  DISPLAY="$display" xdotool getdisplaygeometry >/dev/null 2>&1 && break
  sleep 0.5
done

fail() {
  echo "smoke: FAIL: $*" >&2
  echo "smoke: last lines of the Radar log:" >&2
  tail -n 40 "$out/radar.log" >&2 || true
  exit 1
}

# FUSE is usually unavailable in CI containers; the AppImage extracts itself.
# A test run sends no version check or usage report.
"${run_as[@]}" env -i HOME="$home" PATH=/usr/local/bin:/usr/bin:/bin XDG_RUNTIME_DIR="$runtime_dir" \
  DISPLAY="$display" XDG_SESSION_TYPE=x11 LANG=C.UTF-8 APPIMAGE_EXTRACT_AND_RUN=1 \
  RADAR_DISABLE_VERSION_CHECK=1 RADAR_USAGE_REPORTING=off \
  "$appimage" >"$out/radar.log" 2>&1 &
launcher_pid=$!

# Radar writes its port to ~/.radar/mcp-port once the server listens.
port=""
for _ in $(seq 1 90); do
  [[ -f "$home/.radar/mcp-port" ]] && port="$(head -n 1 "$home/.radar/mcp-port")"
  if [[ -n "$port" ]] && curl -sf "http://127.0.0.1:$port/api/health" >/dev/null; then break; fi
  kill -0 "$launcher_pid" 2>/dev/null || fail "the AppImage exited during startup"
  sleep 2
done
[[ -n "$port" ]] || fail "Radar did not write its port file within 180s"
curl -sf "http://127.0.0.1:$port/api/health" >"$out/health.json" || fail "the local API did not answer /api/health"
echo "smoke: local API answers on port $port"

argv0() { tr '\0' '\n' <"/proc/$1/cmdline" 2>/dev/null | head -n 1; }
for p in $(pgrep -f radar-desktop || true); do
  if [[ "$(argv0 "$p")" == */usr/bin/radar-desktop ]]; then radar_pid="$p"; fi
done
[[ -n "$radar_pid" ]] || fail "no radar-desktop process from the AppImage"

# Only the loaded UI opens Radar's live-update stream, so a connected client
# means the webview rendered the app rather than an error or blank page.
clients=0
for _ in $(seq 1 60); do
  clients="$(curl -sf "http://127.0.0.1:$port/metrics" | awk '/^radar_sse_connected_clients /{print $2}')"
  [[ "${clients%.*}" -ge 1 ]] 2>/dev/null && break
  sleep 2
done
[[ "${clients%.*}" -ge 1 ]] 2>/dev/null || fail "the webview never loaded the UI (no live-update client after 120s)"
echo "smoke: the webview loaded the UI ($clients live-update client)"

webkit=""
for p in $(pgrep -f WebKitWebProcess || true); do
  [[ "$(ps -o ppid= -p "$p" | tr -d ' ')" == "$radar_pid" ]] && webkit="$p"
done
[[ -n "$webkit" ]] || fail "no WebKitWebProcess started by Radar"
echo "smoke: WebKit web process $webkit runs $(argv0 "$webkit")"

window="$(DISPLAY="$display" xdotool search --onlyvisible --pid "$radar_pid" 2>/dev/null | head -n 1 || true)"
[[ -n "$window" ]] || fail "Radar has no visible window"
sleep 5
DISPLAY="$display" import -window "$window" "$out/window.png" || echo "smoke: could not capture the window" >&2
echo "smoke: PASS"
