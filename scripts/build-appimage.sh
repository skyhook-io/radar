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

# Pinned tools. Bump the version and checksums together. The AppImage
# runtime is embedded in every image; without LDAI_RUNTIME_FILE linuxdeploy's
# appimage plugin downloads whatever type2-runtime's "continuous" tag holds.
linuxdeploy_release="1-alpha-20251107-1"
runtime_release="20251108"

case "$(uname -m)" in
  x86_64)
    arch=amd64
    linuxdeploy_asset="linuxdeploy-x86_64.AppImage"
    linuxdeploy_sha256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
    runtime_asset="runtime-x86_64"
    runtime_sha256="2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d"
    ;;
  aarch64)
    arch=arm64
    linuxdeploy_asset="linuxdeploy-aarch64.AppImage"
    linuxdeploy_sha256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
    runtime_asset="runtime-aarch64"
    runtime_sha256="00cbdfcf917cc6c0ff6d3347d59e0ca1f7f45a6df1a428a0d6d8a78664d87444"
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
mkdir -p "$work"
work="$(cd "$work" && pwd)"
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
else
  LINUXDEPLOY="$(cd "$(dirname "$LINUXDEPLOY")" && pwd)/$(basename "$LINUXDEPLOY")"
fi
fetch "https://github.com/AppImage/type2-runtime/releases/download/$runtime_release/$runtime_asset" \
  "$runtime_sha256" "$tools/$runtime_asset"
export LDAI_RUNTIME_FILE="$tools/$runtime_asset"
# The GTK plugin is vendored in scripts/appimage. linuxdeploy looks for plugins
# next to itself before PATH, so a copy there (older tools caches had one)
# would silently win.
rm -f -- "${tools:?}/linuxdeploy-plugin-gtk.sh"
if [[ -e "$(dirname "$LINUXDEPLOY")/linuxdeploy-plugin-gtk.sh" ]]; then
  echo "build-appimage: remove $(dirname "$LINUXDEPLOY")/linuxdeploy-plugin-gtk.sh; the vendored plugin in scripts/appimage must be the one that runs" >&2
  exit 1
fi
export PATH="$repo_root/scripts/appimage:$PATH"

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

# Every variable the GTK plugin's environment script (gtk-env/, see below) or
# AppRun assigns, except APPDIR. AppRun records their values before it changes
# them, so Radar can give the processes it starts (shells, auth plugins,
# xdg-open) the environment the user launched it with (internal/desktopenv).
# The build fails if the plugin's script assigns anything not listed.
restore_keys="GTK_DATA_PREFIX GTK_THEME GDK_BACKEND XDG_DATA_DIRS GSETTINGS_SCHEMA_DIR GI_TYPELIB_PATH GTK_EXE_PREFIX GTK_PATH GTK_IM_MODULE_FILE GDK_PIXBUF_MODULE_FILE COLOR_SCHEME GTK_THEME_VARIANT APPIMAGE_GTK_THEME WEBKIT_INJECTED_BUNDLE_PATH GIO_MODULE_DIR"

cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
set -e
APPDIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
export APPDIR
# The AppImage runtime never changes directory, and sets OWD only when it
# mounts the image, so an OWD in the environment may be inherited and stale.
OWD="$(pwd 2>/dev/null)" || OWD="$HOME"
export OWD
RADAR_APPIMAGE_KEYS="@RESTORE_KEYS@"
export RADAR_APPIMAGE_KEYS
for key in $RADAR_APPIMAGE_KEYS; do
  if eval "[ -n \"\${$key+set}\" ]"; then
    eval "export RADAR_APPIMAGE_ORIG_$key=\"\$$key\""
  fi
