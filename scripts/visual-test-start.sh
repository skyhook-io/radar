#!/bin/bash
# Start a Radar instance for visual testing.
# Usage: ./scripts/visual-test-start.sh [--skip-build]
#
# Outputs a state file at .playwright-mcp/visual-test-state.env that
# the stop script and the /visual-test command can source.

set -euo pipefail

SKIP_BUILD=false
if [[ "${1:-}" == "--skip-build" ]]; then
  SKIP_BUILD=true
fi

PORT=$((9300 + RANDOM % 100))
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
SSDIR=".playwright-mcp/visual-test/$TIMESTAMP"
LOGFILE="/tmp/radar-visual-test-$PORT.log"
STATEFILE=".playwright-mcp/visual-test-state.env"

mkdir -p "$SSDIR"

# Build unless skipped
if [[ "$SKIP_BUILD" == false ]]; then
  echo "Building Radar..."
  make build
fi

# Check binary exists
if [[ ! -f ./radar ]]; then
  echo "ERROR: ./radar binary not found. Run 'make build' first." >&2
  exit 1
fi

# Launch
echo "Starting Radar on port $PORT..."
./radar -port "$PORT" -no-browser > "$LOGFILE" 2>&1 &
PID=$!

# Wait for ready
echo -n "Waiting for Radar to be ready"
for i in $(seq 1 30); do
  if curl -s "http://localhost:$PORT/api/dashboard" > /dev/null 2>&1; then
    echo " ready!"
    break
  fi
  echo -n "."
  sleep 1
  if [[ $i -eq 30 ]]; then
    echo " TIMEOUT"
    echo "Radar failed to start. Check logs: $LOGFILE" >&2
    kill "$PID" 2>/dev/null || true
    exit 1
  fi
done

# Write state file
cat > "$STATEFILE" <<EOF
RADAR_PID=$PID
RADAR_PORT=$PORT
SCREENSHOT_DIR=$SSDIR
RADAR_LOG=$LOGFILE
RADAR_URL=http://localhost:$PORT
EOF

echo ""
echo "=== Visual Test Ready ==="
echo "  URL:          http://localhost:$PORT"
echo "  PID:          $PID"
echo "  Screenshots:  $SSDIR"
echo "  Logs:         $LOGFILE"
echo "  State:        $STATEFILE"
echo ""
echo "  REMINDER: set browser viewport BEFORE the first navigate, e.g."
echo "    mcp__playwright__browser_resize({ width: 1920, height: 1080 })"
echo "  Playwright defaults to ~1280px — too narrow to catch real layout bugs."
