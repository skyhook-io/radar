package main

import (
	"encoding/json"

	versionpkg "github.com/skyhook-io/radar/internal/version"
)

func (a *DesktopApp) checkForUpdates() {
	if a.ctx == nil || a.ctx.Err() != nil {
		return
	}
	if !a.updateCheckMu.TryLock() {
		return
	}
	a.execJS(`window.dispatchEvent(new Event('radar:check-for-updates'))`)
	go func() {
		defer a.updateCheckMu.Unlock()
		result := a.checkForUpdate(a.ctx)
		if a.ctx.Err() != nil {
			return
		}
		// The UI replaces its version-check data with this result.
		encoded, _ := json.Marshal(versionpkg.WithProductHuntLaunch(result))
		a.execJS("window.dispatchEvent(new CustomEvent('radar:update-check-result', {detail:" + string(encoded) + "}))")
	}()
}