done
# The GTK plugin's environment (GDK pixbuf loaders, GIO modules, GTK paths)
# for the bundled GTK.
for script in "$APPDIR"/gtk-env/*.sh; do
  if [ -e "$script" ]; then . "$script"; fi
done
# The library's injected-bundle path stays absolute (the build machine's), so
# point WebKit at the bundled copy; release builds honour this variable.
export WEBKIT_INJECTED_BUNDLE_PATH="$APPDIR/usr/lib/webkit2gtk-4.1/injected-bundle/"
# The bundled GIO's own modules (HTTPS for the webview). Without this it looks
# in the build machine's module directory, which other distros don't have.
export GIO_MODULE_DIR="$APPDIR/usr/lib/gio/modules"
# The bundled library starts its helpers from ".", the working directory.
cd "$APPDIR/usr/lib"
exec "$APPDIR/usr/bin/radar-desktop" "$@"
EOF
sed -i "s/@RESTORE_KEYS@/$restore_keys/" "$appdir/AppRun"
chmod 0755 "$appdir/AppRun"

cd "$work"
export DEPLOY_GTK_VERSION=3
export APPIMAGE_EXTRACT_AND_RUN=1 # linuxdeploy is itself an AppImage; no FUSE needed
"$LINUXDEPLOY" --appdir "$appdir" \
  --executable "$appdir/usr/bin/radar-desktop" \
  --desktop-file "$appdir/usr/share/applications/radar-desktop.desktop" \
  --icon-file "$appdir/usr/share/icons/hicolor/512x512/apps/radar-desktop.png" \
  --plugin gtk

# linuxdeploy moved our AppRun to AppRun.wrapped and put a generated launcher
# in front that sources apprun-hooks/ and then runs ours. That would set up
# GTK before AppRun records the original environment. Keep ours as the only
# launcher: it sources the plugin's script itself, from a directory linuxdeploy
# does not look at, so the --output run below generates no wrapper.
[[ -f "$appdir/AppRun.wrapped" && -d "$appdir/apprun-hooks" ]] || {
  echo "build-appimage: linuxdeploy did not install the GTK environment script as expected" >&2
  exit 1
}
mv -f "$appdir/AppRun.wrapped" "$appdir/AppRun"
rm -rf -- "${appdir:?}/gtk-env"
mv "$appdir/apprun-hooks" "$appdir/gtk-env"
python3 - "$appdir/gtk-env" "$restore_keys" <<'EOF'
import glob, os, re, sys

env_dir, keys = sys.argv[1], set(sys.argv[2].split())
assigned = set()
for script in glob.glob(os.path.join(env_dir, "*.sh")):
    with open(script) as f:
        for line in f:
            for m in re.finditer(r"(?:^|[\s;)])(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=", line):
                assigned.add(m.group(1))
missing = sorted(assigned - keys - {"APPDIR"})
if missing:
    sys.exit("build-appimage: add to restore_keys (the GTK environment script now also sets): " + " ".join(missing))
EOF

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
# WebKit's network process makes HTTPS connections through GIO's TLS module
# (glib-networking); the GTK plugin bundles GIO but none of its modules. Bundle
# the modules built for this GIO, with their libraries: TLS, the proxy
# resolvers for http_proxy/https_proxy and for GNOME's proxy settings, and dconf,
# through which GIO reads those settings (and GTK the desktop's). glib-networking
# and dconf come with libwebkit2gtk-4.1-dev and libgtk-3-dev.
gio_moduledir="$(pkg-config --variable=giomoduledir gio-2.0)"
mkdir -p "$appdir/usr/lib/gio/modules"
for module in libgiognutls.so libgioenvironmentproxy.so libgiognomeproxy.so libdconfsettings.so; do
  [[ -f "$gio_moduledir/$module" ]] || {
    echo "build-appimage: $gio_moduledir/$module not found; install glib-networking and dconf-gsettings-backend" >&2
    exit 1
  }
  install -m 0644 "$gio_moduledir/$module" "$appdir/usr/lib/gio/modules/"
done

OUTPUT="$output_name" "$LINUXDEPLOY" --appdir "$appdir" \
  --deploy-deps-only "$appdir/usr/lib/gio/modules" --output appimage
mv "$work/$output_name" "$out_dir/$output_name"
echo "build-appimage: $out_dir/$output_name"
