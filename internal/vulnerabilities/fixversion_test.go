package vulnerabilities

import "testing"

// The fix to move to is the smallest listed fix above the installed version,
// read with each ecosystem's own ordering; anything Radar can't order shows
// the scanner's list instead of a guess.
func TestFixTarget(t *testing.T) {
	cases := []struct {
		name, pkgType, os, installed, fixed, want string
		ok                                        bool
	}{
		{"npm: fix on the installed branch", "npm", "", "6.11.4", "8.0.1, 7.5.5, 6.11.6", "6.11.6", true},
		{"npm: no fix on the branch, the next one up", "npm", "", "6.11.4", "8.0.1, 7.5.5", "7.5.5", true},
		{"maven: an older branch's fix is skipped", "jar", "", "4.2.16.Final", "4.2.17.Final, 4.1.137.Final", "4.2.17.Final", true},
		{"go binary with v prefixes", "gobinary", "", "v0.31.0", "v0.38.0", "v0.38.0", true},
		{"python with a pre-release installed", "python-pkg", "", "2.0.0rc1", "2.0.0", "2.0.0", true},
		{"debian epoch and revision", "debian", "", "1:1.2.13.dfsg-1", "1:1.2.13.dfsg-1+deb12u1", "1:1.2.13.dfsg-1+deb12u1", true},
		{"debian by the image's OS when the type is missing", "", "debian", "3.0.11-1~deb12u2", "3.0.15-1~deb12u1", "3.0.15-1~deb12u1", true},
		{"alpine revision", "alpine", "", "1.2.0-r1", "1.2.1-r0", "1.2.1-r0", true},
		{"rpm release", "redhat", "", "1.1.1k-7.el8_6", "1.1.1k-9.el8_7", "1.1.1k-9.el8_7", true},
		{"ruby", "gemspec", "", "1.13.10", "1.14.3, 1.13.11", "1.13.11", true},
		{"unknown ecosystem: no guess", "conan", "", "1.0", "1.1", "", false},
		{"unparseable version: no guess", "npm", "", "6.11.4", "not-a-version", "", false},
		{"nothing listed above the installed version", "npm", "", "9.0.0", "8.0.1, 7.5.5", "", false},
		{"no fix listed", "npm", "", "6.11.4", "", "", false},
	}
	for _, c := range cases {
		got, ok := fixTarget(c.pkgType, c.os, c.installed, c.fixed)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: fixTarget(%q, %q, %q, %q) = %q, %v; want %q, %v", c.name, c.pkgType, c.os, c.installed, c.fixed, got, ok, c.want, c.ok)
		}
	}
}
