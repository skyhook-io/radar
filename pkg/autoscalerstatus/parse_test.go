package autoscalerstatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func mustInt(t *testing.T, p *int, label string) int {
	t.Helper()
	if p == nil {
		t.Fatalf("%s: expected non-nil int, got nil", label)
	}
	return *p
}

func TestParseZonalSinglePool(t *testing.T) {
	st, err := Parse(loadFixture(t, "gke-zonal-single-pool.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Format != FormatStructured {
		t.Errorf("Format = %q, want %q", st.Format, FormatStructured)
	}
	if st.AutoscalerState != "Running" {
		t.Errorf("AutoscalerState = %q, want %q", st.AutoscalerState, "Running")
	}
	if st.Time == nil {
		t.Errorf("Time = nil, want non-nil parsed timestamp")
	}
	if len(st.NodeGroups) != 1 {
		t.Fatalf("len(NodeGroups) = %d, want 1", len(st.NodeGroups))
	}

	ng := st.NodeGroups[0]
	if got := mustInt(t, ng.MinSize, "MinSize"); got != 5 {
		t.Errorf("MinSize = %d, want 5", got)
	}
	if got := mustInt(t, ng.MaxSize, "MaxSize"); got != 11 {
		t.Errorf("MaxSize = %d, want 11", got)
	}
	if got := mustInt(t, ng.Target, "Target"); got != 9 {
		t.Errorf("Target = %d, want 9", got)
	}
	if got := mustInt(t, ng.Health.Ready, "Health.Ready"); got != 9 {
		t.Errorf("Health.Ready = %d, want 9", got)
	}
	if got := mustInt(t, ng.Health.Registered, "Health.Registered"); got != 9 {
		t.Errorf("Health.Registered = %d, want 9", got)
	}
	if ng.Health.Status != "Healthy" {
		t.Errorf("Health.Status = %q, want %q", ng.Health.Status, "Healthy")
	}
	if ng.ScaleUp.Status != "NoActivity" {
		t.Errorf("ScaleUp.Status = %q, want %q", ng.ScaleUp.Status, "NoActivity")
	}
	if ng.ScaleUp.Backoff != nil {
		t.Errorf("ScaleUp.Backoff = %+v, want nil for empty backoffInfo {}", ng.ScaleUp.Backoff)
	}
	if !strings.HasSuffix(ng.Basename, "-grp") {
		t.Errorf("Basename = %q, want suffix %q", ng.Basename, "-grp")
	}
	if ng.Basename != "gke-demo-cluster--default-pool-16275362-grp" {
		t.Errorf("Basename = %q, want %q", ng.Basename, "gke-demo-cluster--default-pool-16275362-grp")
	}
}

func TestParseRegionalMultiMigScaleToZero(t *testing.T) {
	st, err := Parse(loadFixture(t, "gke-regional-multi-mig-scale-to-zero.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Format != FormatStructured {
		t.Errorf("Format = %q, want %q", st.Format, FormatStructured)
	}
	if len(st.NodeGroups) != 13 {
		t.Fatalf("len(NodeGroups) = %d, want 13", len(st.NodeGroups))
	}

	zeroTargets := 0
	for _, ng := range st.NodeGroups {
		if got := mustInt(t, ng.MinSize, "MinSize"); got != 0 {
			t.Errorf("group %s: MinSize = %d, want 0", ng.Basename, got)
		}
		if got := mustInt(t, ng.MaxSize, "MaxSize"); got != 1000 {
			t.Errorf("group %s: MaxSize = %d, want 1000", ng.Basename, got)
		}
		if ng.Target != nil && *ng.Target == 0 {
			zeroTargets++
			// Scale-to-zero groups publish an explicit `lastProbeTime: null`;
			// that must survive as nil, never as a zero time.
			if ng.Health.LastProbeTime != nil {
				t.Errorf("group %s: target 0 should keep Health.LastProbeTime nil, got %v",
					ng.Basename, ng.Health.LastProbeTime)
			}
		}
	}
	if zeroTargets != 10 {
		t.Errorf("groups with Target==0 = %d, want 10", zeroTargets)
	}
}

func TestParseTruncatedNames(t *testing.T) {
	st, err := Parse(loadFixture(t, "gke-truncated-names.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(st.NodeGroups) != 2 {
		t.Fatalf("len(NodeGroups) = %d, want 2", len(st.NodeGroups))
	}

	g0 := st.NodeGroups[0]
	wantName0 := "https://www.googleapis.com/compute/v1/projects/example-project/zones/me-west1-a/instanceGroups/gke-longname-clust-longname-clust-4712e1a8-grp"
	if g0.Name != wantName0 {
		t.Errorf("group[0].Name = %q, want verbatim %q", g0.Name, wantName0)
	}
	if g0.Basename != "gke-longname-clust-longname-clust-4712e1a8-grp" {
		t.Errorf("group[0].Basename = %q, want %q", g0.Basename, "gke-longname-clust-longname-clust-4712e1a8-grp")
	}
	if !strings.Contains(g0.Basename, "longname-clust") {
		t.Errorf("group[0].Basename = %q, want it to contain %q", g0.Basename, "longname-clust")
	}
	if got := mustInt(t, g0.MinSize, "group[0].MinSize"); got != 15 {
		t.Errorf("group[0].MinSize = %d, want 15", got)
	}
	if got := mustInt(t, g0.MaxSize, "group[0].MaxSize"); got != 20 {
		t.Errorf("group[0].MaxSize = %d, want 20", got)
	}

	g1 := st.NodeGroups[1]
	if g1.Basename != "gke-longname-clust-second-pool-trun-fb123523-grp" {
		t.Errorf("group[1].Basename = %q, want %q", g1.Basename, "gke-longname-clust-second-pool-trun-fb123523-grp")
	}
	if got := mustInt(t, g1.MinSize, "group[1].MinSize"); got != 3 {
		t.Errorf("group[1].MinSize = %d, want 3", got)
	}
	if got := mustInt(t, g1.MaxSize, "group[1].MaxSize"); got != 5 {
		t.Errorf("group[1].MaxSize = %d, want 5", got)
	}
}

func TestParseLegacyText(t *testing.T) {
	st, err := Parse(loadFixture(t, "legacy-text.txt"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Format != FormatLegacyText {
		t.Errorf("Format = %q, want %q", st.Format, FormatLegacyText)
	}
	if st.Time == nil {
		t.Errorf("Time = nil, want non-nil parsed header timestamp")
	}
	if len(st.NodeGroups) != 2 {
		t.Fatalf("len(NodeGroups) = %d, want 2", len(st.NodeGroups))
	}

	g0 := st.NodeGroups[0]
	if g0.Basename != "gke-legacy-cluster-default-pool-a1b2c3d4-grp" {
		t.Errorf("group[0].Basename = %q, want %q", g0.Basename, "gke-legacy-cluster-default-pool-a1b2c3d4-grp")
	}
	if g0.Health.Status != "Healthy" {
		t.Errorf("group[0].Health.Status = %q, want %q", g0.Health.Status, "Healthy")
	}
	if got := mustInt(t, g0.Target, "group[0].Target"); got != 3 {
		t.Errorf("group[0].Target = %d, want 3", got)
	}
	if got := mustInt(t, g0.MinSize, "group[0].MinSize"); got != 1 {
		t.Errorf("group[0].MinSize = %d, want 1", got)
	}
	if got := mustInt(t, g0.MaxSize, "group[0].MaxSize"); got != 5 {
		t.Errorf("group[0].MaxSize = %d, want 5", got)
	}
	if got := mustInt(t, g0.Health.Ready, "group[0].Health.Ready"); got != 3 {
		t.Errorf("group[0].Health.Ready = %d, want 3", got)
	}
	if got := mustInt(t, g0.Health.Registered, "group[0].Health.Registered"); got != 3 {
		t.Errorf("group[0].Health.Registered = %d, want 3", got)
	}
	if g0.ScaleUp.Status != "NoActivity" {
		t.Errorf("group[0].ScaleUp.Status = %q, want %q", g0.ScaleUp.Status, "NoActivity")
	}
	if g0.ScaleDown.Status != "NoCandidates" {
		t.Errorf("group[0].ScaleDown.Status = %q, want %q", g0.ScaleDown.Status, "NoCandidates")
	}
	if g0.Health.LastProbeTime == nil {
		t.Errorf("group[0].Health.LastProbeTime = nil, want non-nil")
	}
	if g0.Health.LastTransition == nil {
		t.Errorf("group[0].Health.LastTransition = nil, want non-nil")
	}

	g1 := st.NodeGroups[1]
	if got := mustInt(t, g1.Target, "group[1].Target"); got != 2 {
		t.Errorf("group[1].Target = %d, want 2", got)
	}
	if got := mustInt(t, g1.MinSize, "group[1].MinSize"); got != 0 {
		t.Errorf("group[1].MinSize = %d, want 0", got)
	}
	if got := mustInt(t, g1.MaxSize, "group[1].MaxSize"); got != 10 {
		t.Errorf("group[1].MaxSize = %d, want 10", got)
	}
	if got := mustInt(t, g1.Health.Ready, "group[1].Health.Ready"); got != 2 {
		t.Errorf("group[1].Health.Ready = %d, want 2", got)
	}
}

func TestParseUnrecognized(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"whitespace":     "   \n  ",
		"prose":          "this is not a cluster-autoscaler status at all",
		"yaml-no-keys":   "{some: yaml, without: known keys}",
		"unrelated-yaml": "foo: bar\nbaz: 1\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Errorf("Parse(%q) err = nil, want error", raw)
			}
		})
	}
}
