#!/usr/bin/env bash
# Launches a Radar Desktop AppImage on a headless display and checks that the
# app starts: its local API answers and the window's webview loaded the UI.
#
# Usage: scripts/appimage/smoke.sh <AppImage> <output dir> [x11|wayland]
#   x11      (default) an X server (Xvfb), GDK_BACKEND not set, so the
#            AppImage uses its default backend, X11
#   wayland  a Wayland compositor (weston, headless, kiosk shell) without
#            Xwayland and GDK_BACKEND=wayland: the AppImage must use the
#            backend the user chose, and processes it starts must get it too
#
# Needs curl, procps and ImageMagick; for x11 Xvfb, xdotool and import, for
# wayland weston and weston-screenshooter; as root also openssl, python3 and
# update-ca-certificates (Debian/Ubuntu). Uses a fresh
# HOME, so no kubeconfig: Radar starts disconnected, which is enough to load
# the UI. Also opens a link through Radar with a stand-in xdg-open to check
# that processes Radar starts get the launch environment and folder, not the
# AppImage's. Writes the app log and a screenshot of its window to <output dir>.
# Run as root (a CI container), it creates an unprivileged user to run Radar
# and checks that the webview can make HTTPS requests: the UI fetches the
# repository's star count from api.github.com, which is pointed at a local
# HTTPS server whose certificate is added to the system's trusted ones.
set -euo pipefail

usage="usage: scripts/appimage/smoke.sh <AppImage> <output dir> [x11|wayland]"
appimage="${1:?$usage}"
out="${2:?$usage}"
session="${3:-x11}"
case "$session" in
  x11 | wayland) ;;
  *) echo "$usage" >&2; exit 2 ;;
esac
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
launch_dir="$("${run_as[@]}" mktemp -d)"
# Values the AppImage's GTK setup overrides; started processes must get these back.
user_data_dirs="/usr/local/share:/usr/share:/opt/radar-smoke/share"
display=":99"
wayland_socket="wayland-smoke"

# Stand-in xdg-open: records the environment and folder it was started with.
tools="$(mktemp -d)"
cat >"$tools/xdg-open" <<EOF
#!/bin/sh
{ echo "cwd=\$(pwd)"; env; } > "$out/xdg-open-env.txt"
EOF
chmod 0755 "$tools"
chmod 0755 "$tools/xdg-open"

display_pid=""
radar_pid=""
https_pid=""
cleanup() {
  [[ -n "$radar_pid" ]] && kill "$radar_pid" 2>/dev/null || true
  [[ -n "$https_pid" ]] && kill "$https_pid" 2>/dev/null || true
  [[ -n "$display_pid" ]] && kill "$display_pid" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
  echo "smoke: FAIL: $*" >&2
  echo "smoke: last lines of the Radar log:" >&2
  tail -n 40 "$out/radar.log" >&2 || true
  exit 1
}

