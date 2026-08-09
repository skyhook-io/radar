#!/usr/bin/env bash
#
# Smoke-test split public/internal OIDC issuer support.
#
# Why this exists:
#   Issue #981 is not just a metadata parsing problem. The failure mode shows
#   up across the real OIDC browser redirect + callback + token exchange +
#   JWKS verification flow when the browser-facing issuer URL is different
#   from the URL Radar can reach from inside the cluster. Unit tests cover the
#   resolver rules, but they do not prove the full OAuth code flow still works
#   against a real provider.
#
# What this tests:
#   - Starts Dex locally with a canonical public issuer URL.
#   - Exposes that public issuer through a tiny reverse proxy.
#   - The proxy forwards browser endpoints but deliberately blocks public
#     /token, /keys, and /userinfo requests.
#   - Starts Radar with:
#       --auth-oidc-issuer=<public issuer>
#       --auth-oidc-internal-issuer=<direct Dex URL>
#   - Completes a real Dex password login with curl.
#   - Asserts that:
#       * the browser authorization path used the public issuer,
#       * Radar created a valid OIDC session for admin@example.com,
#       * Radar never called the public token/JWKS/userinfo endpoints.
#
# Why it is worth keeping in the repo:
#   This is a cheap, reproducible manual smoke test for a deployment shape that
#   is otherwise awkward to recreate. It does not need an AWS cluster, a kind
#   cluster, ingress, DNS, or hostAliases; Docker plus the built Radar binary is
#   enough. Keeping it next to the other auth smoke scripts makes it easy to
#   rerun before changing OIDC internals and gives future #981-style regressions
#   a concrete reproduction.
#
# When to run it:
#   - Before merging changes to internal/auth/oidc.go.
#   - When changing OIDC CLI flags, Helm values/templates, or auth docs.
#   - After upgrading go-oidc, oauth2, Dex test image versions, or auth cookie
#     behavior.
#   - When debugging a customer/provider setup where public issuer URLs and
#     in-cluster IdP URLs differ.
#   It is intentionally not part of default CI: it depends on Docker and scrapes
#   Dex's login form, so it is best treated as an explicit smoke test.
#
# What this does not test:
#   - Kubernetes impersonation/RBAC after login.
#   - TLS certificate behavior or real ingress/DNS.
#   - Every OIDC provider's quirks. Dex is only the local provider used to
#     exercise the standard code flow.
#
# Prerequisites:
#   - radar binary built (make build)
#   - docker
#   - curl, jq, python3
#
# Usage:
#   ./scripts/test-oidc-split.sh [radar-binary-path]
#
# Useful knobs:
#   DEX_IMAGE=ghcr.io/dexidp/dex:<tag>  Override the Dex image.
#   KEEP_LOGS=1                         Keep temp logs after a successful run.

set -euo pipefail

RADAR="${1:-./radar}"
DEX_IMAGE="${DEX_IMAGE:-ghcr.io/dexidp/dex:v2.41.1}"

PASS=0
FAIL=0
RADAR_PID=""
PROXY_PID=""
DEX_CONTAINER=""
TMP_DIR=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

require_cmd() {
    local cmd="$1"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo -e "${RED}Missing required command: $cmd${NC}"
        exit 1
    fi
}

random_port() {
    python3 - <<'PY'
import random
import socket

for _ in range(100):
    port = random.randint(10000, 30000)
    with socket.socket() as sock:
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            continue
        print(port)
        break
else:
    raise SystemExit("could not find a free localhost port")
PY
}

cleanup() {
    local status=$?
    if [[ -n "$RADAR_PID" ]] && kill -0 "$RADAR_PID" 2>/dev/null; then
        kill "$RADAR_PID" 2>/dev/null || true
        wait "$RADAR_PID" 2>/dev/null || true
    fi
    if [[ -n "$PROXY_PID" ]] && kill -0 "$PROXY_PID" 2>/dev/null; then
        kill "$PROXY_PID" 2>/dev/null || true
        wait "$PROXY_PID" 2>/dev/null || true
    fi
    if [[ -n "$DEX_CONTAINER" ]]; then
        docker rm -f "$DEX_CONTAINER" >/dev/null 2>&1 || true
    fi
    if [[ -n "$TMP_DIR" && "$status" -eq 0 && -z "${KEEP_LOGS:-}" ]]; then
        rm -rf "$TMP_DIR"
    elif [[ -n "$TMP_DIR" ]]; then
        echo "Logs kept in $TMP_DIR"
    fi
}
trap cleanup EXIT

