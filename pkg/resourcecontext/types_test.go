package resourcecontext

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestContextTierMarshalsSnakeCase(t *testing.T) {
	cases := map[ContextTier]string{
		TierBasic:      "basic",
		TierDiagnostic: "diagnostic",
	}
	for c, want := range cases {
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal ContextTier %q: %v", c, err)
		}
		if string(got) != `"`+want+`"` {
			t.Errorf("ContextTier marshal: got %s, want %q", got, want)
		}
	}
}

func TestContextTierUnmarshalsSnakeCase(t *testing.T) {
	cases := map[string]ContextTier{
		"basic":      TierBasic,
		"diagnostic": TierDiagnostic,
	}
	for in, want := range cases {
		var got ContextTier
		if err := json.Unmarshal([]byte(`"`+in+`"`), &got); err != nil {
			t.Fatalf("unmarshal ContextTier %q: %v", in, err)
		}
		if got != want {
			t.Errorf("ContextTier unmarshal %q: got %q, want %q", in, got, want)
		}
	}
}

func TestOmittedReasonMarshalsSnakeCase(t *testing.T) {
	cases := map[OmittedReason]string{
		OmittedRBACDenied:     "rbac_denied",
		OmittedBudgetExceeded: "budget_exceeded",
		OmittedCacheCold:      "cache_cold",
		OmittedNotInstalled:   "not_installed",
	}
	for c, want := range cases {
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal OmittedReason %q: %v", c, err)
		}
		if string(got) != `"`+want+`"` {
			t.Errorf("OmittedReason marshal: got %s, want %q", got, want)
		}
	}
}

func TestOmittedReasonUnmarshalsSnakeCase(t *testing.T) {
	cases := map[string]OmittedReason{
		"rbac_denied":     OmittedRBACDenied,
		"budget_exceeded": OmittedBudgetExceeded,
		"cache_cold":      OmittedCacheCold,
		"not_installed":   OmittedNotInstalled,
	}
	for in, want := range cases {
		var got OmittedReason
		if err := json.Unmarshal([]byte(`"`+in+`"`), &got); err != nil {
			t.Fatalf("unmarshal OmittedReason %q: %v", in, err)
		}
		if got != want {
			t.Errorf("OmittedReason unmarshal %q: got %q, want %q", in, got, want)
		}
	}
}

// TestEmptyResourceContextMarshalsStable pins the wire shape of a zero-value
// ResourceContext. omitempty handling means every nilable field disappears
// and only "tier" remains, with the empty string.
func TestEmptyResourceContextMarshalsStable(t *testing.T) {
	got, err := json.Marshal(ResourceContext{})
	if err != nil {
		t.Fatalf("marshal empty ResourceContext: %v", err)
	}
	want := `{"tier":""}`
	if string(got) != want {
		t.Errorf("empty ResourceContext marshal: got %s, want %s", got, want)
	}
}

// TestResourceContextFieldOrdering pins the on-the-wire field order by
// inspecting the marshaled JSON for a fully populated value. Go's
// encoder emits struct fields in declaration order, so this guards
// against accidental field reshuffling.
func TestResourceContextFieldOrdering(t *testing.T) {
	ac := ResourceContext{
		Tier:          TierBasic,
		ManagedBy:     []ContextRef{{Kind: "Deployment", Name: "api"}},
		Exposes:       []ContextRef{{Kind: "Service", Name: "api"}},
		SelectedBy:    []ContextRef{{Kind: "NetworkPolicy", Name: "default-deny"}},
		Uses:          &UsesBlock{},
		RunsOn:        &ContextRef{Kind: "Node", Name: "node-1"},
		ScaledBy:      []ContextRef{{Kind: "HorizontalPodAutoscaler", Name: "api-hpa"}},
		IssueSummary:  &IssueSummary{Count: 1},
		AuditSummary:  &AuditSummary{Count: 2},
		PolicySummary: &PolicySummary{},
		Hints:         []string{"hint"},
		Omitted:       []OmittedField{{Field: "selectedBy", Reason: OmittedRBACDenied}},
	}
	b, err := json.Marshal(ac)
	if err != nil {
		t.Fatalf("marshal populated ResourceContext: %v", err)
	}
	s := string(b)
	wantOrder := []string{
		`"tier"`,
		`"managedBy"`,
		`"exposes"`,
		`"selectedBy"`,
		`"uses"`,
		`"runsOn"`,
		`"scaledBy"`,
		`"issueSummary"`,
		`"auditSummary"`,
		`"policySummary"`,
		`"hints"`,
		`"omitted"`,
	}
	prev := -1
	for _, key := range wantOrder {
		idx := strings.Index(s, key)
		if idx == -1 {
			t.Fatalf("missing key %s in %s", key, s)
		}
		if idx <= prev {
			t.Fatalf("field %s out of order in %s", key, s)
		}
		prev = idx
	}
}

