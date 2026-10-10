#!/usr/bin/env python3
# radar-tray.py - Radar status indicator in the macOS menu bar (MVP, untested on a real Mac yet).
# Requires Python 3.9+, rumps and PyObjC (pip install rumps).
#
# States (dot): green = server up AND cluster reachable (GET /api/namespaces 200),
#               red   = server up but cluster NOT reachable (e.g. VPN/SSO tunnel down),
#               gray  = server down / not running (port 9280 does not answer).
# Menu-bar title shows the connected cluster. Menu: Open Radar | Start Radar |
# Change cluster (submenu) | Check for updates... | Quit.
#
# Install: cp to ~/.radar/radar-tray.py and launch it (directly or via the LaunchAgent plist).

import json
import os
import socket
import subprocess
import time
import urllib.request

import rumps

RADAR_DIR = os.path.expanduser("~/.radar")


def get_port():
    """Radar's configured API/MCP port, read from ~/.radar/mcp-port (default 9280)."""
    try:
        p = os.path.join(RADAR_DIR, "mcp-port")
        if os.path.exists(p):
            v = open(p).read().strip()
            if v.isdigit():
                return int(v)
    except Exception:
        pass
    return 9280


PORT = get_port()
BASE = "http://localhost:{port}".format(port=PORT)


def _refresh_port():
    """Re-read the configured port so we pick up ~/.radar/mcp-port once Radar has bound it
    (Radar writes that file after binding and removes it on shutdown, so it may be absent
    at login)."""
    global PORT, BASE
    PORT = get_port()
    BASE = "http://localhost:{port}".format(port=PORT)


ICON_DIR = os.path.join(RADAR_DIR, "icons")
START_SCRIPT = os.path.join(RADAR_DIR, "start-radar.sh")
UPDATE_SCRIPT = os.path.join(RADAR_DIR, "radar-update.sh")
RESULT_FILE = os.path.join(RADAR_DIR, "last-update-result.txt")
LOCK_FILE = os.path.join(RADAR_DIR, "tray.lock")

UA = {"User-Agent": "radar-tray"}


def radar_up():
    _refresh_port()
    try:
        s = socket.socket()
        s.settimeout(1.5)
        s.connect(("127.0.0.1", PORT))
        s.close()
        return True
    except Exception:
        return False


def _get(path):
    try:
        with urllib.request.urlopen(urllib.request.Request(BASE + path, headers=UA), timeout=6) as r:
            return r.status, r.read().decode()
    except Exception:
        return None, None


def cluster_ok():
    status, _ = _get("/api/namespaces")
    return status == 200


def contexts():
    status, body = _get("/api/contexts")
    if status == 200:
        try:
            data = json.loads(body)
            return data if isinstance(data, list) else []
        except Exception:
            return []
    return []


def friendly(name):
    if name and "cluster/" in name:
        return name[name.rfind("cluster/") + len("cluster/"):]
    return name


def current_cluster():
    for c in contexts():
        if c.get("isCurrent"):
            return friendly(c.get("name"))
    try:
        with open(os.path.join(RADAR_DIR, "settings.json")) as f:
            s = json.load(f)
        n = (s.get("lastDesktopContext") or {}).get("name")
        if n:
            return friendly(n)
    except Exception:
        pass
    return None


def current_raw():
    """Raw (full) name of the current context - for exact comparisons in switching."""
    for c in contexts():
        if c.get("isCurrent"):
            return c.get("name")
    return None


def make_icon_png(name, hexcolor):
    """Draw a filled circle into a 16x16 PNG via AppKit."""
    import AppKit

    os.makedirs(ICON_DIR, exist_ok=True)
    path = os.path.join(ICON_DIR, name)
    img = AppKit.NSImage.alloc().initWithSize_((16, 16))
    img.lockFocus()
    color = AppKit.NSColor.colorWithCalibratedRed_green_blue_alpha_(
        int(hexcolor[0:2], 16) / 255.0,
        int(hexcolor[2:4], 16) / 255.0,
        int(hexcolor[4:6], 16) / 255.0,
        1.0,
    )
    color.setFill()
    AppKit.NSBezierPath.bezierPathWithOvalInRect_(AppKit.NSMakeRect(1, 1, 14, 14)).fill()
    img.unlockFocus()
    rep = AppKit.NSBitmapImageRep.imageRepWithData_(img.TIFFRepresentation())
    rep.representationUsingType_properties_(AppKit.NSPNGFileType, None).writeToFile_atomically_(path, True)
    return path


