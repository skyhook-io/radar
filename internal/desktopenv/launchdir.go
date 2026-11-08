package desktopenv

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// appImage describes how the running process relates to Radar's AppImage.
type appImage struct {
	// inImage is true when this executable runs from the AppImage's AppDir.
	inImage bool
	// launchDir is the directory the AppImage was started from, or "".
	launchDir string
}

// detectAppImage decides whether exe is running from Radar's AppImage. The
// AppImage launcher exports APPDIR (the mounted or extracted image) and OWD
// (the directory it was started from). Both are inherited by anything a
// process started from inside some AppImage launches, so they are only
// trusted when this executable actually lives inside APPDIR.
func detectAppImage(lookup func(string) (string, bool), exe string) appImage {
	appDir, _ := lookup("APPDIR")
	if appDir == "" || exe == "" || !within(appDir, exe) {
		return appImage{}
	}
	result := appImage{inImage: true}
	if owd, _ := lookup("OWD"); filepath.IsAbs(owd) {
		result.launchDir = owd
	}
	return result
}

// within reports whether path is dir or below it, after resolving symlinks,
// comparing whole path components (/tmp/image-other is not in /tmp/image).
func within(dir, path string) bool {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// current is computed once: the desktop app removes the launcher's variables
// from its own environment at startup (see PlanChildEnv), after this ran.
var current = sync.OnceValue(func() appImage {
	exe, err := os.Executable()
	if err != nil {
		return appImage{}
	}
	return detectAppImage(os.LookupEnv, exe)
})

// InAppImage reports whether Radar is running from its AppImage.
func InAppImage() bool { return current().inImage }

// LaunchDir returns the directory the AppImage was started from, or "" when
// Radar is not running from its AppImage. The AppImage's launcher changes into
// the image's library directory so WebKit can find its bundled helper
// processes, which leaves the process working directory pointing inside the
// read-only image. Relative paths the user gave, and processes Radar starts,
// use this directory instead.
func LaunchDir() string { return current().launchDir }

// ChildDir is the working directory for processes Radar starts: the launch
// folder inside the AppImage, or the home folder if the launch folder has
// since been removed or renamed (starting a process in a missing directory
// fails). Outside the AppImage it is "", so children inherit Radar's own.
func ChildDir() string {
	return childDirFor(LaunchDir(), os.UserHomeDir)
}

func childDirFor(launch string, home func() (string, error)) string {
	if launch == "" {
		return ""
	}
	if info, err := os.Stat(launch); err == nil && info.IsDir() {
		return launch
	}
	if h, err := home(); err == nil {
		return h
	}
	return ""
}

// ResolveLaunchPath makes a relative path the user supplied absolute against
// LaunchDir, so it means what it meant in the shell that started the AppImage.
// Empty, absolute and "~"-prefixed paths, and every path outside an AppImage,
// are returned unchanged.
func ResolveLaunchPath(path string) string {
	return resolveAgainst(LaunchDir(), path)
}

func resolveAgainst(dir, path string) string {
	if dir == "" || path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return path
	}
	// Not filepath.Join: cleaning "link/.." would skip the symlink the
	// kernel follows when it resolves the path from the launch folder.
	return strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator) + path
}

// launchPathEnv lists environment variables that hold paths Radar, or a
// process it starts, reads relative to the working directory. Exec auth
// plugins (client-go) and Helm plugins are started without a way to choose
// their working directory, so their path variables are resolved here too.
var launchPathEnv = []struct {
	key string
	// list is true for os.PathListSeparator-separated values.
	list bool
	// command is true for an executable name: a bare name is looked up on
	// PATH, so only values containing a separator are paths.
	command bool
}{
	{key: "KUBECONFIG", list: true},
	{key: "GOOGLE_APPLICATION_CREDENTIALS"},
	{key: "CLOUDSDK_CONFIG"},
	{key: "AZURE_CONFIG_DIR"},
	{key: "AWS_CONFIG_FILE"},
	{key: "AWS_SHARED_CREDENTIALS_FILE"},
	{key: "CODEX_HOME"},
	{key: "RADAR_AI_CLI_BIN", command: true},
	{key: "RADAR_ARGOCD_TOKEN_FILE"},
	{key: "RADAR_OPERATOR_SETTINGS_FILE"},
	{key: "HELM_CACHE_HOME"},
	{key: "HELM_CONFIG_HOME"},
	{key: "HELM_DATA_HOME"},
	{key: "HELM_PLUGINS", list: true},
	{key: "HELM_REGISTRY_CONFIG"},
	{key: "HELM_REPOSITORY_CACHE"},
	{key: "HELM_REPOSITORY_CONFIG"},
	{key: "HELM_KUBECAFILE"},
	// Helm falls back to these and uses a relative value as given.
	{key: "XDG_CACHE_HOME"},
	{key: "XDG_CONFIG_HOME"},
	{key: "XDG_DATA_HOME"},
	// Registry credentials for image inspection (go-containerregistry).
	{key: "DOCKER_CONFIG"},
	{key: "REGISTRY_AUTH_FILE"},
	{key: "SSL_CERT_FILE"},
	{key: "SSL_CERT_DIR", list: true},
	{key: "TMPDIR"},
}

// ResolveLaunchEnv rewrites relative paths in launchPathEnv variables to
// absolute ones against LaunchDir. It does nothing outside the AppImage.
func ResolveLaunchEnv() {
	for key, value := range resolvedLaunchEnv(LaunchDir(), os.LookupEnv) {
		_ = os.Setenv(key, value)
	}
}

// resolvedLaunchEnv returns the launchPathEnv variables whose value changes
// when resolved against dir.
func resolvedLaunchEnv(dir string, lookup func(string) (string, bool)) map[string]string {
	changed := map[string]string{}
	if dir == "" {
		return changed
	}
	for _, v := range launchPathEnv {
		value, ok := lookup(v.key)
		if !ok || value == "" {
			continue
		}
		var resolved string
		switch {
		case v.list:
			parts := filepath.SplitList(value)
			for i, p := range parts {
				parts[i] = resolveAgainst(dir, p)
			}
			resolved = strings.Join(parts, string(os.PathListSeparator))
		case v.command && !strings.ContainsRune(value, filepath.Separator):
			resolved = value
		default:
			resolved = resolveAgainst(dir, value)
		}
		if resolved != value {
			changed[v.key] = resolved
		}
	}
	return changed
}
