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
INSTALL_DIR="/usr/local/bin"

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

# Get latest release version
echo "Fetching latest release..."
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest version"
  exit 1
fi

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
if [ -w "$INSTALL_DIR" ]; then
  mv "$BINARY_NAME" "$INSTALL_DIR/"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "$BINARY_NAME" "$INSTALL_DIR/"
fi

# Cleanup
rm -rf "$TMP_DIR"

echo ""
echo "Radar v${VERSION} installed successfully!"
echo ""
echo "Usage:"
echo "  kubectl radar          # as kubectl plugin"
echo "  kubectl-radar          # standalone"
echo ""
echo "Run 'kubectl radar --help' for more options."