check() {
    local desc="$1"
    local expected="$2"
    local actual="$3"
    if [[ "$actual" == "$expected" ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (expected $expected, got $actual)"
        FAIL=$((FAIL + 1))
    fi
}

check_success() {
    local desc="$1"
    shift
    if "$@"; then
        echo -e "  ${GREEN}PASS${NC} $desc"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc"
        FAIL=$((FAIL + 1))
    fi
}

fail_now() {
    local message="$1"
    echo -e "${RED}$message${NC}"
    echo "Logs are in $TMP_DIR"
    exit 1
}

wait_for_url() {
    local url="$1"
    local desc="$2"
    local max_attempts="${3:-45}"

    for _ in $(seq 1 "$max_attempts"); do
        if curl -sf "$url" >/dev/null 2>&1; then
            echo "$desc ready."
            return 0
        fi
        sleep 1
    done

    return 1
}

absolute_form_action() {
    local html_file="$1"
    local base_url="$2"

    python3 - "$html_file" "$base_url" <<'PY'
import sys
from html.parser import HTMLParser
from urllib.parse import urljoin


class LoginFormParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.forms = []
        self.current = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "form":
            self.current = {"action": attrs.get("action", ""), "inputs": set()}
        elif tag == "input" and self.current is not None:
            name = attrs.get("name")
            if name:
                self.current["inputs"].add(name)

    def handle_endtag(self, tag):
        if tag == "form" and self.current is not None:
            self.forms.append(self.current)
            self.current = None


with open(sys.argv[1], "r", encoding="utf-8") as f:
    parser = LoginFormParser()
    parser.feed(f.read())

for form in parser.forms:
    if {"login", "password"}.issubset(form["inputs"]):
        print(urljoin(sys.argv[2], form["action"]))
        sys.exit(0)

sys.exit("could not find Dex login form")
PY
}

form_payload() {
    local html_file="$1"

    python3 - "$html_file" <<'PY'
import sys
from html.parser import HTMLParser
from urllib.parse import urlencode


class LoginFormParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.forms = []
        self.current = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "form":
            self.current = []
        elif tag == "input" and self.current is not None:
            name = attrs.get("name")
            if name:
                self.current.append((name, attrs.get("value", "")))

    def handle_endtag(self, tag):
        if tag == "form" and self.current is not None:
            self.forms.append(self.current)
            self.current = None


with open(sys.argv[1], "r", encoding="utf-8") as f:
    parser = LoginFormParser()
    parser.feed(f.read())

for inputs in parser.forms:
    names = {name for name, _ in inputs}
    if {"login", "password"}.issubset(names):
        data = [(name, value) for name, value in inputs if name not in {"login", "password"}]
        data.extend([
            ("login", "admin@example.com"),
            ("password", "password"),
        ])
        print(urlencode(data))
        sys.exit(0)

sys.exit("could not find Dex login form")
PY
}

session_cookie_from_jar() {
    local jar="$1"
    awk '($0 ~ /^#HttpOnly_/ || $0 !~ /^#/) && NF >= 7 && $6 == "radar_session" { print $7 }' "$jar" | tail -n1
}

require_cmd docker
require_cmd curl
require_cmd jq
require_cmd python3

if [[ ! -x "$RADAR" ]]; then
    echo -e "${RED}Radar binary not found or not executable: $RADAR${NC}"
    echo "Run make build first, or pass the binary path as the first argument."
    exit 1
fi

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/radar-oidc-split.XXXXXX")
RADAR_PORT=$(random_port)
DEX_PUBLIC_PORT=$(random_port)
DEX_INTERNAL_PORT=$(random_port)
RADAR_BASE="http://localhost:${RADAR_PORT}"
PUBLIC_ISSUER="http://localhost:${DEX_PUBLIC_PORT}/dex"
INTERNAL_ISSUER="http://127.0.0.1:${DEX_INTERNAL_PORT}/dex"
DEX_CONTAINER="radar-oidc-split-$RANDOM-$RANDOM"

DEX_CONFIG="$TMP_DIR/dex-config.yaml"
DEX_LOG="$TMP_DIR/dex.log"
RADAR_LOG="$TMP_DIR/radar.log"
PROXY_LOG="$TMP_DIR/public-proxy.log"
COOKIE_JAR="$TMP_DIR/cookies.txt"
LOGIN_HTML="$TMP_DIR/login.html"
POST_HTML="$TMP_DIR/post-login.html"

cat > "$DEX_CONFIG" <<EOF
issuer: "$PUBLIC_ISSUER"
storage:
  type: memory
web:
  http: 0.0.0.0:5556
oauth2:
  skipApprovalScreen: true
staticClients:
  - id: radar
    secret: radar-secret
    name: Radar
    redirectURIs:
      - "$RADAR_BASE/auth/callback"
enablePasswordDB: true
staticPasswords:
  - email: admin@example.com
    hash: '\$2a\$10\$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W'
    username: admin
    userID: "1"
EOF

echo -e "${YELLOW}Starting Dex on internal port $DEX_INTERNAL_PORT${NC}"
docker rm -f "$DEX_CONTAINER" >/dev/null 2>&1 || true
docker run --rm --name "$DEX_CONTAINER" \
    -p "127.0.0.1:${DEX_INTERNAL_PORT}:5556" \
    -v "$DEX_CONFIG:/etc/dex/config.yaml:ro" \
    "$DEX_IMAGE" dex serve /etc/dex/config.yaml >"$DEX_LOG" 2>&1 &

if ! wait_for_url "$INTERNAL_ISSUER/.well-known/openid-configuration" "Dex"; then
    cat "$DEX_LOG" >&2 || true
    fail_now "Dex failed to start"
fi

echo -e "${YELLOW}Starting public issuer proxy on port $DEX_PUBLIC_PORT${NC}"
python3 - "$DEX_INTERNAL_PORT" "$DEX_PUBLIC_PORT" "$PROXY_LOG" <<'PY' &
import http.client
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

internal_port = int(sys.argv[1])
public_port = int(sys.argv[2])
log_path = sys.argv[3]
blocked_paths = {"/dex/token", "/dex/keys", "/dex/userinfo"}
hop_by_hop = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}


def log(line):
    with open(log_path, "a", encoding="utf-8") as f:
        f.write(f"{time.time():.3f} {line}\n")


class Proxy(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _fmt, *_args):
        return

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def do_HEAD(self):
        self.forward()

    def forward(self):
        path = urlsplit(self.path).path
        if path in blocked_paths:
            log(f"BLOCKED {self.command} {self.path}")
            body = b"blocked\n"
            self.send_response(502)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
            return

        log(f"PUBLIC {self.command} {self.path}")
        length = int(self.headers.get("Content-Length", "0") or "0")
        body = self.rfile.read(length) if length else None
        headers = {
            key: value
            for key, value in self.headers.items()
            if key.lower() not in hop_by_hop
        }
        headers["Host"] = f"localhost:{public_port}"

        conn = http.client.HTTPConnection("127.0.0.1", internal_port, timeout=30)
        try:
            conn.request(self.command, self.path, body=body, headers=headers)
            resp = conn.getresponse()
            data = resp.read()
        finally:
            conn.close()

        self.send_response(resp.status, resp.reason)
        for key, value in resp.getheaders():
            lower = key.lower()
            if lower in hop_by_hop or lower == "content-length":
                continue
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)


