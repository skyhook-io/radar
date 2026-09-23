package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/timeline"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DesktopApp manages the desktop application lifecycle.
type DesktopApp struct {
	ctx              context.Context
	srv              *server.Server
	timelineStoreCfg timeline.StoreConfig

	// setWindowTitle is the side-effecty title setter, injectable for tests.
	// Defaults to wailsRuntime.WindowSetTitle bound to a.ctx.
	setWindowTitle func(title string)
}

func NewDesktopApp(srv *server.Server, timelineStoreCfg timeline.StoreConfig) *DesktopApp {
	a := &DesktopApp{
		srv:              srv,
		timelineStoreCfg: timelineStoreCfg,
	}
	a.setWindowTitle = func(title string) {
		if a.ctx == nil {
			return
		}
		wailsRuntime.WindowSetTitle(a.ctx, title)
	}
	return a
}

// formatWindowTitle returns the Wails window title for a given kubeconfig
// context name. Empty context (e.g. before the cluster is initialized) yields
// the bare product name. Otherwise the context is run through clusterShortName
// so the OS title matches the label the in-page cluster selector shows for the
// same cluster (e.g. "packagear-prod-eks", not the full EKS ARN).
func formatWindowTitle(contextName string) string {
	if contextName == "" {
		return "Radar"
	}
	return "Radar — " + clusterShortName(contextName)
}

func (a *DesktopApp) updateWindowTitle(contextName string) {
	a.setWindowTitle(formatWindowTitle(contextName))
}

// startup is called when the Wails app starts.
func (a *DesktopApp) startup(ctx context.Context) {
	a.ctx = ctx
	startNativeMouseMonitor(ctx)
	a.srv.SetSaveFileFunc(a.saveFile)
	a.srv.SetSaveFileStreamFunc(a.saveFileStream)

	// The OS titlebar must track the active kubeconfig context for the same
	// reason the in-page selector does: a fleet UI showing the wrong cluster
	// name invites destructive actions on the wrong cluster.
	k8s.OnContextSwitch(func(newContext string) {
		a.updateWindowTitle(newContext)
	})
}

// saveFile writes a file to the user's Downloads folder.
// We write directly to ~/Downloads instead of showing a native save dialog
// because Wails' SaveFileDialog is immediately dismissed by the webview on macOS.
func (a *DesktopApp) saveFile(defaultFilename string, data []byte) (string, error) {
	return a.saveFileStream(defaultFilename, bytes.NewReader(data))
}

// saveFileStream writes a streamed file to the user's Downloads folder. The
// bytes land in a partial file first, so an interrupted transfer leaves nothing
// that looks like a finished download and nothing that a second transfer of the
// same name can collide with.
func (a *DesktopApp) saveFileStream(defaultFilename string, r io.Reader) (string, error) {
	dir, err := downloadsDir()
	if err != nil {
		return "", err
	}
	partial, err := os.CreateTemp(dir, sanitizeDownloadName(defaultFilename)+".*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(partial.Name()) // no-op once the rename below has moved it

	if _, copyErr := io.Copy(partial, r); copyErr != nil {
		// A close failure on a write handle can be the one that lost the data,
		// so it is reported alongside the copy error rather than dropped.
		return "", errors.Join(copyErr, partial.Close())
	}
	if err := partial.Close(); err != nil {
		return "", err
	}

	claimed, err := claimDownloadFile(defaultFilename)
	if err != nil {
		return "", err
	}
	if err := claimed.Close(); err != nil {
		os.Remove(claimed.Name())
		return "", err
	}
	if err := os.Rename(partial.Name(), claimed.Name()); err != nil {
		os.Remove(claimed.Name())
		return "", err
	}
	return claimed.Name(), nil
}

// claimDownloadFile creates and returns an empty file in ~/Downloads named
// after defaultFilename, working around collisions the way a browser does:
// file.txt → file (1).txt. The name is claimed with O_EXCL rather than tested
// with Stat first, so two downloads of the same name cannot pick it together
// and a symlink planted at the path cannot be written through.
func claimDownloadFile(defaultFilename string) (*os.File, error) {
	dir, err := downloadsDir()
	if err != nil {
		return nil, err
	}

	base := sanitizeDownloadName(defaultFilename)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 0; i <= 1000; i++ {
		candidate := filepath.Join(dir, base)
		if i > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("cannot create file %q: %w", candidate, err)
		}
	}
	return nil, fmt.Errorf("could not find a free name for %q in %s", base, dir)
}

func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot access Downloads folder: %w", err)
	}
	return dir, nil
}

// sanitizeDownloadName reduces a name from inside a container to one this host
// will accept, and keeps it from reaching outside the Downloads folder. Linux
// file names are freer than Windows ones — the timestamped exports people pull
// out of pods routinely carry colons — so the Windows rules are applied only
// where they hold, leaving names untouched elsewhere.
func sanitizeDownloadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\x00", ""))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "download"
	}
	if runtime.GOOS == "windows" {
		return windowsSafeName(name)
	}
	return name
}

// windowsReservedNames cannot be used as a file name on Windows whatever the
// extension: CON.txt is still the console.
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

func windowsSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == '<', r == '>', r == ':', r == '"', r == '/', r == '\\', r == '|', r == '?', r == '*':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	// Windows silently drops a trailing dot or space, which would leave the
	// name pointing at a file nobody asked for.
	safe := strings.TrimRight(b.String(), " .")
	if safe == "" {
		return "download"
	}
	// Windows reserves the name before the FIRST dot, so com1.x.y is still the
	// serial port. Trimming only the last extension would let it through.
	if windowsReservedNames[strings.ToLower(strings.SplitN(safe, ".", 2)[0])] {
		safe = "_" + safe
	}
	return safe
}

// domReady is called when the webview DOM is ready.
func (a *DesktopApp) domReady(ctx context.Context) {
	a.updateWindowTitle(k8s.GetContextName())
}

// beforeClose is called before the app quits. Return true to prevent quitting.
//
// On macOS this no longer runs when the window is closed — HideWindowOnClose
// hides the app instead, and shutdown only happens on an explicit quit.
func (a *DesktopApp) beforeClose(ctx context.Context) bool {
	return false // allow quit
}

// shutdown is called when the application is shutting down.
func (a *DesktopApp) shutdown(ctx context.Context) {
	stopNativeMouseMonitor()
	log.Println("Desktop app shutting down...")
	app.Shutdown(a.srv)
	// Last, so a teardown that hangs or is killed still reads as an unclean
	// exit — that is precisely the failure worth knowing about.
	markSessionEnd()
}
