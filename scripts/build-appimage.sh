#!/usr/bin/env bash
# Packages a Linux radar-desktop binary (Wails v2, WebKitGTK 4.1) as an AppImage.
#
# Usage: scripts/build-appimage.sh <radar-desktop binary> <version> [output dir]
#   version     goes into the file name: radar-desktop_<version>_linux_<arch>.AppImage
#   output dir  default: current directory
#
# Run on the machine that built the binary (the release job runs it on
# ubuntu-22.04), with libgtk-3-dev and libwebkit2gtk-4.1-dev installed: GTK,
# WebKitGTK and their dependencies are copied from this system into the image.
# glibc is not bundled, so the image runs on distros with glibc >= the build's.
#
# WebKitGTK starts helper processes (WebKitNetworkProcess, WebKitWebProcess)
# from a directory compiled into libwebkit2gtk; release builds ignore
# WEBKIT_EXEC_PATH. Copied as-is, the image either fails on other distros with
# "Unable to spawn a new child process" or, where the build machine's path
# exists, runs that system's helpers against the bundled library. So the
# helpers are bundled next to the libraries in usr/lib, the path in the bundled
# library is rewritten to ".", NUL-padded to the same length, and AppRun starts
# the app in $APPDIR/usr/lib, where "." resolves to the bundled helpers. WebKit
# resolves "." on every helper spawn, so the app has to stay in that directory;
# it reads the user's original directory from OWD instead (internal/desktopenv).
#
# Environment:
#   LINUXDEPLOY   use this linuxdeploy instead of downloading the pinned one
#   APPIMAGE_WORK scratch directory (default: build/appimage)
set -euo pipefail

usage="usage: scripts/build-appimage.sh <radar-desktop binary> <version> [output dir]"
binary="${1:?$usage}"
version="${2:?$usage}"
out_dir="${3:-$PWD}"

# Pinned tools. Bump the version and checksum together.
linuxdeploy_release="1-alpha-20251107-1"
gtk_plugin_commit="7a3fbc31a9e5075073ff8790f26effbac5f84453"
gtk_plugin_sha256="b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171"

case "$(uname -m)" in
  x86_64)
    arch=amd64
    linuxdeploy_asset="linuxdeploy-x86_64.AppImage"
    linuxdeploy_sha256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
    ;;
  aarch64)
    arch=arm64
    linuxdeploy_asset="linuxdeploy-aarch64.AppImage"
    linuxdeploy_sha256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
    ;;
  *)
    echo "build-appimage: unsupported architecture $(uname -m)" >&2
    exit 1
    ;;
esac

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
binary="$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
work="${APPIMAGE_WORK:-$repo_root/build/appimage}"
appdir="$work/AppDir"
tools="$work/tools"
output_name="radar-desktop_${version}_linux_${arch}.AppImage"

for cmd in curl sha256sum pkg-config python3 file convert; do
  command -v "$cmd" >/dev/null || { echo "build-appimage: $cmd is required" >&2; exit 1; }
done
if ! file -L "$binary" | grep -q ELF; then
  echo "build-appimage: $binary is not a Linux binary" >&2
  exit 1
fi

# fetch <url> <sha256> <dest>
fetch() {
  if [[ -f "$3" ]] && echo "$2  $3" | sha256sum -c --status; then
    return
  fi
  echo "build-appimage: downloading $1"
  curl -fSL --retry 3 --connect-timeout 20 --max-time 600 -o "$3.tmp" "$1"
  if ! echo "$2  $3.tmp" | sha256sum -c --status; then
    echo "build-appimage: checksum mismatch for $1" >&2
    rm -f "$3.tmp"
    exit 1
  fi
  mv "$3.tmp" "$3"
  chmod +x "$3"
}

mkdir -p "$tools"
if [[ -z "${LINUXDEPLOY:-}" ]]; then
  LINUXDEPLOY="$tools/$linuxdeploy_asset"
  fetch "https://github.com/linuxdeploy/linuxdeploy/releases/download/$linuxdeploy_release/$linuxdeploy_asset" \
    "$linuxdeploy_sha256" "$LINUXDEPLOY"
fi
# linuxdeploy finds plugins next to itself or on PATH.
fetch "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/$gtk_plugin_commit/linuxdeploy-plugin-gtk.sh" \
  "$gtk_plugin_sha256" "$tools/linuxdeploy-plugin-gtk.sh"
export PATH="$tools:$PATH"