server = ThreadingHTTPServer(("127.0.0.1", public_port), Proxy)
log("READY")
server.serve_forever()
PY
PROXY_PID=$!

if ! wait_for_url "$PUBLIC_ISSUER/.well-known/openid-configuration" "Public issuer proxy"; then
    cat "$PROXY_LOG" >&2 || true
    fail_now "Public issuer proxy failed to start"
fi

echo -e "${YELLOW}Starting Radar on port $RADAR_PORT with split OIDC issuer URLs${NC}"
"$RADAR" \
    --port "$RADAR_PORT" \
    --no-browser \
    --auth-mode=oidc \
    --auth-secret=test-oidc-split-secret \
    --auth-oidc-issuer="$PUBLIC_ISSUER" \
    --auth-oidc-internal-issuer="$INTERNAL_ISSUER" \
    --auth-oidc-client-id=radar \
    --auth-oidc-client-secret=radar-secret \
    --auth-oidc-redirect-url="$RADAR_BASE/auth/callback" \
    --auth-oidc-scopes=openid,profile,email \
    >"$RADAR_LOG" 2>&1 &
RADAR_PID=$!

if ! wait_for_url "$RADAR_BASE/api/health" "Radar"; then
    cat "$RADAR_LOG" >&2 || true
    fail_now "Radar failed to start"