// TestResourceContextRoundTrip marshals a populated ResourceContext, unmarshals
// it back, and asserts deep equality. Covers every type defined in this
// package.
func TestResourceContextRoundTrip(t *testing.T) {
	orig := ResourceContext{
		Tier: TierDiagnostic,
		ManagedBy: []ContextRef{{
			Kind:      "Deployment",
			Group:     "apps",
			Namespace: "prod",
			Name:      "api",
		}},
		Exposes: []ContextRef{{
			Kind:      "Service",
			Namespace: "prod",
			Name:      "api",
		}},
		SelectedBy: []ContextRef{{
			Kind:      "NetworkPolicy",
			Group:     "networking.k8s.io",
			Namespace: "prod",
			Name:      "default-deny",
		}},
		Uses: &UsesBlock{
			ConfigMaps:     []ContextRef{{Kind: "ConfigMap", Name: "api-config"}},
			Secrets:        []ContextRef{{Kind: "Secret", Name: "api-creds"}},
			ServiceAccount: &ContextRef{Kind: "ServiceAccount", Name: "api-sa"},
			PVCs:           []ContextRef{{Kind: "PersistentVolumeClaim", Name: "data"}},
		},
		RunsOn: &ContextRef{Kind: "Node", Name: "node-1"},
		ScaledBy: []ContextRef{{
			Kind:  "HorizontalPodAutoscaler",
			Group: "autoscaling",
			Name:  "api-hpa",
		}},
		IssueSummary: &IssueSummary{
			Count:           3,
			HighestSeverity: "critical",
			TopReason:       "ImagePullBackOff",
			BySource:        map[string]int{"problem": 2, "condition": 1},
		},
		AuditSummary: &AuditSummary{
			Count:           4,
			HighestSeverity: "warning",
			TopFinding:      "CKV_K8S_8",
		},
		PolicySummary: &PolicySummary{
			Kyverno: &KyvernoSummary{
				Fail: 1, Warn: 2, Pass: 3,
				Top: []KyvernoFinding{{
					Policy:  "require-labels",
					Rule:    "check-app",
					Result:  "fail",
					Message: "missing label",
				}},
			},
		},
		Hints: []string{"Managed by Deployment api"},
		Omitted: []OmittedField{
			{Field: "selectedBy.networkPolicies", Reason: OmittedRBACDenied},
			{Field: "policySummary.kyverno", Reason: OmittedNotInstalled},
		},
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ResourceContext
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("round-trip mismatch:\nwant %#v\ngot  %#v", orig, got)
	}
}

// TestSummaryContextRoundTrip covers SummaryContext + ManagedByRef
// which are not embedded in ResourceContext.
func TestSummaryContextRoundTrip(t *testing.T) {
	orig := SummaryContext{
		ManagedBy:  &ManagedByRef{Kind: "Application", Source: "argocd", Name: "storefront", Namespace: "argocd"},
		Health:     "degraded",
		IssueCount: 2,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SummaryContext
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("round-trip mismatch:\nwant %#v\ngot  %#v", orig, got)
	}

	// Compact wire shape: kind + source + name (+ optional namespace) only.
	// No group, reason, or confidence — those would inflate per-row bytes
	// in list/search responses with no consumer benefit at this tier.
	wantSubstr := []string{`"kind":"Application"`, `"source":"argocd"`, `"name":"storefront"`, `"namespace":"argocd"`, `"health":"degraded"`, `"issueCount":2`}
	s := string(b)
	for _, sub := range wantSubstr {
		if !strings.Contains(s, sub) {
			t.Errorf("SummaryContext JSON missing %s: %s", sub, s)
		}
	}
	for _, forbidden := range []string{`"group"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("SummaryContext JSON leaks %s: %s", forbidden, s)
		}
	}
}

// TestManagedByRefDistinguishesFluxKinds pins the reason Kind was added:
// without it, Flux Kustomization vs HelmRelease serialize to identical
// JSON, forcing consumers to parse the Source string.
func TestManagedByRefDistinguishesFluxKinds(t *testing.T) {
	kustomization := SummaryContext{ManagedBy: &ManagedByRef{Kind: "Kustomization", Source: "flux", Name: "prod-apps", Namespace: "flux-system"}}
	helmRelease := SummaryContext{ManagedBy: &ManagedByRef{Kind: "HelmRelease", Source: "flux", Name: "prod-apps", Namespace: "flux-system"}}

	kJSON, _ := json.Marshal(kustomization)
	hJSON, _ := json.Marshal(helmRelease)
	if string(kJSON) == string(hJSON) {
		t.Fatalf("Flux Kustomization and HelmRelease must serialize to different JSON when Kind is set\nboth: %s", kJSON)
	}
	if !strings.Contains(string(kJSON), `"kind":"Kustomization"`) {
		t.Errorf("Kustomization JSON missing kind: %s", kJSON)
	}
	if !strings.Contains(string(hJSON), `"kind":"HelmRelease"`) {
		t.Errorf("HelmRelease JSON missing kind: %s", hJSON)
	}
}

