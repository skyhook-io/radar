package images

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// DeclaredRuntime is a runtime version an image declares in its config. It is
// what the image says about itself, not proof of the binary inside: a child
// image can replace the binary and keep the inherited ENV.
type DeclaredRuntime struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	// Source names where the version came from: "env:PHP_VERSION" or
	// "label:org.opencontainers.image.version".
	Source string `json:"source"`
	// Fix classifies Version against the caller's fixed versions; empty when
	// none were given. See ClassifyFix.
	Fix string `json:"fix,omitempty"`
	// FixedIn is the fixed version Fix compared against.
	FixedIn string `json:"fixedIn,omitempty"`
}

// runtimeEnv maps the version ENV that Docker official images set (and every
// image built FROM them inherits) to the runtime it names. Each key was
// checked against the upstream Dockerfile; helper versions those images also
// set (GOSU_VERSION, YARN_VERSION, …) are deliberately absent. No other env
// key is ever read: image env can carry baked-in credentials.
var runtimeEnv = map[string]string{
	"PHP_VERSION":     "php",
	"NODE_VERSION":    "node",
	"PYTHON_VERSION":  "python",
	"GOLANG_VERSION":  "go",
	"RUBY_VERSION":    "ruby",
	"JAVA_VERSION":    "java",
	"HTTPD_VERSION":   "httpd",
	"NGINX_VERSION":   "nginx",
	"TOMCAT_VERSION":  "tomcat",
	"PG_VERSION":      "postgres",
	"REDIS_VERSION":   "redis",
	"MONGO_VERSION":   "mongodb",
	"MARIADB_VERSION": "mariadb",
	"MYSQL_VERSION":   "mysql",
	"RUST_VERSION":    "rust",
}

const (
	labelImageVersion = "org.opencontainers.image.version"
	labelBaseName     = "org.opencontainers.image.base.name"
)

var versionValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_~:-]{0,63}$`)

// DeclaredRuntimes reads the runtime versions an image config declares, and
// the base image it names, if any.
func DeclaredRuntimes(cfg *v1.ConfigFile) (runtimes []DeclaredRuntime, baseImage string) {
	if cfg == nil {
		return nil, ""
	}
	// One value per key, the last one, as a container sees it: a config can
	// repeat a key any number of times, and the output must stay bounded.
	values := map[string]string{}
	for _, kv := range cfg.Config.Env {
		key, value, ok := strings.Cut(kv, "=")
		if _, known := runtimeEnv[key]; ok && known {
			values[key] = value
		}
	}
	for key, value := range values {
		if versionValue.MatchString(value) {
			runtimes = append(runtimes, DeclaredRuntime{Runtime: runtimeEnv[key], Version: value, Source: "env:" + key})
		}
	}
	if v := cfg.Config.Labels[labelImageVersion]; versionValue.MatchString(v) {
		runtimes = append(runtimes, DeclaredRuntime{Runtime: "image", Version: v, Source: "label:" + labelImageVersion})
	}
	if base := cfg.Config.Labels[labelBaseName]; base != "" && len(base) <= 256 {
		if _, err := name.ParseReference(base); err == nil {
			baseImage = base
		}
	}
	sort.Slice(runtimes, func(i, j int) bool {
		if runtimes[i].Runtime != runtimes[j].Runtime {
			return runtimes[i].Runtime < runtimes[j].Runtime
		}
		return runtimes[i].Source < runtimes[j].Source
	})
	return runtimes, baseImage
}

// Fix classifications.
const (
	FixBelow         = "declared_below_fix"
	FixAtOrAbove     = "declared_at_or_above_fix"
	FixBranchUnknown = "branch_not_listed"
	FixUnparsed      = "unparsed"
)

var (
	leadingVersion = regexp.MustCompile(`(?i)^(?:v|jdk-)?(\d+(?:\.\d+)*)(.*)$`)
	// prerelease marks a version that comes before its numeric release
	// (8.3.36RC1, 8.3.36-dev, 8.3.36~rc1, 21.0.5-ea+7, 22.23.4-rc.1), so it
	// lacks that release's fix.
	prerelease = regexp.MustCompile(`(?i)^[-~._]?(rc|alpha|beta|dev|pre|ea|nightly|snapshot|canary|internal|a|b)(\.?\d+)?(\+[0-9A-Za-z.-]+)?$`)
	// Distro revisions require package-specific comparison and backport data.
	finalSuffix = regexp.MustCompile(`^(\+[0-9A-Za-z.-]+)?$`)
)

// releaseLineLength sets how many leading components of a fixed version name
// its branch, for runtimes whose release lines aren't "all but the last
// component". A Node.js line is a major with semver minors, so 22.23.4 fixes
// 22.x; Rust ships one stable train with no maintained older lines, so 1.82.0
// fixes every earlier version.
var releaseLineLength = map[string]int{"node": 1, "rust": 0}

func ValidateRuntimeQuery(runtime string, fixedIn []string) error {
	if runtime == "image" && len(fixedIn) > 0 {
		return fmt.Errorf("fixed_in requires a runtime such as php or node; image labels are not runtime fix evidence")
	}
	valid := runtime == "" || runtime == "image"
	names := map[string]bool{"image": true}
	for _, name := range runtimeEnv {
		names[name] = true
		valid = valid || runtime == name
	}
	if !valid {
		allowed := make([]string, 0, len(names))
		for name := range names {
			allowed = append(allowed, name)
		}
		sort.Strings(allowed)
		return fmt.Errorf("unknown runtime %q; use one of: %s", runtime, strings.Join(allowed, ", "))
	}
	for _, raw := range fixedIn {
		version, suffix := parseVersionRest(raw)
		if version == nil || len(version) > 3 || !finalSuffix.MatchString(suffix) {
			return fmt.Errorf("fixed_in %q is not a supported upstream release; use numeric versions such as 8.3.36 or 17.6, without distro revisions", raw)
		}
	}
	return nil
}

// ClassifyFix compares a declared version of runtime against fixed releases.
// A fixed version's branch is everything but its last component (8.3.36 →
// 8.3, 17.6 → 17; a one-part fix is its own branch) unless releaseLineLength
// says otherwise for the runtime; a declared version is compared with the
// earliest fix on the longest branch it falls in. Only upstream versions with
// up to three numeric components, known prereleases and build metadata are
// supported; distro revisions stay unparsed.
// It returns the classification and the fixed version used.
func ClassifyFix(runtime, version string, fixedIn []string) (string, string) {
	got, rest := parseVersionRest(version)
	if got == nil || len(got) > 3 || (!finalSuffix.MatchString(rest) && !prerelease.MatchString(rest)) {
		return FixUnparsed, ""
	}
	var best []int
	bestRaw := ""
	bestBranch := -1
	for _, raw := range fixedIn {
		fixed, suffix := parseVersionRest(raw)
		if fixed == nil || len(fixed) > 3 || !finalSuffix.MatchString(suffix) {
			return FixUnparsed, ""
		}
		// A one-part fix ("17") covers its own major.
		line := max(len(fixed)-1, 1)
		if n, ok := releaseLineLength[runtime]; ok {
			line = n
		}
		branch := fixed[:line]
		if len(got) < len(branch) || !hasPrefix(got, branch) {
			continue
		}
		// The longest branch wins; on one branch, the earliest fix does.
		if len(branch) > bestBranch || (len(branch) == bestBranch && compareVersions(fixed, best) < 0) {
			best, bestRaw, bestBranch = fixed, raw, len(branch)
		}
	}
	if best == nil {
		return FixBranchUnknown, ""
	}
	if len(got) < len(best) {
		return FixUnparsed, bestRaw
	}
	switch c := compareVersions(got, best); {
	case c < 0, c == 0 && prerelease.MatchString(rest):
		return FixBelow, bestRaw
	case c == 0 && !finalSuffix.MatchString(rest):
		// The fixed version with a suffix this can't read: it may be a
		// pre-release build without the fix.
		return FixUnparsed, bestRaw
	case prerelease.MatchString(rest) && !hasPrefix(got, best[:max(len(best)-1, 1)]):
		// Past the fix's own patch line (a later Node minor, a later Rust
		// release), a prerelease may have been cut before the fix landed.
		return FixUnparsed, bestRaw
	}
	return FixAtOrAbove, bestRaw
}

// parseVersionRest returns the numeric version and what follows it.
func parseVersionRest(v string) ([]int, string) {
	m := leadingVersion.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil, ""
	}
	parts := strings.Split(m[1], ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, ""
		}
		out = append(out, n)
	}
	return out, m[2]
}

func hasPrefix(v, prefix []int) bool {
	for i := range prefix {
		if v[i] != prefix[i] {
			return false
		}
	}
	return true
}

func compareVersions(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
