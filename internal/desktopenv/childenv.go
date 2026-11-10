package desktopenv

import (
	"strings"
	"sync"
)

// The AppImage's AppRun records the environment it found before it set up
// the bundled GTK: RADAR_APPIMAGE_KEYS names every variable it may change and
// RADAR_APPIMAGE_ORIG_<NAME> holds the value of each one that was set.
const (
	appImageKeysEnv    = "RADAR_APPIMAGE_KEYS"
	appImageOrigPrefix = "RADAR_APPIMAGE_ORIG_"
)

// Set by the AppImage runtime or AppRun for Radar itself. The values a child
// would have inherited are gone (the runtime overwrites them), so children get
// none, like any process started outside an AppImage.
var appImageLauncherKeys = []string{"APPIMAGE", "APPDIR", "ARGV0", "OWD"}

// EnvChange is one variable whose value for child processes differs from the
// value the bundled GTK and WebKit need.
type EnvChange struct {
	Key string
	// Child is the value processes Radar starts get; ChildSet false means unset.
	Child    string
	ChildSet bool
	// Image is the value GTK and WebKit keep; ImageSet false means unset.
	Image    string
	ImageSet bool
}

// PlanChildEnv returns the changes that give processes Radar starts the
// environment the user launched the AppImage with. lookup reads the current
// (image) environment. It returns nothing outside Radar's AppImage.
func PlanChildEnv(lookup func(string) (string, bool)) []EnvChange {
	if !InAppImage() {
		return nil
	}
	return planChildEnv(lookup)
}

func planChildEnv(lookup func(string) (string, bool)) []EnvChange {
	keysValue, ok := lookup(appImageKeysEnv)
	if !ok {
		return nil
	}
	var changes []EnvChange
	add := func(key, child string, childSet bool) {
		image, imageSet := lookup(key)
		if image == child && imageSet == childSet {
			return
		}
		changes = append(changes, EnvChange{Key: key, Child: child, ChildSet: childSet, Image: image, ImageSet: imageSet})
	}
	seen := map[string]bool{}
	for _, key := range strings.Fields(keysValue) {
		if seen[key] {
			continue
		}
		seen[key] = true
		orig, origSet := lookup(appImageOrigPrefix + key)
		add(key, orig, origSet)
	}
	for _, key := range appImageLauncherKeys {
		if !seen[key] {
			add(key, "", false)
		}
	}
	add(appImageKeysEnv, "", false)
	for _, key := range strings.Fields(keysValue) {
		add(appImageOrigPrefix+key, "", false)
	}
	return changes
}

var (
	webviewEnvMu sync.Mutex
	webviewEnv   map[string]EnvChange
)

// RecordWebviewEnv keeps the values GTK and WebKit run with after the desktop
// app gave its own environment back to child processes, so diagnostics report
// what the webview actually sees.
func RecordWebviewEnv(changes []EnvChange) {
	webviewEnvMu.Lock()
	defer webviewEnvMu.Unlock()
	webviewEnv = make(map[string]EnvChange, len(changes))
	for _, c := range changes {
		webviewEnv[c.Key] = c
	}
}

// webviewLookup is os.LookupEnv, except for variables RecordWebviewEnv saw.
func webviewLookup(key string, lookup func(string) (string, bool)) (string, bool) {
	webviewEnvMu.Lock()
	c, ok := webviewEnv[key]
	webviewEnvMu.Unlock()
	if ok {
		return c.Image, c.ImageSet
	}
	return lookup(key)
}