fi

echo ""
echo -e "${YELLOW}Test 1: unauthenticated auth/me is soft-auth${NC}"
auth_enabled=$(curl -sS "$RADAR_BASE/api/auth/me" | jq -r '.authEnabled')
check "authEnabled is true" "true" "$auth_enabled"

echo ""
echo -e "${YELLOW}Test 2: complete Dex login through public issuer${NC}"
login_url=$(curl -sS -c "$COOKIE_JAR" -b "$COOKIE_JAR" -L \
    -o "$LOGIN_HTML" -w '%{url_effective}' \
    "$RADAR_BASE/auth/login")

login_action=$(absolute_form_action "$LOGIN_HTML" "$login_url")
payload=$(form_payload "$LOGIN_HTML")

post_url=$(curl -sS -c "$COOKIE_JAR" -b "$COOKIE_JAR" -L \
    -H "Content-Type: application/x-www-form-urlencoded" \
    --data "$payload" \
    -o "$POST_HTML" -w '%{url_effective}' \
    "$login_action")

check_success "browser auth path used public issuer" grep -Eq 'PUBLIC (GET|POST) /dex/auth' "$PROXY_LOG"
check "post-login redirects to Radar" "$RADAR_BASE/" "$post_url"

session_cookie=$(session_cookie_from_jar "$COOKIE_JAR")
if [[ -z "$session_cookie" ]]; then
    cat "$POST_HTML" >&2 || true
    fail_now "Radar session cookie was not set"
fi

me=$(curl -sS -H "Cookie: radar_session=${session_cookie}" "$RADAR_BASE/api/auth/me")
username=$(echo "$me" | jq -r '.username // empty')
auth_mode=$(echo "$me" | jq -r '.authMode // empty')
check "logged-in username" "admin@example.com" "$username"
check "auth mode" "oidc" "$auth_mode"

echo ""
echo -e "${YELLOW}Test 3: server-side endpoints did not use public issuer${NC}"
if grep -Eq 'BLOCKED .* /dex/(token|keys|userinfo)' "$PROXY_LOG"; then
    cat "$PROXY_LOG" >&2
    echo -e "  ${RED}FAIL${NC} public token/JWKS/userinfo endpoint was used"
    FAIL=$((FAIL + 1))
else
    echo -e "  ${GREEN}PASS${NC} public token/JWKS/userinfo endpoints were not used"
    PASS=$((PASS + 1))
fi

check_success "Radar authenticated the OIDC user" grep -q 'User admin@example.com authenticated' "$RADAR_LOG"

echo ""
if [[ -n "${KEEP_LOGS:-}" ]]; then
    echo "Logs kept in $TMP_DIR"
else
    echo "Temp logs: $TMP_DIR (removed on success; set KEEP_LOGS=1 to keep)"
fi
echo -e "Passed: ${GREEN}$PASS${NC}, Failed: ${RED}$FAIL${NC}"

if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
