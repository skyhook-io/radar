#!/usr/bin/env bash
# start-radar.sh - starts the Radar server silently in the background (no terminal window).
# Keeps --prometheus-single-cluster (resolves the "Historical cluster scope could not be
# verified" metric-reconstruction warning) and does not open the browser (-no-browser):
# the web UI + MCP endpoint stay on http://localhost:9280/mcp.
#
# Install: cp this file to ~/.radar/start-radar.sh   (chmod +x)

set -euo pipefail

RADAR_DIR="${RADAR_DIR:-$HOME/.radar}"
RADAR_BIN="${RADAR_BIN:-$RADAR_DIR/radar}"
LOG_FILE="$RADAR_DIR/radar.log"

mkdir -p "$RADAR_DIR"

# Avoid duplicate instances: match the server by its unique startup flag (present only in the
# server command line, never in the helper scripts), not by process name or binary path.
if pgrep -f 'prometheus-single-cluster' >/dev/null 2>&1; then
    exit 0
fi

# macOS does not lock a running executable, but logging to a file keeps the server
# output available for troubleshooting. nohup+& detaches it from this shell.
nohup "$RADAR_BIN" --prometheus-single-cluster -no-browser >>"$LOG_FILE" 2>&1 </dev/null &
disown
