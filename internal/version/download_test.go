package version

import (
	"strings"
	"testing"
)

// The desktop checksums for v1.18.2 as published: sha256sum on the Windows
// runner wrote the binary-mode "*" before the zip's name.
const publishedDesktopChecksums = `647bba90b20ad52e7110fe855cb00d3cc8bcf7645581a9bd7e3774c5a875c44d  radar-desktop_v1.18.2_darwin_universal.zip
b70493e93022c42fde4db0a037cd5ccee6025602972af46851438d3d718f8e13 *radar-desktop_v1.18.2_windows_amd64.zip
64e1ec60c2bba88c08252bfa00698ce96f0cab8b4e5d3912fcfb78165eb5356b  radar-desktop_v1.18.2_linux_amd64.tar.gz
`

func TestChecksumForReadsBothSha256sumForms(t *testing.T) {
	for name, want := range map[string]string{
		"radar-desktop_v1.18.2_windows_amd64.zip":    "b70493e93022c42fde4db0a037cd5ccee6025602972af46851438d3d718f8e13",
		"radar-desktop_v1.18.2_darwin_universal.zip": "647bba90b20ad52e7110fe855cb00d3cc8bcf7645581a9bd7e3774c5a875c44d",
	} {
		got, err := checksumFor(strings.NewReader(publishedDesktopChecksums), name)
		if err != nil || got != want {
			t.Errorf("checksumFor(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestChecksumForMissingAsset(t *testing.T) {
	if _, err := checksumFor(strings.NewReader(publishedDesktopChecksums), "radar-desktop_v1.18.2_linux_amd64.deb"); err == nil {
		t.Error("expected an error for an asset the file doesn't list")
	}
}
