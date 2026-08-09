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

func TestParseEKSManagedNodeGroups(t *testing.T) {
	raw, err := os.ReadFile("testdata/eks-managed-node-groups.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	status, err := Parse(string(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status.Format != FormatStructured || status.AutoscalerState != "Running" {
		t.Fatalf("status = %q %q", status.Format, status.AutoscalerState)
	}
	if len(status.NodeGroups) != 3 {
		t.Fatalf("node groups = %d, want 3", len(status.NodeGroups))
	}
	byName := map[string]NodeGroup{}
	for _, group := range status.NodeGroups {
		byName[group.Basename] = group
	}
	system, found := byName["eks-system-pool-28cf09b0-ae43-f32a-94c0-27fea5fc8bdc"]
	if !found || system.MinSize == nil || *system.MinSize != 2 || system.MaxSize == nil || *system.MaxSize != 4 {
		t.Fatalf("system pool group = %+v", system)
	}
	if system.Health.Ready == nil || *system.Health.Ready != 2 {
		t.Fatalf("system pool ready = %+v", system.Health)
	}
	// Zero-node EKS groups omit nodeCounts entirely (GKE publishes explicit
	// nulls instead) — both must land as nil, never zero.
	loadtest := byName["eks-loadtest-od-26cf0a34-4af9-ec18-066e-a8b0142e7d03"]
	if loadtest.Target == nil || *loadtest.Target != 0 || loadtest.Health.Ready != nil {
		t.Fatalf("scale-to-zero group = %+v", loadtest)
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

func TestParseAKSStructured(t *testing.T) {
	st, err := Parse(loadFixture(t, "aks-structured.yaml"))
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
	if len(st.NodeGroups) != 3 {
		t.Fatalf("len(NodeGroups) = %d, want 3", len(st.NodeGroups))
	}

	// AKS reports each node group by its bare VMSS name (ScaleSet.Id() ==
	// ScaleSet.Name). There is no "-grp" suffix and no "/"-delimited path, so
	// Basename must equal the published name verbatim — never truncated, never
	// stripped. Node names are the VMSS name plus a base36 instance suffix
	// (aks-nodepool1-33037761-vmss000000), so HasPrefix(node, Basename) — the
	// rule the logical-group join relies on — must hold.
	byName := map[string]NodeGroup{}
	for _, ng := range st.NodeGroups {
		if ng.Basename != ng.Name {
			t.Errorf("group %q: Basename = %q, want it to equal Name verbatim (no path, no suffix to strip)", ng.Name, ng.Basename)
		}
		if strings.HasSuffix(ng.Basename, "-grp") {
			t.Errorf("group %q: Basename unexpectedly ends in -grp (that is a GKE MIG convention, not AKS)", ng.Name)
		}
		if trimmed := strings.TrimSuffix(ng.Basename, "-grp"); trimmed != ng.Basename {
			t.Errorf("group %q: TrimSuffix(-grp) mutated the AKS basename to %q; the join key must be the VMSS name unchanged", ng.Name, trimmed)
		}
		syntheticNode := ng.Basename + "000000"
		if !strings.HasPrefix(syntheticNode, ng.Basename) {
			t.Errorf("group %q: node %q does not start with Basename %q — the group join would orphan this child", ng.Name, syntheticNode, ng.Basename)
		}
		byName[ng.Name] = ng
	}

	sys, ok := byName["aks-nodepool1-33037761-vmss"]
	if !ok {
		t.Fatalf("missing system node group aks-nodepool1-33037761-vmss; got %v", byName)
	}
	if got := mustInt(t, sys.MinSize, "system.MinSize"); got != 1 {
		t.Errorf("system MinSize = %d, want 1", got)
	}
	if got := mustInt(t, sys.MaxSize, "system.MaxSize"); got != 3 {
		t.Errorf("system MaxSize = %d, want 3", got)
	}
	if got := mustInt(t, sys.Target, "system.Target"); got != 2 {
		t.Errorf("system Target = %d, want 2", got)
	}
	if got := mustInt(t, sys.Health.Ready, "system.Health.Ready"); got != 2 {
		t.Errorf("system Health.Ready = %d, want 2", got)
	}
	if sys.Health.Status != "Healthy" {
		t.Errorf("system Health.Status = %q, want %q", sys.Health.Status, "Healthy")
	}
	if sys.ScaleUp.Backoff != nil {
		t.Errorf("system ScaleUp.Backoff = %+v, want nil for empty backoffInfo {}", sys.ScaleUp.Backoff)
	}

	// Scale-to-zero user pool: target 0 and minSize 0 must survive as real zero
	// values, never confused with "not published".
	zero, ok := byName["aks-spotgpu-41529630-vmss"]
	if !ok {
		t.Fatalf("missing scale-to-zero node group aks-spotgpu-41529630-vmss")
	}
	if got := mustInt(t, zero.MinSize, "spotgpu.MinSize"); got != 0 {
		t.Errorf("spotgpu MinSize = %d, want 0", got)
	}
	if got := mustInt(t, zero.MaxSize, "spotgpu.MaxSize"); got != 4 {
		t.Errorf("spotgpu MaxSize = %d, want 4", got)
	}
	if got := mustInt(t, zero.Target, "spotgpu.Target"); got != 0 {
		t.Errorf("spotgpu Target = %d, want 0", got)
	}
	if got := mustInt(t, zero.Health.Ready, "spotgpu.Health.Ready"); got != 0 {
		t.Errorf("spotgpu Health.Ready = %d, want 0", got)
	}
}

func TestParseAKSLegacyText(t *testing.T) {
	st, err := Parse(loadFixture(t, "aks-legacy-text.txt"))
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
	if g0.Name != "aks-nodepool1-33037761-vmss" {
		t.Errorf("group[0].Name = %q, want %q", g0.Name, "aks-nodepool1-33037761-vmss")
	}
	if g0.Basename != "aks-nodepool1-33037761-vmss" {
		t.Errorf("group[0].Basename = %q, want it to equal the VMSS name verbatim", g0.Basename)
	}
	if strings.TrimSuffix(g0.Basename, "-grp") != g0.Basename {
		t.Errorf("group[0].Basename %q was mutated by TrimSuffix(-grp); AKS VMSS names carry no such suffix", g0.Basename)
	}
	if !strings.HasPrefix(g0.Basename+"000000", g0.Basename) {
		t.Errorf("group[0]: node name does not start with Basename %q", g0.Basename)
	}
	if g0.Health.Status != "Healthy" {
		t.Errorf("group[0].Health.Status = %q, want %q", g0.Health.Status, "Healthy")
	}
	if got := mustInt(t, g0.MinSize, "group[0].MinSize"); got != 1 {
		t.Errorf("group[0].MinSize = %d, want 1", got)
	}
	if got := mustInt(t, g0.MaxSize, "group[0].MaxSize"); got != 3 {
		t.Errorf("group[0].MaxSize = %d, want 3", got)
	}
	if got := mustInt(t, g0.Target, "group[0].Target"); got != 2 {
		t.Errorf("group[0].Target = %d, want 2", got)
	}
	if got := mustInt(t, g0.Health.Ready, "group[0].Health.Ready"); got != 2 {
		t.Errorf("group[0].Health.Ready = %d, want 2", got)
	}
	if g0.ScaleUp.Status != "NoActivity" {
		t.Errorf("group[0].ScaleUp.Status = %q, want %q", g0.ScaleUp.Status, "NoActivity")
	}

	// Second pool is a scale-to-zero user pool stuck in scale-up backoff (the
	// canonical AKS quota-exhaustion failure). Bounds still parse; the backoff is
	// surfaced as the ScaleUp status token.
	g1 := st.NodeGroups[1]
	if g1.Basename != "aks-spotgpu-41529630-vmss" {
		t.Errorf("group[1].Basename = %q, want %q", g1.Basename, "aks-spotgpu-41529630-vmss")
	}
	if got := mustInt(t, g1.MinSize, "group[1].MinSize"); got != 0 {
		t.Errorf("group[1].MinSize = %d, want 0", got)
	}
	if got := mustInt(t, g1.MaxSize, "group[1].MaxSize"); got != 6 {
		t.Errorf("group[1].MaxSize = %d, want 6", got)
	}
	if got := mustInt(t, g1.Target, "group[1].Target"); got != 1 {
		t.Errorf("group[1].Target = %d, want 1", got)
	}
	if g1.ScaleUp.Status != "Backoff" {
		t.Errorf("group[1].ScaleUp.Status = %q, want %q", g1.ScaleUp.Status, "Backoff")
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

func TestParseLegacyTextReportsRunningAutoscalerState(t *testing.T) {
	// The legacy format has no autoscalerStatus field. Leaving the state blank
	// left every pre-1.30 cluster's manager rollup permanently "unknown", since
	// the rollup reads AutoscalerState to reach "healthy".
	for _, fixture := range []string{"legacy-text.txt", "aks-legacy-text.txt"} {
		t.Run(fixture, func(t *testing.T) {
			st, err := Parse(loadFixture(t, fixture))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if st.AutoscalerState != "Running" {
				t.Errorf("AutoscalerState = %q, want %q — a payload with health sections is a running controller", st.AutoscalerState, "Running")
			}
		})
	}
}

func TestParseLegacyInitializingPayload(t *testing.T) {
	// Before its first health reading the legacy autoscaler writes the bare
	// word into data.status. Rejecting it reported a parse error for a
	// perfectly healthy starting controller.
	st, err := Parse("Initializing\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Format != FormatLegacyText {
		t.Errorf("Format = %q, want %q", st.Format, FormatLegacyText)
	}
	if st.AutoscalerState != "Initializing" {
		t.Errorf("AutoscalerState = %q, want %q", st.AutoscalerState, "Initializing")
	}
	if len(st.NodeGroups) != 0 {
		t.Errorf("NodeGroups = %+v, want none — nothing was published yet", st.NodeGroups)
	}
}

func TestParseLegacyBackoffCarriesStatusWithoutStructuredDetails(t *testing.T) {
	st, err := Parse(loadFixture(t, "aks-legacy-text.txt"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.ClusterWide.ScaleUp.Status != "Backoff" {
		t.Errorf("cluster-wide ScaleUp.Status = %q, want %q", st.ClusterWide.ScaleUp.Status, "Backoff")
	}
	if st.ClusterWide.ScaleUp.Backoff != nil {
		t.Errorf("legacy text carries no structured backoffInfo; Backoff must stay nil, got %+v", st.ClusterWide.ScaleUp.Backoff)
	}
	var spot *NodeGroup
	for i := range st.NodeGroups {
		if st.NodeGroups[i].Basename == "aks-spotgpu-41529630-vmss" {
			spot = &st.NodeGroups[i]
		}
	}
	if spot == nil {
		t.Fatalf("spotgpu group missing from %+v", st.NodeGroups)
	}
	if spot.ScaleUp.Status != "Backoff" {
		t.Errorf("spotgpu ScaleUp.Status = %q, want %q", spot.ScaleUp.Status, "Backoff")
	}
}
