package vulnerabilities

import "testing"

// The fix to move to is the smallest listed fix above the installed version,
// read with each ecosystem's own ordering; anything Radar can't order shows
// the scanner's list instead of a guess.
func TestFixTarget(t *testing.T) {
	cases := []struct {
		name, eco, installed, fixed, want string
		ok                                bool
	}{
		{"npm: fix on the installed branch", "npm", "6.11.4", "8.0.1, 7.5.5, 6.11.6", "6.11.6", true},
		{"npm: no fix on the branch, the next one up", "npm", "6.11.4", "8.0.1, 7.5.5", "7.5.5", true},
		{"maven: an older branch's fix is skipped", "maven", "4.2.16.Final", "4.2.17.Final, 4.1.137.Final", "4.2.17.Final", true},
		{"go with v prefixes", "golang", "v0.31.0", "v0.38.0", "v0.38.0", true},
		{"python with a pre-release installed", "pypi", "2.0.0rc1", "2.0.0", "2.0.0", true},
		{"debian epoch and revision", "deb", "1:1.2.13.dfsg-1", "1:1.2.13.dfsg-1+deb12u1", "1:1.2.13.dfsg-1+deb12u1", true},
		{"alpine revision", "apk", "1.2.0-r1", "1.2.1-r0", "1.2.1-r0", true},
		{"rpm release", "rpm", "1.1.1k-7.el8_6", "1.1.1k-9.el8_7", "1.1.1k-9.el8_7", true},
		{"ruby", "gem", "1.13.10", "1.14.3, 1.13.11", "1.13.11", true},
		{"cocoapods orders like RubyGems", "cocoapods", "1.0.0.rc1", "1.0.0", "1.0.0", true},
		{"unknown ecosystem: no guess", "conan", "1.0", "1.1", "", false},
		{"no ecosystem: no guess", "", "1.0", "1.1", "", false},
		{"unparseable version: no guess", "npm", "6.11.4", "not-a-version", "", false},
		{"nothing listed above the installed version", "npm", "9.0.0", "8.0.1, 7.5.5", "", false},
		{"no fix listed", "npm", "6.11.4", "", "", false},
	}
	for _, c := range cases {
		got, ok := fixTarget(c.eco, c.installed, c.fixed)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: fixTarget(%q, %q, %q) = %q, %v; want %q, %v", c.name, c.eco, c.installed, c.fixed, got, ok, c.want, c.ok)
		}
	}
}

// Trivy Operator writes packageType only when asked to, and the package URL
// whenever it knows one; with neither, the package's ecosystem is unknown
// rather than the image's OS.
func TestEcosystemOf(t *testing.T) {
	cases := []struct{ packageType, purl, want string }{
		{"python-pkg", "", "pypi"},
		{"rustbinary", "", "cargo"},
		{"opensuse-leap", "", "rpm"},
		{"", "pkg:golang/golang.org/x/net@v0.31.0", "golang"},
		{"", "pkg:deb/debian/libssl3@3.0.11-1~deb12u2?arch=amd64&distro=debian-12", "deb"},
		{"", "pkg:pypi/requests@2.31.0", "pypi"},
		{"gobinary", "pkg:golang/golang.org/x/net@v0.31.0", "golang"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := ecosystemOf(c.packageType, c.purl); got != c.want {
			t.Errorf("ecosystemOf(%q, %q) = %q; want %q", c.packageType, c.purl, got, c.want)
		}
	}
}

// One version must fix every finding of a package. A version above one
// finding's fix can sit on another branch that lacks it, so the candidate
// moves up until each finding's fixed releases cover it.
func TestUpgradeForCrossesBranches(t *testing.T) {
	pkg := func(installed string, fixed ...string) *imagePackage {
		p := &imagePackage{pkg: "lib", eco: "npm", installed: installed}
		for _, f := range fixed {
			p.findings = append(p.findings, Finding{FixedVersion: f})
		}
		return p
	}
	cases := []struct {
		name string
		p    *imagePackage
		want string
	}{
		{"both fixed on the installed branch", pkg("6.11.4", "8.0.1, 7.5.5, 6.11.6", "7.5.6, 6.11.7"), "6.11.7"},
		{"a fix on the next branch needs that branch's fix of the other", pkg("6.11.4", "7.5.5, 6.11.6", "7.5.4"), "7.5.5"},
		{"a later branch's fix of the other", pkg("1.0.0", "1.0.5, 2.0.10", "2.0.2"), "2.0.10"},
		{"branches by minor when majors repeat", pkg("1.2.0", "1.2.5, 1.3.2", "1.3.0"), "1.3.2"},
		{"the other's fix on the same branch is lower", pkg("1.0.0", "1.0.5", "2.0.2, 1.0.3"), "1.0.5"},
	}
	for _, c := range cases {
		got, listed := upgradeFor(c.p)
		if got != c.want {
			t.Errorf("%s: upgradeFor = %q (listed %v); want %q", c.name, got, listed, c.want)
		}
	}
	if got, listed := upgradeFor(pkg("1.0.0", "1.0.5", "2.0.2")); got != "2.0.2" || listed != nil {
		t.Errorf("a single fix per finding: %q %v", got, listed)
	}
	unknown := &imagePackage{pkg: "lib", eco: "conan", installed: "1.0", findings: []Finding{{FixedVersion: "1.1"}, {FixedVersion: "1.2"}}}
	if got, listed := upgradeFor(unknown); got != "" || len(listed) != 2 {
		t.Errorf("unordered versions keep the scanner's lists: %q %v", got, listed)
	}
}