# wayland_screenshot <file>: captures the compositor's output. The kiosk shell
# shows Radar's window over the whole output, so this is Radar's window.
wayland_screenshot() {
  local shot_dir
  shot_dir="$("${run_as[@]}" mktemp -d)"
  (cd "$shot_dir" && "${run_as[@]}" env XDG_RUNTIME_DIR="$runtime_dir" WAYLAND_DISPLAY="$wayland_socket" \
    weston-screenshooter >>"$out/screenshooter.log" 2>&1) || return 1
  local shots=("$shot_dir"/*.png)
  [[ -f "${shots[0]}" ]] && cp "${shots[0]}" "$1"
}

if [[ "$session" == x11 ]]; then
  Xvfb "$display" -screen 0 1400x900x24 >"$out/xvfb.log" 2>&1 &
  display_pid=$!
  for _ in $(seq 1 20); do
    DISPLAY="$display" xdotool getdisplaygeometry >/dev/null 2>&1 && break
    sleep 0.5
  done
  session_env=(DISPLAY="$display" XDG_SESSION_TYPE=x11)
else
  # No --xwayland: an app that asks for X11 cannot start here.
  "${run_as[@]}" env XDG_RUNTIME_DIR="$runtime_dir" weston --backend=headless --renderer=pixman \
    --shell=kiosk-shell.so --width=1400 --height=900 --socket="$wayland_socket" --debug \
    >"$out/weston.log" 2>&1 &
  display_pid=$!
  for _ in $(seq 1 40); do
    [[ -S "$runtime_dir/$wayland_socket" ]] && break
    sleep 0.5
  done
  [[ -S "$runtime_dir/$wayland_socket" ]] || { cat "$out/weston.log" >&2; fail "weston did not start"; }
  # The window check below counts colours; it means nothing if the empty
  # output already passes it.
  wayland_screenshot "$out/empty-output.png" || fail "could not capture weston's output"
  empty_colours="$(convert "$out/empty-output.png" -format %k info:)"
  [[ "$empty_colours" -lt 100 ]] || fail "weston's empty output already has $empty_colours colours; the window check would prove nothing"
  session_env=(WAYLAND_DISPLAY="$wayland_socket" XDG_SESSION_TYPE=wayland GDK_BACKEND=wayland)
fi

# The webview's HTTPS requests go through the bundled GIO TLS module. The UI
# itself is local HTTP, so check the module and its libraries load on a
# system without glib-networking.
extract_dir="$(mktemp -d)"
(cd "$extract_dir" && "$appimage" --appimage-extract >/dev/null)
tls_module="$extract_dir/squashfs-root/usr/lib/gio/modules/libgiognutls.so"
[[ -f "$tls_module" ]] || fail "the AppImage has no GIO TLS module (usr/lib/gio/modules/libgiognutls.so)"
missing="$(ldd "$tls_module" | grep 'not found' || true)"
[[ -z "$missing" ]] || fail "the GIO TLS module cannot load: $missing"
echo "smoke: the GIO TLS module and its libraries load"

https_log="$out/https-requests.log"
if [[ "$(id -u)" == 0 ]]; then
  tls_dir="$(mktemp -d)"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=api.github.com" \
    -addext "subjectAltName=DNS:api.github.com" -addext "basicConstraints=critical,CA:TRUE" \
    -keyout "$tls_dir/key.pem" -out "$tls_dir/cert.pem" 2>/dev/null
  cp "$tls_dir/cert.pem" /usr/local/share/ca-certificates/radar-smoke.crt
  update-ca-certificates >/dev/null 2>&1
  echo "127.0.0.1 api.github.com" >>/etc/hosts
  cat >"$tls_dir/server.py" <<'EOF'
import http.server, ssl, sys
log, cert, key = sys.argv[1:4]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a") as f:
            f.write(f"GET {self.path}\n")
        body = b'{"stargazers_count": 4242}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 443), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
EOF
  python3 "$tls_dir/server.py" "$https_log" "$tls_dir/cert.pem" "$tls_dir/key.pem" &
  https_pid=$!
fi

# FUSE is usually unavailable in CI containers; the AppImage extracts itself.
# A test run sends no version check or usage report.
(cd "$launch_dir" && exec "${run_as[@]}" env -i HOME="$home" PATH="$tools:/usr/local/bin:/usr/bin:/bin" \
  XDG_RUNTIME_DIR="$runtime_dir" "${session_env[@]}" LANG=C.UTF-8 APPIMAGE_EXTRACT_AND_RUN=1 \
  XDG_DATA_DIRS="$user_data_dirs" GTK_THEME= \
  RADAR_DISABLE_VERSION_CHECK=1 RADAR_USAGE_REPORTING=off \
  "$appimage" >"$out/radar.log" 2>&1) &
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

if [[ -n "$https_pid" ]]; then
  for _ in $(seq 1 30); do grep -q '^GET /repos/skyhook-io/radar' "$https_log" 2>/dev/null && break; sleep 2; done
  grep -q '^GET /repos/skyhook-io/radar' "$https_log" 2>/dev/null ||
    fail "the webview made no HTTPS request (the star count fetch from api.github.com never arrived)"
  echo "smoke: the webview made an HTTPS request, verified against the system's trusted certificates"
else
  echo "smoke: not root, skipping the webview HTTPS check"
fi

webkit=""
for p in $(pgrep -f WebKitWebProcess || true); do
  [[ "$(ps -o ppid= -p "$p" | tr -d ' ')" == "$radar_pid" ]] && webkit="$p"
done
[[ -n "$webkit" ]] || fail "no WebKitWebProcess started by Radar"
echo "smoke: WebKit web process $webkit runs $(argv0 "$webkit")"

curl -sf -X POST -H 'Content-Type: application/json' -d '{"url":"https://example.com/"}' \
  "http://127.0.0.1:$port/api/desktop/open-url" || fail "Radar refused to open a link"
for _ in $(seq 1 20); do [[ -s "$out/xdg-open-env.txt" ]] && break; sleep 0.5; done
[[ -s "$out/xdg-open-env.txt" ]] || fail "Radar did not start xdg-open for a link"
grep -qx "cwd=$launch_dir" "$out/xdg-open-env.txt" || fail "xdg-open did not start in the launch folder: $(grep '^cwd=' "$out/xdg-open-env.txt")"
leaked="$(grep -E '^(GTK_PATH|GTK_EXE_PREFIX|GTK_DATA_PREFIX|GDK_PIXBUF_MODULE_FILE|GSETTINGS_SCHEMA_DIR|GIO_MODULE_DIR|WEBKIT_INJECTED_BUNDLE_PATH|APPDIR|APPIMAGE|OWD|RADAR_APPIMAGE_[A-Z_]*)=' "$out/xdg-open-env.txt" || true)"
[[ -z "$leaked" ]] || fail "xdg-open inherited the AppImage's environment: $leaked"
grep -qx "XDG_DATA_DIRS=$user_data_dirs" "$out/xdg-open-env.txt" ||
  fail "xdg-open did not get the launch XDG_DATA_DIRS back: $(grep '^XDG_DATA_DIRS=' "$out/xdg-open-env.txt")"
grep -qx "GTK_THEME=" "$out/xdg-open-env.txt" ||
  fail "xdg-open did not get the launch GTK_THEME (set, empty) back: $(grep '^GTK_THEME' "$out/xdg-open-env.txt" || echo unset)"
if [[ "$session" == wayland ]]; then
  grep -qx "GDK_BACKEND=wayland" "$out/xdg-open-env.txt" ||
    fail "xdg-open did not get the launch GDK_BACKEND (wayland): $(grep '^GDK_BACKEND' "$out/xdg-open-env.txt" || echo unset)"
fi
echo "smoke: a link opens with the launch environment and folder"

if [[ "$session" == x11 ]]; then
  window="$(DISPLAY="$display" xdotool search --onlyvisible --pid "$radar_pid" 2>/dev/null | head -n 1 || true)"
  [[ -n "$window" ]] || fail "Radar has no visible window"
  sleep 5
  DISPLAY="$display" import -window "$window" "$out/window.png" || fail "could not capture Radar's window"
else
  sleep 5
  wayland_screenshot "$out/window.png" || fail "could not capture Radar's window"
fi
# A painted UI has hundreds of colours (text antialiasing, icons); a blank or
# single-colour window has a handful.
colours="$(convert "$out/window.png" -format %k info:)"
[[ "$colours" -ge 100 ]] || fail "Radar's window looks blank ($colours colours)"
echo "smoke: the window is painted ($colours colours)"
echo "smoke: PASS"
