package images

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestDeclaredRuntimes_ReadsOnlyCuratedVersionKeys(t *testing.T) {
	cfg := &v1.ConfigFile{Config: v1.Config{
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin",
			"PHP_VERSION=8.3.10",
			"PHP_URL=https://www.php.net/distributions/php-8.3.10.tar.xz",
			"GOSU_VERSION=1.19",
			"APP_VERSION=2.1.0",
			"DB_PASSWORD=hunter2",
			"API_TOKEN_VERSION=abc",
			"NODE_VERSION=$(curl evil)",
		},
		Labels: map[string]string{
			labelImageVersion: "6.5.4",
			labelBaseName:     "docker.io/library/php:8.3-apache",
		},
	}}
	got, base := DeclaredRuntimes(cfg)
	want := []DeclaredRuntime{
		{Runtime: "image", Version: "6.5.4", Source: "label:" + labelImageVersion},
		{Runtime: "php", Version: "8.3.10", Source: "env:PHP_VERSION"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if base != "docker.io/library/php:8.3-apache" {
		t.Errorf("base = %q", base)
	}
}

func TestDeclaredRuntimes_OneValuePerKey(t *testing.T) {
	env := make([]string, 0, 10001)
	for i := 0; i < 10000; i++ {
		env = append(env, "PHP_VERSION=8.3.10")
	}
	env = append(env, "PHP_VERSION=8.3.36")
	got, _ := DeclaredRuntimes(&v1.ConfigFile{Config: v1.Config{Env: env}})
	if len(got) != 1 || got[0].Version != "8.3.36" {
		t.Fatalf("a repeated key must yield its last value once: %d entries, %+v", len(got), got[:min(len(got), 2)])
	}
}

func TestDeclaredRuntimes_RejectsMalformedBaseImage(t *testing.T) {
	_, base := DeclaredRuntimes(&v1.ConfigFile{Config: v1.Config{Labels: map[string]string{labelBaseName: "not a ref!"}}})
	if base != "" {
		t.Errorf("base = %q", base)
	}
	if got, _ := DeclaredRuntimes(nil); got != nil {
		t.Errorf("nil config = %+v", got)
	}
}

func TestClassifyFix(t *testing.T) {
	php := []string{"8.2.35", "8.3.36", "8.4.27", "8.5.12"}
	cases := []struct {
		version string
		fixedIn []string
		want    string
		against string
	}{
		{"8.3.10", php, FixBelow, "8.3.36"},
		{"8.3.9", php, FixBelow, "8.3.36"},
		{"8.3.36", php, FixAtOrAbove, "8.3.36"},
		{"8.3.40", php, FixAtOrAbove, "8.3.36"},
		{"8.2.20", php, FixBelow, "8.2.35"},
		{"8.1.29", php, FixBranchUnknown, ""},
		{"7.4.33", php, FixBranchUnknown, ""},
		{"8.30.1", php, FixBranchUnknown, ""},
		{"17.5", []string{"17.6", "16.10"}, FixBelow, "17.6"},
		{"17.11-1.pgdg12+2", []string{"17.6"}, FixUnparsed, ""},
		{"jdk-21.0.4+7", []string{"21.0.5"}, FixBelow, "21.0.5"},
		{"latest", php, FixUnparsed, ""},
		{"8.3.10", []string{"8.3.36", "8"}, FixBelow, "8.3.36"},
		{"8.3.36RC1", php, FixBelow, "8.3.36"},
		{"8.3.36-dev", php, FixBelow, "8.3.36"},
		{"8.3.36~rc1", php, FixBelow, "8.3.36"},
		{"8.3.37RC1", php, FixAtOrAbove, "8.3.36"},
		{"8.3.36-1~deb12u1", php, FixUnparsed, ""},
		{"21.0.5+11", []string{"21.0.5"}, FixAtOrAbove, "21.0.5"},
		{"jdk-21.0.5-ea+7", []string{"21.0.5"}, FixBelow, "21.0.5"},
		{"1.80.0-nightly", []string{"1.80.0"}, FixBelow, "1.80.0"},
		{"8.3.36zz", php, FixUnparsed, ""},
		{"17.6-1.pgdg12+2", []string{"17.6"}, FixUnparsed, ""},
		{"8.2.20-1+deb12u1", []string{"8.2.20-1+deb12u2"}, FixUnparsed, ""},
		{"8.2.20", []string{"8.2.20-1+deb12u2"}, FixUnparsed, ""},
		{"8.3.36.0RC1", php, FixUnparsed, ""},
		{"8.3", php, FixUnparsed, "8.3.36"},
		{"1.20", []string{"1.20.0"}, FixUnparsed, "1.20.0"},
		{"8.3.40zz", php, FixUnparsed, ""},
		{"9.1.0", []string{"8"}, FixBranchUnknown, ""},
		{"8.3.38", []string{"8.3.40", "8.3.36"}, FixAtOrAbove, "8.3.36"},
		{"8.3.38", []string{"8.3.36", "8.3.40"}, FixAtOrAbove, "8.3.36"},
	}
	for _, tc := range cases {
		got, against := ClassifyFix(tc.version, tc.fixedIn)
		if got != tc.want || against != tc.against {
			t.Errorf("ClassifyFix(%q, %v) = %s against %q, want %s against %q", tc.version, tc.fixedIn, got, against, tc.want, tc.against)
		}
	}
}
