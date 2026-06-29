package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot access Downloads folder: %w", err)
	}

	// Collision handling: file.txt → file (1).txt → file (2).txt
	base := defaultFilename
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	path := filepath.Join(dir, base)
	for i := 1; i <= 1000; i++ {
		_, statErr := os.Stat(path)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return "", fmt.Errorf("cannot check file %q: %w", path, statErr)
		}
		path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, i, ext))
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

// domReady is called when the webview DOM is ready.
func (a *DesktopApp) domReady(ctx context.Context) {
	a.updateWindowTitle(k8s.GetContextName())
}

// beforeClose is called before the window closes. Return true to prevent closing.
func (a *DesktopApp) beforeClose(ctx context.Context) bool {
	return false // allow close
}

// shutdown is called when the application is shutting down.
func (a *DesktopApp) shutdown(ctx context.Context) {
	stopNativeMouseMonitor()
	log.Println("Desktop app shutting down...")
	app.Shutdown(a.srv)
}
