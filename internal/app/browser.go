package app

import (
	"log"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser opens the given URL in the user's default browser.
func OpenBrowser(url, preferredBrowser string) {
	cmd := browserCommand(url, preferredBrowser, runtime.GOOS)
	if cmd == nil {
		log.Printf("Cannot open browser on %s, please open manually: %s", runtime.GOOS, url)
		return
	}
	if browser := strings.TrimSpace(preferredBrowser); browser != "" {
		log.Printf("Opening browser %q: %s", browser, url)
	} else {
		log.Printf("Opening browser: %s", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v", err)
		log.Printf("Please open manually: %s", url)
	}
}

func browserCommand(url, preferredBrowser, goos string) *exec.Cmd {
	if browser := strings.TrimSpace(preferredBrowser); browser != "" {
		if goos == "darwin" && shouldOpenMacApp(browser) {
			return exec.Command("open", "-a", browser, url)
		}
		return exec.Command(browser, url)
	}
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "linux":
		return exec.Command("xdg-open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return nil
	}
}

func shouldOpenMacApp(browser string) bool {
	lower := strings.ToLower(browser)
	return strings.HasSuffix(lower, ".app") || !strings.Contains(browser, "/")
}
