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

// releaseWithAssets lists assets the way GitHub returns them: alphabetically.
func releaseWithAssets(tag string, names ...string) *githubReleaseWithAssets {
	r := &githubReleaseWithAssets{TagName: tag}
	for _, n := range names {
		r.Assets = append(r.Assets, githubAsset{Name: n})
	}
	return r
}

func TestFindDesktopAssetPicksTheLinuxTarball(t *testing.T) {
	v1201 := []string{
		"checksums-desktop.txt", "checksums.txt",
		"radar-desktop_v1.20.1_darwin_universal.zip",
		"radar-desktop_v1.20.1_linux_amd64.deb",
		"radar-desktop_v1.20.1_linux_amd64.rpm",
		"radar-desktop_v1.20.1_linux_amd64.tar.gz",
		"radar-desktop_v1.20.1_windows_amd64.zip",
		"radar_v1.20.1_linux_amd64.tar.gz",
	}
	withAppImage := append([]string{"radar-desktop_v1.20.1_linux_amd64.AppImage"}, v1201...)

	for name, assets := range map[string][]string{"v1.20.1 as published": v1201, "with an AppImage": withAppImage} {
		t.Run(name, func(t *testing.T) {
			got := FindDesktopAsset(releaseWithAssets("v1.20.1", assets...), "linux", "amd64")
			if got == nil || got.Name != "radar-desktop_v1.20.1_linux_amd64.tar.gz" {
				t.Fatalf("FindDesktopAsset(linux) = %+v, want the desktop tarball", got)
			}
		})
	}

	if got := FindDesktopAsset(releaseWithAssets("v1.20.1", "radar-desktop_v1.20.0_linux_amd64.tar.gz", "radar-desktop_v1.20.1_linux_amd64.deb"), "linux", "amd64"); got != nil {
		t.Errorf("a release without this version's desktop tarball has nothing to install, got %q", got.Name)
	}
	if got := FindDesktopAsset(releaseWithAssets("v1.20.1", v1201...), "linux", "arm64"); got != nil {
		t.Errorf("no arm64 desktop build, got %q", got.Name)
	}
	if got := FindDesktopAsset(releaseWithAssets("v1.20.1", v1201...), "darwin", "arm64"); got == nil || got.Name != "radar-desktop_v1.20.1_darwin_universal.zip" {
		t.Errorf("macOS keeps the universal zip, got %+v", got)
	}
}
