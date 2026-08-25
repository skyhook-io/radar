package ai

import (
	"os"
	"runtime"
	"strings"
)

// envBaseKeys are the variables ANY spawned agent CLI needs on this platform,
// independent of which CLI it is. Windows keeps the agent's own auth and config
// under USERPROFILE/APPDATA/LOCALAPPDATA, and a process launched without
// SYSTEMROOT/COMSPEC can fail before it reaches its own code — so a safeguarded
// run that dropped them wouldn't start at all.
func envBaseKeys() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"PATH", "PATHEXT", "COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE",
			"TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "NUMBER_OF_PROCESSORS",
		}
	}
	return []string{"HOME", "PATH", "TMPDIR"}
}

// filterEnv keeps only the entries of environ whose name is listed in keep, starts
// with one of prefixes, or belongs to the platform base set. Windows environment
// names are case-insensitive (SystemRoot and SYSTEMROOT are one variable), so
// matching folds case there and stays exact everywhere else.
func filterEnv(environ, keep, prefixes []string) []string {
	fold := runtime.GOOS == "windows"
	norm := func(s string) string {
		if fold {
			return strings.ToUpper(s)
		}
		return s
	}
	allow := make(map[string]bool, len(keep)+len(envBaseKeys()))
	for _, k := range envBaseKeys() {
		allow[norm(k)] = true
	}
	for _, k := range keep {
		allow[norm(k)] = true
	}
	normPrefixes := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		normPrefixes = append(normPrefixes, norm(p))
	}

	var out []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		n := norm(k)
		if allow[n] {
			out = append(out, kv)
			continue
		}
		for _, p := range normPrefixes {
			if strings.HasPrefix(n, p) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

// minimalEnv is filterEnv over the live process environment.
func minimalEnv(keep, prefixes []string) []string {
	return filterEnv(os.Environ(), keep, prefixes)
}
