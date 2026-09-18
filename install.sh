#!/bin/sh
# Radar installer
# Usage: curl -fsSL https://get.radarhq.io | sh
#
# Always use the explicit https:// scheme — without it curl defaults to
# port 80 and accepts a 308 redirect to https, which lets a network
# attacker substitute the script before the redirect.
#
# Keep POSIX-clean: no [[ ]], no $((  )), no arrays, no <<<. The script is
# piped to `sh` everywhere we publish it, so bash-isms will silently break
# the install on systems whose /bin/sh is not bash (Alpine, Debian dash,
# BusyBox).

set -e

REPO="skyhook-io/radar"
BINARY_NAME="kubectl-radar"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Relative paths would resolve inside the temp dir this script extracts into
# and then deletes, so reject them before doing any work.
case "$INSTALL_DIR" in
  /*) ;;
  *)
    echo "INSTALL_DIR must be an absolute path (got: ${INSTALL_DIR})" >&2
    exit 1
    ;;
esac

if [ -e "$INSTALL_DIR" ] && [ ! -d "$INSTALL_DIR" ]; then
  echo "INSTALL_DIR is not a directory: ${INSTALL_DIR}" >&2
  exit 1
fi

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)       echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  darwin|linux) ;;
  mingw*|msys*|cygwin*) OS="windows" ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Resolve the ordinary GitHub web redirect instead of using the REST API.
# The API's unauthenticated per-IP quota can be exhausted by unrelated users
# behind the same NAT, while this redirect is the same path browsers use.
echo "Fetching latest release..."
if ! LATEST_RELEASE_URL=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
  "https://github.com/${REPO}/releases/latest"); then
  echo "Failed to fetch latest release" >&2
  exit 1
fi

TAG=${LATEST_RELEASE_URL##*/}
if [ "$LATEST_RELEASE_URL" != "https://github.com/${REPO}/releases/tag/${TAG}" ]; then
  echo "Failed to resolve latest release: unexpected redirect URL" >&2
  exit 1
fi

if ! printf '%s\n' "$TAG" | grep -Eq \
  '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
  echo "Failed to resolve latest release: unexpected tag" >&2
  exit 1
fi
VERSION=${TAG#v}

echo "Installing Radar v${VERSION}..."

# Download
FILENAME="radar_v${VERSION}_${OS}_${ARCH}"
if [ "$OS" = "windows" ]; then
  FILENAME="${FILENAME}.zip"
else
  FILENAME="${FILENAME}.tar.gz"
fi

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${FILENAME}"
TMP_DIR=$(mktemp -d)

echo "Downloading ${DOWNLOAD_URL}..."
curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${FILENAME}"

# Extract
cd "$TMP_DIR"
if [ "$OS" = "windows" ]; then
  unzip -q "$FILENAME"
else
  tar -xzf "$FILENAME"
fi

# Install
mkdir -p "$INSTALL_DIR" 2>/dev/null || true

if [ -w "$INSTALL_DIR" ]; then
  SUDO=""
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  SUDO="sudo"
  $SUDO mkdir -p "$INSTALL_DIR"
fi

$SUDO mv "$BINARY_NAME" "$INSTALL_DIR/"

# Symlink so the binary can also be invoked as `radar`. Best-effort —
# the primary install already succeeded, and `radar` is a common-enough
# name that we refuse to clobber an unrelated regular file.
if [ "$OS" != "windows" ]; then
  if [ -e "$INSTALL_DIR/radar" ] && [ ! -L "$INSTALL_DIR/radar" ]; then
    echo "Note: ${INSTALL_DIR}/radar already exists and is not a symlink — skipping shorthand. Use 'kubectl-radar' or 'kubectl radar'." >&2
  else
    $SUDO ln -sf "$BINARY_NAME" "$INSTALL_DIR/radar" || \
      echo "Warning: could not create 'radar' symlink — use 'kubectl-radar' or 'kubectl radar'." >&2
  fi
fi

# Cleanup
rm -rf "$TMP_DIR"

echo ""
echo "Radar v${VERSION} installed successfully to ${INSTALL_DIR}!"
echo ""

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "Note: ${INSTALL_DIR} is not in your PATH — add it to run the commands below." >&2; echo "" ;;
esac

echo "Usage:"
echo "  radar                  # standalone"
echo "  kubectl radar          # as kubectl plugin"
echo ""
echo "Run 'radar --help' for more options."