# WebKitGTK's helper directory on this system, e.g. /usr/lib/x86_64-linux-gnu/webkit2gtk-4.1.
webkit_libdir="$(pkg-config --variable=libdir webkit2gtk-4.1)"
helper_dir="$webkit_libdir/webkit2gtk-4.1"
for f in WebKitNetworkProcess WebKitWebProcess injected-bundle/libwebkit2gtkinjectedbundle.so; do
  [[ -e "$helper_dir/$f" ]] || { echo "build-appimage: $helper_dir/$f not found" >&2; exit 1; }
done

rm -rf "$appdir"
mkdir -p "$appdir/usr/bin" "$appdir/usr/lib/webkit2gtk-4.1/injected-bundle" \
  "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/512x512/apps"
install -m 0755 "$binary" "$appdir/usr/bin/radar-desktop"
install -m 0644 "$repo_root/deploy/linux/radar-desktop.desktop" "$appdir/usr/share/applications/"
# build/appicon.png is 1024x1024; linuxdeploy accepts hicolor sizes up to 512.
convert "$repo_root/build/appicon.png" -resize 512x512 "$appdir/usr/share/icons/hicolor/512x512/apps/radar-desktop.png"
# The helpers go straight into usr/lib: linuxdeploy gives everything there the
# RPATH $ORIGIN, which is only right for files next to the bundled libraries.
for f in WebKitNetworkProcess WebKitWebProcess WebKitGPUProcess; do
  if [[ -e "$helper_dir/$f" ]]; then
    install -m 0755 "$helper_dir/$f" "$appdir/usr/lib/"
  fi
done
install -m 0644 "$helper_dir/injected-bundle/libwebkit2gtkinjectedbundle.so" \
  "$appdir/usr/lib/webkit2gtk-4.1/injected-bundle/"

# AppRun: linuxdeploy keeps an existing one. Hooks are the GTK plugin's
# environment (GDK pixbuf loaders, GIO modules, GTK paths) for the bundled GTK.
cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
set -e
APPDIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
export APPDIR
OWD="${OWD:-$PWD}"
export OWD
for hook in "$APPDIR"/apprun-hooks/*.sh; do
  if [ -e "$hook" ]; then . "$hook"; fi
done
# The library's injected-bundle path stays absolute (the build machine's), so
# point WebKit at the bundled copy; release builds honour this variable.
export WEBKIT_INJECTED_BUNDLE_PATH="$APPDIR/usr/lib/webkit2gtk-4.1/injected-bundle/"
# The bundled library starts its helpers from ".", the working directory.
cd "$APPDIR/usr/lib"
exec "$APPDIR/usr/bin/radar-desktop" "$@"
EOF
chmod 0755 "$appdir/AppRun"

cd "$work"
export DEPLOY_GTK_VERSION=3
export APPIMAGE_EXTRACT_AND_RUN=1 # linuxdeploy is itself an AppImage; no FUSE needed
"$LINUXDEPLOY" --appdir "$appdir" \
  --executable "$appdir/usr/bin/radar-desktop" \
  --desktop-file "$appdir/usr/share/applications/radar-desktop.desktop" \
  --icon-file "$appdir/usr/share/icons/hicolor/512x512/apps/radar-desktop.png" \
  --plugin gtk

# Point the bundled library at the bundled helpers: the compiled-in helper
# directory becomes ".", NUL-padded to the same length, which AppRun's working
# directory ($APPDIR/usr/lib) resolves. Only the NUL-terminated directory
# string is rewritten: the injected-bundle path shares its prefix and is
# redirected by AppRun instead.
python3 - "$appdir" "$helper_dir" <<'EOF'
import glob, os, sys

appdir, helper_dir = sys.argv[1], sys.argv[2]
lib_dir = os.path.join(appdir, "usr", "lib")
old = helper_dir.encode() + b"\0"
new = b"." + b"\0" * (len(old) - 1)

patched = []
for lib in glob.glob(os.path.join(lib_dir, "libwebkit2gtk-4.1.so*")):
    if os.path.islink(lib):
        continue
    with open(lib, "rb") as f:
        data = f.read()
    if old not in data:
        continue
    with open(lib, "wb") as f:
        f.write(data.replace(old, new))
    patched.append(os.path.basename(lib))
if not patched:
    sys.exit(f"build-appimage: no bundled libwebkit2gtk-4.1 references {helper_dir}")

print(f"build-appimage: {', '.join(patched)} now starts its helpers from usr/lib")
EOF

rm -f "$work/$output_name"
OUTPUT="$output_name" "$LINUXDEPLOY" --appdir "$appdir" --output appimage
mv "$work/$output_name" "$out_dir/$output_name"
echo "build-appimage: $out_dir/$output_name"
