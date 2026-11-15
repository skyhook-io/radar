package vulnerabilities

import (
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

// comparerFor picks the version ordering for a finding the way the scanner
// decided it was vulnerable: by its package type (Trivy's result type), or
// for OS packages without one, by the image's OS family. nil means Radar
// doesn't know how this ecosystem orders versions.
func comparerFor(packageType, osFamily string) compareFunc {
	t := strings.ToLower(packageType)
	if t == "" {
		t = strings.ToLower(osFamily)
	}
	switch t {
	case "debian", "ubuntu":
		return compareDeb
	case "alpine", "wolfi", "chainguard", "minimos":
		return compareApk
	case "redhat", "centos", "rocky", "alma", "amazon", "oracle", "fedora", "photon",
		"suse", "sles", "slem", "opensuse", "opensuse.leap", "opensuse.tumbleweed",
		"cbl-mariner", "azurelinux", "mariner":
		return compareRpm
	case "pip", "pipenv", "poetry", "python-pkg", "uv":
		return comparePep440
	case "gemspec", "bundler":
		return compareGem
	case "jar", "pom", "gradle", "sbt":
		return compareMvn
	case "npm", "yarn", "pnpm", "bun", "node-pkg", "gobinary", "gomod", "cargo", "rust-binary",
		"composer", "composer-vendor", "nuget", "dotnet-core", "packages-props", "pub", "hex", "swift", "cocoapods":
		return compareSemver
	}
	return nil
}

// fixTarget is the version to move a package to: the smallest listed fixed
// version above the installed one, read with the package's own version
// ordering. A scanner lists one fix per maintained branch ("8.0.1, 7.5.5,
// 6.11.6"); for 6.11.4 the answer is 6.11.6. ok is false when the ordering
// is unknown, a version doesn't parse, or nothing listed is above the
// installed version: the caller then shows the list as the scanner wrote it
// and picks nothing.
func fixTarget(packageType, osFamily, installed, fixed string) (target string, ok bool) {
	compare := comparerFor(packageType, osFamily)
	if compare == nil || installed == "" || fixed == "" {
		return "", false
	}
	for _, candidate := range strings.Split(fixed, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		above, parsed := compare(candidate, installed)
		if !parsed {
			return "", false
		}
		if above <= 0 {
			continue
		}
		if target == "" {
			target = candidate
			continue
		}
		lower, parsed := compare(candidate, target)
		if !parsed {
			return "", false
		}
		if lower < 0 {
			target = candidate
		}
	}
	return target, target != ""
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
