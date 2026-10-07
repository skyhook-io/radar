//go:build !windows

package ai

import (
	"os"
	"path/filepath"
)

// agentBinDirs lists the install locations probed after PATH. Fixed literals,
// so the fallback widens where Radar looks without widening what it will run.
// Per-user installs come first, matching the precedence a shell PATH gives them.
//
// Deliberately does NOT try to cover an npm global prefix under nvm, fnm or
// asdf: that directory embeds the active Node version and moves with `nvm use`,
// so no fixed list can reach it. Fixed npm prefixes (pnpm, bun, ~/.npm-global)
// are left out too: their scripts need a node that isn't beside them, so Radar
// would offer an agent that can't start. Asking the
// login shell is the only thing that can, which is what cmd/desktop/env.go does
// for the desktop app.
func agentBinDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			// Claude Code's `migrate-installer` layout. It is reachable only
			// through a shell alias, so no PATH lookup can ever find it.
			filepath.Join(home, ".claude", "local"),
			// OpenCode's install script. It reaches PATH only through the
			// line it appends to the shell rc file.
			filepath.Join(home, ".opencode", "bin"),
			// Volta's shims for npm globals. Unlike other Node managers' bin
			// folders this one is fixed, and its shims find node themselves.
			filepath.Join(home, ".volta", "bin"),
		)
	}
	return append(dirs,
		"/opt/homebrew/bin",              // Homebrew, Apple Silicon
		"/usr/local/bin",                 // Homebrew on Intel; most curl-to-shell installers
		"/home/linuxbrew/.linuxbrew/bin", // Homebrew on Linux
		"/usr/bin",                       // apt / dnf / apk packages
	)
}

func executableNames(name string) []string { return []string{name} }
