package packages

import (
	"testing"
)

func TestSplitChart(t *testing.T) {
	cases := []struct {
		in              string
		wantName, wantV string
	}{
		{"cert-manager-1.14.0", "cert-manager", "1.14.0"},
		{"cert-manager-v1.14.0", "cert-manager", "v1.14.0"},
		{"kube-prometheus-stack-45.27.2", "kube-prometheus-stack", "45.27.2"},
		// scans backwards for first hyphen-followed-by-digit; finds 0.32.0-dev.
		{"karpenter-0.32.0-dev", "karpenter", "0.32.0-dev"},
		{"foo", "foo", ""},
		{"", "", ""},
		{"-1.0.0", "", "1.0.0"},
	}
	for _, c := range cases {
		gotName, gotV := splitChart(c.in)
		if gotName != c.wantName || gotV != c.wantV {
			t.Errorf("splitChart(%q) = (%q, %q), want (%q, %q)", c.in, gotName, gotV, c.wantName, c.wantV)
		}
	}
}

func TestAggregate_HelmOnly(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name:           "cert-manager",
			Namespace:      "cert-manager",
			Chart:          "cert-manager-1.14.0",
			ResourceHealth: "healthy",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Chart != "cert-manager" || r.Version != "1.14.0" || r.Namespace != "cert-manager" {
		t.Errorf("bad row %+v", r)
	}
	if got := r.Sources; len(got) != 1 || got[0] != SourceHelm {
		t.Errorf("want sources=[H], got %v", got)
	}
	if r.Health != "healthy" {
		t.Errorf("want health=healthy, got %q", r.Health)
	}
}

