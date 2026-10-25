package main

import (
	"encoding/json"
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
		encoded, _ := json.Marshal(result)
		a.execJS("window.dispatchEvent(new CustomEvent('radar:update-check-result', {detail:" + string(encoded) + "}))")
	}()
}
