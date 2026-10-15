package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

func storedReleaseValue(t *testing.T, rel *release.Release, gzipped bool) []byte {
	t.Helper()
	return helmReleaseSecret(t, "apps", rel, gzipped).Data["release"]
}

func summaryTestRelease(status release.Status) *release.Release {
	return &release.Release{
		Name:      "web",
		Namespace: "apps",
		Version:   3,
		Info: &release.Info{
			Status:       status,
			Description:  "Upgrade complete",
			LastDeployed: helmtime.Unix(100, 0),
			Deleted:      helmtime.Unix(200, 0),
			Notes:        strings.Repeat("notes ", 1000),
		},
		Chart: &chart.Chart{
			Metadata: &chart.Metadata{Name: "web", Version: "1.2.3", AppVersion: "4.5.6", Description: "dropped"},
			Values:   map[string]any{"big": strings.Repeat("x", 10000)},
		},
		Config:   map[string]any{"replicas": 3},
		Manifest: summaryTestManifest,
	}
}

func TestSummarizeReleaseKeepsWhatTheListReads(t *testing.T) {
	for _, gzipped := range []bool{false, true} {
		summary, err := summarizeRelease(storedReleaseValue(t, summaryTestRelease(release.StatusDeployed), gzipped))
		if err != nil {
			t.Fatal(err)
		}
		want := &release.Release{
			Name:      "web",
			Namespace: "apps",
			Version:   3,
			Info: &release.Info{
				LastDeployed: helmtime.Unix(100, 0),
				Deleted:      helmtime.Unix(200, 0),
				Description:  "Upgrade complete",
				Status:       release.StatusDeployed,
			},
			Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "web", Version: "1.2.3", AppVersion: "4.5.6"}},
		}
		got := summary.release
		if !got.Info.LastDeployed.Equal(want.Info.LastDeployed) || !got.Info.Deleted.Equal(want.Info.Deleted) {
			t.Fatalf("times = %v / %v", got.Info.LastDeployed, got.Info.Deleted)
		}
		got.Info.LastDeployed, got.Info.Deleted = want.Info.LastDeployed, want.Info.Deleted
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("gzipped=%v\n got: %+v\nwant: %+v", gzipped, got, want)
		}
		if !summary.resourcesRecorded {
			t.Fatal("deployed revision has no recorded resources")
		}
		// Resolved, the summary's resources are what a full read finds.
		if got, want := summary.ownedResources("apps"), parseManifestResources(summaryTestManifest, "apps"); !reflect.DeepEqual(got, want) {
			t.Fatalf("resources = %+v, want %+v", got, want)
		}
	}
}

func TestSummarizeReleaseLeavesSupersededResourcesOut(t *testing.T) {
	summary, err := summarizeRelease(storedReleaseValue(t, summaryTestRelease(release.StatusSuperseded), true))
	if err != nil {
		t.Fatal(err)
	}
	if summary.resourcesRecorded || len(summary.resources) != 0 {
		t.Fatalf("superseded revision kept its resources: %+v", summary.resources)
	}
	if summary.release.Info.Status != release.StatusSuperseded || summary.release.Chart == nil {
		t.Fatalf("superseded revision lost its summary: %+v", summary.release)
	}
}

func TestSummarizeReleaseSkipsWhatAFullReadSkips(t *testing.T) {
	for _, value := range [][]byte{
		nil,
		[]byte("%%% not base64"),
		[]byte(base64.StdEncoding.EncodeToString([]byte("{not json"))),
		mistypedReleaseValue(t, summaryTestRelease(release.StatusDeployed)),
	} {
		summary, _ := summarizeRelease(value)
		if !summary.skip {
			t.Fatalf("value %.20q: summary %+v, want skipped", value, summary)
		}
		if _, err := decodeHelmReleaseData(string(value)); err == nil && len(value) > 0 {
			t.Fatalf("value %.20q: a full read decodes it", value)
		}
	}
}

func TestSummarizeReleaseStopsAtTheDecodeLimit(t *testing.T) {
	// 512MiB of zeros compresses to well under a Secret's 1MiB.
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	chunk := make([]byte, 1<<20)
	for range 512 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	value := []byte(base64.StdEncoding.EncodeToString(b.Bytes()))
	if len(value) > 1<<20 {
		t.Fatalf("fixture is %d bytes; it must fit in a Secret", len(value))
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	summary, _ := summarizeRelease(value)
	runtime.ReadMemStats(&after)
	// Reading it all would allocate well over its 512MiB.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<20 {
		t.Fatalf("allocated %d MiB; the limit must stop the read, not follow it", allocated>>20)
	}
	if !summary.tooLarge {
		t.Fatalf("summary = %+v, want too large", summary)
	}
}

func TestSummarizeReleaseMarksLargeValidReleasesTooLarge(t *testing.T) {
	for _, size := range []int{maxSummarySize + 1, maxSummarizedRelease + 1<<20} {
		rel := summaryTestRelease(release.StatusFailed)
		rel.Info.Description = strings.Repeat("x", size)
		for _, gzipped := range []bool{true, false} {
			value := storedReleaseValue(t, rel, gzipped)
			if gzipped && len(value) > 1<<20 {
				t.Fatalf("fixture is %d bytes; it must fit in a Secret", len(value))
			}
			if summary, err := summarizeRelease(value); err != nil || !summary.tooLarge {
				t.Fatalf("description of %d bytes, gzipped=%v: summary = %+v (%v), want too large", size, gzipped, summary, err)
			}
		}
		// A full read still decodes it.
		decoded, err := decodeHelmReleaseData(string(storedReleaseValue(t, rel, true)))
		if err != nil || len(decoded.Info.Description) != size {
			t.Fatalf("description of %d bytes: full read lost it (%v)", size, err)
		}
	}
}

func TestSummarizedReleaseCopiesAreIndependent(t *testing.T) {
	summary, err := summarizeRelease(storedReleaseValue(t, summaryTestRelease(release.StatusDeployed), true))
	if err != nil {
		t.Fatal(err)
	}
	a := summary.releaseWithLabels(map[string]string{"owner": "helm"})
	b := summary.releaseWithLabels(nil)
	if a == b || summary.release.Labels != nil || b.Labels != nil {
		t.Fatal("labels set on one read's release leaked into the summary or another read")
	}
}