class RadarApp(rumps.App):
    def __init__(self):
        super(RadarApp, self).__init__("Radar")
        self.icon_green = make_icon_png("dot-green.png", "32CD32")
        self.icon_red = make_icon_png("dot-red.png", "B22222")
        self.icon_gray = make_icon_png("dot-gray.png", "808080")
        self._state = None
        self._update = None
        self._tick_n = 0
        self._update_refreshed_ok = False
        self._build_menu()

    def _build_menu(self):
        self.menu = [
            "Open Radar (web UI)",
            "Start Radar",
            None,
            "Change cluster",
            None,
            "Check for updates...",
            None,
            "Quit",
        ]
        self._build_cluster_menu()

    # --- menu handlers ---
    @rumps.clicked("Open Radar (web UI)")
    def _open(self, _):
        subprocess.Popen(["open", BASE])

    @rumps.clicked("Start Radar")
    def _start(self, _):
        # Avoid racing with the async update (Radar is stopped while the binaries are replaced).
        if self._update is not None and self._update.poll() is None:
            rumps.notification("Radar", "Update in progress: start disabled until it finishes.", "")
            return
        if not radar_up():
            subprocess.Popen(["bash", START_SCRIPT])

    @rumps.clicked("Check for updates...")
    def _check(self, _):
        if self._update is not None and self._update.poll() is None:
            rumps.notification("Radar", "Check/update already in progress...", "")
            return
        try:
            os.remove(RESULT_FILE)
        except OSError:
            pass
        self._update = subprocess.Popen(["bash", UPDATE_SCRIPT, "--silent"])
        rumps.notification("Radar", "Checking for updates...", "")

    @rumps.clicked("Quit")
    def _quit(self, _):
        rumps.quit_application()

    # --- cluster switching (verifies the REAL outcome, surfaces rejections) ---
    def _switch(self, sender):
        ctx = getattr(sender, "_ctx", None)
        if not ctx:
            return
        before = current_raw()
        post_err = ""
        try:
            import urllib.parse

            _refresh_port()
            url = BASE + "/api/contexts/" + urllib.parse.quote(ctx, safe="")
            req = urllib.request.Request(url, data=b"", method="POST")
            try:
                urllib.request.urlopen(req, timeout=40).read()
            except Exception as e:
                # a 500 is expected on unreachable clusters, but the context still changes
                post_err = " ({})".format(e)
            time.sleep(1.2)
            after_raw = current_raw()
            if after_raw != ctx:
                # the context did not become the requested one -> the switch was rejected/failed
                still = friendly(before) if before else (friendly(ctx) or "")
                rumps.notification("Radar", "Cluster switch FAILED (still on {}){}".format(still, post_err), "")
            else:
                target = friendly(after_raw) or friendly(ctx)
                ok = cluster_ok()
                esito = "connected" if ok else "unreachable"
                rumps.notification("Radar", "Cluster: {} - {}".format(target, esito), "")
        except Exception as e:
            rumps.notification("Radar", "Cluster switch error: {}".format(e), "")
        self._tick_n = 0
        self._refresh_status()
        self._build_cluster_menu()

    # --- periodic refresh ---
    @rumps.timer(5)
    def _tick(self, _):
        self._tick_n += 1
        self._refresh_status()
        if self._tick_n % 3 == 0:
            self._build_cluster_menu()

    def _refresh_status(self):
        cl = current_cluster()
        if not radar_up():
            icon, tip = self.icon_gray, "Radar: DOWN"
        elif cluster_ok():
            icon, tip = self.icon_green, "Radar: ACTIVE" + (" - " + cl if cl else "")
        else:
            icon, tip = self.icon_red, "Radar: cluster unreachable" + (" (" + cl + ")" if cl else "")
        if self._state != tip:
            self.icon = icon
            self.title = cl or ""
            self._state = tip
        self._check_update_done()

    def _check_update_done(self):
        if self._update is not None and self._update.poll() is not None:
            msg = "No result available."
            try:
                if os.path.exists(RESULT_FILE):
                    msg = open(RESULT_FILE).read().strip()
            except Exception:
                pass
            rumps.notification("Radar update", msg[:140], "")
            self._update = None

    def _build_cluster_menu(self):
        sub = rumps.MenuItem("Change cluster", children=[])
        cols = contexts()
        if not cols:
            sub.children = [rumps.MenuItem("(no contexts)", callback=None)]
        else:
            items = []
            for c in cols:
                mi = rumps.MenuItem(friendly(c.get("name")), callback=self._switch)
                mi.state = 1 if c.get("isCurrent") else 0
                mi._ctx = c.get("name")
                items.append(mi)
            sub.children = items
        try:
            self.menu["Change cluster"] = sub
        except Exception:
            pass


def main():
    # Single instance via a file lock.
    import fcntl

    os.makedirs(RADAR_DIR, exist_ok=True)
    lock = open(LOCK_FILE, "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        return
    # The menu bar is meaningless without the server: auto-start it on first launch if it is down.
    if not radar_up():
        subprocess.Popen(["bash", START_SCRIPT])
    RadarApp().run()
    lock.close()


if __name__ == "__main__":
    main()
