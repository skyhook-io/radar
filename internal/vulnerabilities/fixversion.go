package vulnerabilities

import (
	"slices"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	gem "github.com/aquasecurity/go-gem-version"
	pep440 "github.com/aquasecurity/go-pep440-version"
	apk "github.com/knqyf263/go-apk-version"
	deb "github.com/knqyf263/go-deb-version"
	rpm "github.com/knqyf263/go-rpm-version"
	mvn "github.com/masahiro331/go-mvn-version"
)

// compareFunc orders two versions of one ecosystem; ok is false when either
// doesn't parse as that ecosystem's version.
type compareFunc func(a, b string) (cmp int, ok bool)

// packageTypeEcosystem maps the scanner's package types (Trivy's result
// types and OS families) to the ecosystems below.
var packageTypeEcosystem = map[string]string{
	"debian": "deb", "ubuntu": "deb",
	"alpine": "apk", "wolfi": "apk", "chainguard": "apk", "minimos": "apk",
	"redhat": "rpm", "centos": "rpm", "rocky": "rpm", "alma": "rpm", "amazon": "rpm", "oracle": "rpm", "fedora": "rpm",
	"photon": "rpm", "suse": "rpm", "sles": "rpm", "slem": "rpm", "opensuse": "rpm", "opensuse.leap": "rpm",
	"opensuse-leap": "rpm", "opensuse.tumbleweed": "rpm", "opensuse-tumbleweed": "rpm", "cbl-mariner": "rpm",
	"azurelinux": "rpm", "mariner": "rpm",
	"pip": "pypi", "pipenv": "pypi", "poetry": "pypi", "python-pkg": "pypi", "uv": "pypi",
	"gemspec": "gem", "bundler": "gem", "cocoapods": "cocoapods",
	"jar": "maven", "pom": "maven", "gradle": "maven", "sbt": "maven",
	"npm": "npm", "yarn": "npm", "pnpm": "npm", "bun": "npm", "node-pkg": "npm",
	"gobinary": "golang", "gomod": "golang",
	"cargo": "cargo", "rust-binary": "cargo", "rustbinary": "cargo",
	"composer": "composer", "composer-vendor": "composer",
	"nuget": "nuget", "dotnet-core": "nuget", "packages-props": "nuget",
	"pub": "pub", "hex": "hex", "swift": "swift",
}

// ecosystemOf names the ecosystem a finding's package belongs to: from the
// scanner's package type when the report carries it (Trivy Operator writes
// it only when asked to), else from the type in the package URL, which it
// writes whenever it knows one. Without either the package could be the
// OS's or a dependency inside the image, so it stays unknown: ordering its
// versions by the image's OS would be a guess.
func ecosystemOf(packageType, purl string) string {
	if t := strings.ToLower(packageType); t != "" {
		if eco, ok := packageTypeEcosystem[t]; ok {
			return eco
		}
		return t
	}
	rest, ok := strings.CutPrefix(purl, "pkg:")
	if !ok {
		return ""
	}
	t, _, _ := strings.Cut(rest, "/")
	return strings.ToLower(t)
}

// comparerFor is the version ordering of an ecosystem; nil when Radar
// doesn't know how it orders versions.
func comparerFor(eco string) compareFunc {
	switch eco {
	case "deb":
		return compareDeb
	case "apk":
		return compareApk
	case "rpm":
		return compareRpm
	case "pypi":
		return comparePep440
	case "gem", "cocoapods":
		return compareGem
	case "maven":
		return compareMvn
	case "npm", "golang", "cargo", "composer", "nuget", "pub", "hex", "swift":
		return compareSemver
	}
	return nil
}

