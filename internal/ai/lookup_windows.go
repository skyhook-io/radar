//go:build windows

package ai

import (
	"os"
	"path/filepath"
)

// agentBinDirs lists the install locations probed after PATH. Fixed literals,
// so the fallback widens where Radar looks without widening what it will run.
func agentBinDirs() []string {
	var dirs []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs,
			filepath.Join(local, "Microsoft", "WinGet", "Links"),
			filepath.Join(local, "cursor-agent"),                       // Cursor's own installer
			filepath.Join(local, "Programs", "OpenAI", "Codex", "bin"), // Codex's own installer
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// executableNames is the set of filenames an agent CLI can have in a directory.
// Only .exe, because Radar spawns the CLI directly: os/exec hands the resolved
// path to CreateProcess with no batch-file handling of its own, so a .cmd or
// .bat shim is not something this process can start.
//
// The PATH branch of lookupAgent does NOT apply this filter — exec.LookPath on
// Windows walks PATHEXT and will return a `claude.cmd` from an npm install. That
// asymmetry is deliberate for now: whether CreateProcess actually rejects such a
// shim has not been confirmed on a real Windows machine, and narrowing the PATH
// branch on an unconfirmed premise would un-detect agents that may work today.
func executableNames(name string) []string { return []string{name + ".exe"} }