// HelmAPI + workload labels + CRDs all describing the same cert-manager
// install must collapse into a single row with sources [H,L,C].
func TestAggregate_CertManager_AllThreeSources(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name:           "cert-manager",
			Namespace:      "cert-manager",
			Chart:          "cert-manager-1.14.0",
			ResourceHealth: "healthy",
		}},
		Workloads: []Workload{{
			Kind:      "Deployment",
			Namespace: "cert-manager",
			Name:      "cert-manager",
			Labels:    map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "cert-manager",
				"meta.helm.sh/release-namespace": "cert-manager",
			},
			Health: "healthy",
		}},
		CRDs: []CRD{{
			Name:    "certificates.cert-manager.io",
			Group:   "cert-manager.io",
			Kind:    "Certificate",
			Plural:  "certificates",
			Versions: []string{"v1"},
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Chart != "cert-manager" {
		t.Errorf("want chart=cert-manager, got %q", r.Chart)
	}
	want := []SourceCode{SourceHelm, SourceLabels, SourceCRDs}
	if !equalSources(r.Sources, want) {
		t.Errorf("want sources=%v, got %v", want, r.Sources)
	}
}

// Karpenter typical install: Helm secret access blocked → only labels +
// CRDs contribute. Row should be [L,C] with the workload's namespace +
// release-name annotation.
func TestAggregate_Karpenter_NoHelmAccess(t *testing.T) {
	rows := Aggregate(Sources{
		Workloads: []Workload{{
			Kind:      "Deployment",
			Namespace: "karpenter",
			Name:      "karpenter",
			Labels: map[string]string{
				"helm.sh/chart": "karpenter-0.32.0",
			},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "karpenter",
				"meta.helm.sh/release-namespace": "karpenter",
			},
			Health: "healthy",
		}},
		CRDs: []CRD{
			{Name: "nodepools.karpenter.sh", Group: "karpenter.sh", Kind: "NodePool", Plural: "nodepools", Versions: []string{"v1beta1"}},
			{Name: "ec2nodeclasses.karpenter.k8s.aws", Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Plural: "ec2nodeclasses", Versions: []string{"v1beta1"}},
		},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Chart != "karpenter" {
		t.Errorf("want chart=karpenter, got %q", r.Chart)
	}
	want := []SourceCode{SourceLabels, SourceCRDs}
	if !equalSources(r.Sources, want) {
		t.Errorf("want sources=%v, got %v", want, r.Sources)
	}
	// Both CRDs map to "karpenter" — should fold into the same row,
	// not produce duplicates.
}

// Raw-YAML operator: only CRDs registered, no Helm release, no
// workload labels → standalone CRD-only row keyed on the chart we
// know the group corresponds to.
func TestAggregate_RawOperator_KnownGroup(t *testing.T) {
	rows := Aggregate(Sources{
		CRDs: []CRD{{
			Name:    "certificates.cert-manager.io",
			Group:   "cert-manager.io",
			Kind:    "Certificate",
			Plural:  "certificates",
			Versions: []string{"v1"},
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Chart != "cert-manager" || r.Health != "unknown" || r.FromCRDGroup != "" {
		t.Errorf("bad CRD-only row: %+v", r)
	}
	if !equalSources(r.Sources, []SourceCode{SourceCRDs}) {
		t.Errorf("want sources=[C], got %v", r.Sources)
	}
}

// Unknown CRD group should produce a standalone row keyed on the group
// itself (FromCRDGroup set).
func TestAggregate_RawOperator_UnknownGroup(t *testing.T) {
	rows := Aggregate(Sources{
		CRDs: []CRD{{
			Name:    "widgets.example.com",
			Group:   "example.com",
			Kind:    "Widget",
			Plural:  "widgets",
			Versions: []string{"v1alpha1"},
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Chart != "example.com" || r.FromCRDGroup != "example.com" {
		t.Errorf("bad unknown-group row: %+v", r)
	}
}

// Argo Application that declares a Helm chart should merge with the
// Helm release the app actually creates → sources [H, A].
func TestAggregate_ArgoHelmApp_MergesWithHelmRelease(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name:           "my-app",
			Namespace:      "production",
			Chart:          "my-app-2.0.0",
			ResourceHealth: "healthy",
		}},
		GitOpsDeclarations: []Declaration{{
			Source:          "argocd",
			Namespace:       "argocd",
			Name:            "my-app",
			TargetNamespace: "production",
			TargetName:      "my-app",
			Chart:           "my-app",
			ChartVersion:    "2.0.0",
			Status:          "healthy",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 merged row, got %d: %+v", len(rows), rows)
	}
	if got := rows[0].Sources; !equalSources(got, []SourceCode{SourceHelm, SourceArgoCD}) {
		t.Errorf("want sources=[H,A], got %v", got)
	}
}

// Flux Kustomization with no chart info → standalone row keyed on the
// declaration name. Source [F].
func TestAggregate_FluxKustomization_NoChart(t *testing.T) {
	rows := Aggregate(Sources{
		GitOpsDeclarations: []Declaration{{
			Source:          "flux",
			Namespace:       "flux-system",
			Name:            "infra-controllers",
			TargetNamespace: "infra",
			TargetName:      "",
			// No chart field — Kustomization renders raw YAML.
			Status: "healthy",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Chart != "infra-controllers" {
		t.Errorf("want chart=infra-controllers, got %q", r.Chart)
	}
	if !equalSources(r.Sources, []SourceCode{SourceFluxCD}) {
		t.Errorf("want sources=[F], got %v", r.Sources)
	}
}

// Health is the worst across contributors. A degraded workload + a
// healthy Helm release → degraded.
func TestAggregate_HealthIsWorstOf(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name:           "cert-manager",
			Namespace:      "cert-manager",
			Chart:          "cert-manager-1.14.0",
			ResourceHealth: "healthy",
		}},
		Workloads: []Workload{{
			Kind:      "Deployment",
			Namespace: "cert-manager",
			Name:      "cert-manager-cainjector",
			Labels:    map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "cert-manager",
				"meta.helm.sh/release-namespace": "cert-manager",
			},
			Health: "degraded",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Health != "degraded" {
		t.Errorf("want health=degraded, got %q", rows[0].Health)
	}
}

// Worst-of ranking: unhealthy > degraded > unknown > healthy. Plus
// alias collapse (stalled → unhealthy rank, progressing → degraded
// rank). Empty string is "no opinion" (the other side wins).
func TestWorseHealth_Ranking(t *testing.T) {
	cases := []struct {
		a, b, want Health
	}{
		{HealthUnhealthy, HealthDegraded, HealthUnhealthy},
		{HealthDegraded, HealthUnhealthy, HealthUnhealthy},
		{HealthUnknown, HealthHealthy, HealthUnknown},
		{HealthHealthy, HealthUnknown, HealthUnknown},
		{HealthDegraded, HealthUnknown, HealthDegraded},
		{HealthHealthy, HealthDegraded, HealthDegraded},
		{"stalled", HealthDegraded, "stalled"},          // stalled ranks with unhealthy
		{"progressing", HealthHealthy, "progressing"},   // progressing ranks with degraded
		{"", HealthHealthy, HealthHealthy},              // empty = no opinion
		{HealthDegraded, "", HealthDegraded},            // empty = no opinion
		{"weirdo", HealthHealthy, "weirdo"},             // unrecognized → "unknown" rank, beats healthy
	}
	for _, c := range cases {
		if got := worseHealth(c.a, c.b); got != c.want {
			t.Errorf("worseHealth(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// Three-contributor merge: when Helm reports degraded, a workload
// reports unhealthy, and an Argo declaration reports healthy, the row
// must surface the worst (unhealthy).
func TestAggregate_HealthWorstOf_ThreeContributors(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name: "cert-manager", Namespace: "cm", Chart: "cert-manager-1.14.0",
			ResourceHealth: "degraded",
		}},
		Workloads: []Workload{{
			Kind: "Deployment", Namespace: "cm", Name: "cert-manager",
			Labels:      map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cm"},
			Health:      "unhealthy",
		}},
		GitOpsDeclarations: []Declaration{{
			Source: "argocd", Namespace: "argocd", Name: "cert-manager",
			TargetNamespace: "cm", TargetName: "cert-manager", Chart: "cert-manager", Status: "healthy",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Health != "unhealthy" {
		t.Errorf("want health=unhealthy, got %q", rows[0].Health)
	}
}

// Sources order must be canonical H, L, C, A, F regardless of input
// declaration order. Uses cert-manager so the CRD group resolves to
// the same chart as Helm/L/A/F — exercises all five source codes on
// one row.
func TestAggregate_SourceOrderIsCanonical(t *testing.T) {
	rows := Aggregate(Sources{
		// Provide in reverse order to verify Aggregate normalizes.
		GitOpsDeclarations: []Declaration{{
			Source: "flux", Name: "cert-manager", TargetNamespace: "cm", TargetName: "cert-manager", Chart: "cert-manager", Status: "healthy",
		}, {
			Source: "argocd", Name: "cert-manager", TargetNamespace: "cm", TargetName: "cert-manager", Chart: "cert-manager", Status: "healthy",
		}},
		CRDs: []CRD{{Name: "certificates.cert-manager.io", Group: "cert-manager.io", Kind: "Certificate", Plural: "certificates", Versions: []string{"v1"}}},
		Workloads: []Workload{{
			Kind: "Deployment", Namespace: "cm", Name: "cert-manager",
			Labels:      map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cm"},
			Health:      "healthy",
		}},
		Helm: []HelmRelease{{Name: "cert-manager", Namespace: "cm", Chart: "cert-manager-1.14.0", ResourceHealth: "healthy"}},
	})
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}
	for _, r := range rows {
		if r.Chart == "cert-manager" {
			want := []SourceCode{SourceHelm, SourceLabels, SourceCRDs, SourceArgoCD, SourceFluxCD}
			if !equalSources(r.Sources, want) {
				t.Errorf("want canonical sources %v, got %v", want, r.Sources)
			}
			return
		}
	}
	t.Errorf("no cert-manager row found in %+v", rows)
}

// Contributors should carry per-source detail that survives the
// worst-of-health and first-wins-version merges. This is what Hub uses
// for the "Helm: healthy · Argo: degraded" tooltip and for deep-linking
// to the controlling Argo Application.
func TestAggregate_ContributorsCarryPerSourceDetail(t *testing.T) {
	rows := Aggregate(Sources{
		Helm: []HelmRelease{{
			Name:           "cert-manager",
			Namespace:      "cm",
			Chart:          "cert-manager-1.14.0",
			AppVersion:     "v1.14.0",
			ResourceHealth: HealthHealthy,
		}},
		Workloads: []Workload{{
			Kind:        "Deployment",
			Namespace:   "cm",
			Name:        "cert-manager",
			Labels:      map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cm"},
			Health:      HealthDegraded,
		}},
		GitOpsDeclarations: []Declaration{{
			Source:          "argocd",
			Namespace:       "argocd",
			Name:            "cert-manager-app",
			TargetNamespace: "cm",
			TargetName:      "cert-manager",
			Chart:           "cert-manager",
			ChartVersion:    "1.14.1", // disagrees with Helm's 1.14.0
			Status:          HealthHealthy,
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]

	// Aggregated fields keep existing semantics: worst-of-health, first-
	// wins-version (Helm wins because it runs first).
	if r.Health != HealthDegraded {
		t.Errorf("aggregated Health = %q, want degraded (worst-of)", r.Health)
	}
	if r.Version != "1.14.0" {
		t.Errorf("aggregated Version = %q, want 1.14.0 (Helm first-wins)", r.Version)
	}

	// Contributors carry per-source detail and are in canonical order.
	if len(r.Contributors) != 3 {
		t.Fatalf("want 3 contributors (H, L, A), got %d: %+v", len(r.Contributors), r.Contributors)
	}
	wantOrder := []SourceCode{SourceHelm, SourceLabels, SourceArgoCD}
	for i, c := range r.Contributors {
		if c.Source != wantOrder[i] {
			t.Errorf("contributor[%d].Source = %q, want %q (canonical order)", i, c.Source, wantOrder[i])
		}
	}

	helm := findContrib(r, SourceHelm)
	if helm.Health != HealthHealthy || helm.Version != "1.14.0" || helm.AppVersion != "v1.14.0" || helm.ReleaseName != "cert-manager" || helm.ReleaseNamespace != "cm" {
		t.Errorf("Helm contribution wrong: %+v", helm)
	}

	labels := findContrib(r, SourceLabels)
	if labels.Health != HealthDegraded {
		t.Errorf("Labels contribution health = %q, want degraded (per-source preserved)", labels.Health)
	}

	argo := findContrib(r, SourceArgoCD)
	// The Argo Application's identity is exposed for deep-linking.
	if argo.DeclarationName != "cert-manager-app" || argo.DeclarationNamespace != "argocd" {
		t.Errorf("Argo declaration identity wrong: name=%q ns=%q", argo.DeclarationName, argo.DeclarationNamespace)
	}
	// Argo's version disagreement is preserved per-source even though
	// the aggregated row.Version takes Helm's value.
	if argo.Version != "1.14.1" {
		t.Errorf("Argo contribution Version = %q, want 1.14.1 (per-source disagreement preserved)", argo.Version)
	}
	if argo.Health != HealthHealthy {
		t.Errorf("Argo contribution Health = %q, want healthy (per-source preserved)", argo.Health)
	}
}

// Multiple workloads under the same release: their Health worst-ofs
// within the single L contribution, not into separate entries.
func TestAggregate_Contributors_LabelsCollapseAcrossWorkloads(t *testing.T) {
	rows := Aggregate(Sources{
		Workloads: []Workload{
			{
				Kind: "Deployment", Namespace: "cm", Name: "cert-manager",
				Labels:      map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
				Annotations: map[string]string{"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cm"},
				Health:      HealthHealthy,
			},
			{
				Kind: "Deployment", Namespace: "cm", Name: "cert-manager-cainjector",
				Labels:      map[string]string{"helm.sh/chart": "cert-manager-1.14.0"},
				Annotations: map[string]string{"meta.helm.sh/release-name": "cert-manager", "meta.helm.sh/release-namespace": "cm"},
				Health:      HealthDegraded,
			},
		},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if len(r.Contributors) != 1 {
		t.Fatalf("want 1 contributor (collapsed L), got %d: %+v", len(r.Contributors), r.Contributors)
	}
	if r.Contributors[0].Source != SourceLabels {
		t.Fatalf("contributor source = %q, want L", r.Contributors[0].Source)
	}
	if r.Contributors[0].Health != HealthDegraded {
		t.Errorf("L contribution Health = %q, want degraded (worst-of across workloads)", r.Contributors[0].Health)
	}
}

// CRD-only rows produce a C contribution with no release identity (CRDs
// are cluster-scoped registrations). Hub's "managed by Argo →" deep-link
// logic relies on `Contributors[i].DeclarationName != ""` as the GitOps
// signal — if a C contribution accidentally got a release-name from
// somewhere, the contract breaks for downstream consumers.
func TestAggregate_CRDOnlyContributionShape(t *testing.T) {
	rows := Aggregate(Sources{
		CRDs: []CRD{{
			Name: "certificates.cert-manager.io", Group: "cert-manager.io",
			Kind: "Certificate", Plural: "certificates", Versions: []string{"v1"},
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if len(r.Contributors) != 1 {
		t.Fatalf("want 1 contributor, got %d: %+v", len(r.Contributors), r.Contributors)
	}
	c := r.Contributors[0]
	if c.Source != SourceCRDs {
		t.Errorf("Source = %q, want C", c.Source)
	}
	if c.Version != "v1" {
		t.Errorf("Version = %q, want v1", c.Version)
	}
	if c.ReleaseName != "" || c.ReleaseNamespace != "" {
		t.Errorf("CRD contribution should have no release identity, got name=%q ns=%q",
			c.ReleaseName, c.ReleaseNamespace)
	}
	if c.DeclarationName != "" || c.DeclarationNamespace != "" {
		t.Errorf("CRD contribution should have no GitOps declaration identity, got name=%q ns=%q",
			c.DeclarationName, c.DeclarationNamespace)
	}
	if c.Cluster != "" {
		t.Errorf("single-cluster mode should leave Cluster empty, got %q", c.Cluster)
	}
}

// Hub fan-in: contributions from different clusters with the same
// SourceCode must coexist (not collapse). This is what lets Hub deep-
// link to per-cluster Argo App identity in the "same chart, two
// clusters, two Apps" hub-and-spoke pattern.
func TestPackageRow_AddContribution_ClusterDisambiguates(t *testing.T) {
	r := &PackageRow{Chart: "cert-manager"}
	r.AddContribution(SourceContribution{
		Source: SourceArgoCD, Health: HealthHealthy, Version: "1.14.0",
		DeclarationName: "cert-manager-app", DeclarationNamespace: "argocd",
		Cluster: "cluster-a",
	})
	r.AddContribution(SourceContribution{
		Source: SourceArgoCD, Health: HealthDegraded, Version: "1.13.5",
		DeclarationName: "cert-manager-app", DeclarationNamespace: "argocd",
		Cluster: "cluster-b",
	})
	if len(r.Contributors) != 2 {
		t.Fatalf("want 2 A contributions (per cluster), got %d: %+v", len(r.Contributors), r.Contributors)
	}
	gotClusters := map[string]bool{r.Contributors[0].Cluster: true, r.Contributors[1].Cluster: true}
	if !gotClusters["cluster-a"] || !gotClusters["cluster-b"] {
		t.Errorf("expected both cluster contributions, got %v", gotClusters)
	}
	// Aggregated row health is worst-of across all clusters.
	if r.Health != HealthDegraded {
		t.Errorf("Health = %q, want degraded (worst-of across clusters)", r.Health)
	}
	// Helper picks the first contribution by canonical source order.
	if c := r.Contributor(SourceArgoCD); c == nil {
		t.Errorf("Contributor(A) returned nil")
	}
	if c := r.Contributor(SourceHelm); c != nil {
		t.Errorf("Contributor(H) want nil, got %+v", c)
	}
}

// Same-cluster repeated contribution from the same Source still merges
// in place (the merge key (Source, Cluster) collapses to Source when
// Cluster is empty in single-cluster mode).
func TestPackageRow_AddContribution_SingleClusterMerges(t *testing.T) {
	r := &PackageRow{Chart: "cert-manager"}
	r.AddContribution(SourceContribution{Source: SourceArgoCD, Health: HealthHealthy, Version: "1.14.0", DeclarationName: "first-app"})
	r.AddContribution(SourceContribution{Source: SourceArgoCD, Health: HealthDegraded, Version: "1.14.1", DeclarationName: "second-app"})
	if len(r.Contributors) != 1 {
		t.Fatalf("single-cluster: want 1 merged A contribution, got %d", len(r.Contributors))
	}
	c := r.Contributors[0]
	if c.Health != HealthDegraded {
		t.Errorf("merged Health = %q, want degraded (worst-of)", c.Health)
	}
	if c.Version != "1.14.0" {
		t.Errorf("merged Version = %q, want 1.14.0 (first-wins)", c.Version)
	}
	if c.DeclarationName != "first-app" {
		t.Errorf("merged DeclarationName = %q, want first-app (first-wins)", c.DeclarationName)
	}
}

func findContrib(r PackageRow, src SourceCode) SourceContribution {
	for _, c := range r.Contributors {
		if c.Source == src {
			return c
		}
	}
	return SourceContribution{}
}

func equalSources(a, b []SourceCode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