// fixedReleases splits a scanner's fixed-version list ("8.0.1, 7.5.5,
// 6.11.6": one fix per maintained branch).
func fixedReleases(fixed string) []string {
	var out []string
	for _, v := range strings.Split(fixed, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// fixTarget is the version to move a package to for one finding: the
// smallest listed fixed version above the installed one, read with the
// ecosystem's own ordering. For 6.11.4 with "8.0.1, 7.5.5, 6.11.6" that is
// 6.11.6. ok is false when the ordering is unknown, a version doesn't parse,
// or nothing listed is above the installed version: the caller then shows
// the list as the scanner wrote it and picks nothing.
func fixTarget(eco, installed, fixed string) (target string, ok bool) {
	compare := comparerFor(eco)
	if compare == nil || installed == "" {
		return "", false
	}
	return nextFix(compare, installed, fixedReleases(fixed))
}

// nextFix is the smallest of fixes above v.
func nextFix(compare compareFunc, v string, fixes []string) (string, bool) {
	var next string
	for _, f := range fixes {
		above, ok := compare(f, v)
		if !ok {
			return "", false
		}
		if above <= 0 {
			continue
		}
		if next != "" {
			lower, ok := compare(f, next)
			if !ok {
				return "", false
			}
			if lower >= 0 {
				continue
			}
		}
		next = f
	}
	return next, next != ""
}

// fixedBy reports whether moving to v fixes a finding whose fixed releases
// are fixes. At or above every listed fix it does. Otherwise v must be on
// the branch of the highest fix at or below it: with "7.5.5, 6.11.6", 6.11.7
// is fixed but 7.5.4 is not, though both are above 6.11.6. A branch is the
// shortest version prefix (major, then major.minor) that tells the listed
// fixes apart.
func fixedBy(compare compareFunc, v string, fixes []string) (fixed, ok bool) {
	var floor string
	top := true
	for _, f := range fixes {
		c, ok := compare(f, v)
		if !ok {
			return false, false
		}
		if c > 0 {
			top = false
			continue
		}
		if floor == "" {
			floor = f
		} else if higher, ok := compare(f, floor); !ok {
			return false, false
		} else if higher > 0 {
			floor = f
		}
	}
	if top {
		return true, true
	}
	if floor == "" {
		return false, true
	}
	n := branchDepth(fixes)
	return slices.Equal(versionPrefix(floor, n), versionPrefix(v, n)), true
}

func branchDepth(fixes []string) int {
	for n := 1; n < 3; n++ {
		seen := map[string]bool{}
		distinct := true
		for _, f := range fixes {
			k := strings.Join(versionPrefix(f, n), ".")
			if seen[k] {
				distinct = false
				break
			}
			seen[k] = true
		}
		if distinct {
			return n
		}
	}
	return 3
}

// versionPrefix is the first n numeric components of a version, past a
// leading "v" and a Debian-style epoch ("1:").
func versionPrefix(v string, n int) []string {
	if _, rest, ok := strings.Cut(v, ":"); ok {
		v = rest
	}
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	parts := strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	return parts[:min(n, len(parts))]
}

func compareDeb(a, b string) (int, bool) {
	va, err := deb.NewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := deb.NewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}

func compareApk(a, b string) (int, bool) {
	va, err := apk.NewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := apk.NewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}

// rpm versions always parse: the ordering is defined for any string.
func compareRpm(a, b string) (int, bool) {
	return rpm.NewVersion(a).Compare(rpm.NewVersion(b)), true
}

func comparePep440(a, b string) (int, bool) {
	va, err := pep440.Parse(a)
	if err != nil {
		return 0, false
	}
	vb, err := pep440.Parse(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}

func compareGem(a, b string) (int, bool) {
	va, err := gem.NewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := gem.NewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}

func compareMvn(a, b string) (int, bool) {
	va, err := mvn.NewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := mvn.NewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}

func compareSemver(a, b string) (int, bool) {
	va, err := semver.NewVersion(a)
	if err != nil {
		return 0, false
	}
	vb, err := semver.NewVersion(b)
	if err != nil {
		return 0, false
	}
	return va.Compare(vb), true
}
