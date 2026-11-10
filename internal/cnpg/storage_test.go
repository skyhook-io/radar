package cnpg

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func TestCNPGDiskFindingsThresholds(t *testing.T) {
	r := func(v float64) *float64 { return &v }
	vols := []CNPGStorageVolume{
		{Claim: "a", Role: cnpgPVCRoleData, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.79)}},
		{Claim: "b", Role: cnpgPVCRoleWAL, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.80)}},
		{Claim: "c", Role: cnpgPVCRoleTablespace, Tablespace: "archive", Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateOK, Ratio: r(0.90)}},
		{Claim: "d", Role: cnpgPVCRoleData, Usage: CNPGStorageVolumeUsage{State: cnpgUsageStateNoSeries}},
	}
	got := cnpgDiskFindings("pg-1", vols, nil)
	if len(got) != 2 || got[0].Severity != "warning" || got[1].Severity != "critical" {
		t.Fatalf("findings = %+v", got)
	}
	if got[1].Message != "The tablespace archive volume of pg-1 is 90% full" {
		t.Errorf("message = %q", got[1].Message)
	}
	unverified := cnpgDiskFindings("pg-1", vols, &prometheuspkg.SeriesIsolation{Mode: prometheuspkg.SeriesIsolationUnverified, Note: "Radar couldn't confirm these volume stats belong to this exact cluster"})
	if !strings.Contains(unverified[1].Message, "couldn't confirm these volume stats belong to this exact cluster") {
		t.Errorf("an unverified match must say so: %q", unverified[1].Message)
	}
}

func TestCNPGStorageExpansionDefaultsToStorageSize(t *testing.T) {
	c := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"storage": map[string]any{"resizeInUseVolumes": false}}}}
	got := cnpgStorageExpansionOf(c)
	if len(got.Targets) != 1 || got.Targets[0].Field != "spec.storage.size" || got.Targets[0].Declared != "" {
		t.Errorf("targets = %+v", got.Targets)
	}
	if got.ResizeInUseVolumes == nil || *got.ResizeInUseVolumes {
		t.Errorf("resizeInUseVolumes = %v, want the declared false", got.ResizeInUseVolumes)
	}
}

func TestCNPGWALCoverageReason(t *testing.T) {
	cases := []struct {
		failed, partial, total int
		want                   string
	}{
		{3, 0, 3, "3 of 3 instances could not be read"},
		{1, 2, 3, "1 of 3 instances could not be read; 2 of 3 were read only in part"},
		{0, 1, 3, "1 of 3 was read only in part"},
	}
	for _, c := range cases {
		if got := cnpgWALCoverageReason(c.failed, c.partial, c.total); got != c.want {
			t.Errorf("(%d, %d, %d) = %q, want %q", c.failed, c.partial, c.total, got, c.want)
		}
	}
}
