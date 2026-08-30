package context

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummary_GenericCRD_ConditionPriority(t *testing.T) {
	tests := []struct {
		name       string
		conditions []map[string]any
		phase      string // fallback status.phase
		wantStatus string
	}{
		{
			name:       "Ready=True",
			conditions: []map[string]any{{"type": "Ready", "status": "True"}},
			wantStatus: "Ready",
		},
		{
			name:       "Ready=False with reason",
			conditions: []map[string]any{{"type": "Ready", "status": "False", "reason": "ConfigError"}},
			wantStatus: "ConfigError",
		},
		{
			name:       "Ready=False no reason",
			conditions: []map[string]any{{"type": "Ready", "status": "False"}},
			wantStatus: "NotReady",
		},
		{
			name: "Available over unknown condition",
			conditions: []map[string]any{
				{"type": "Healthy", "status": "True"},
				{"type": "Available", "status": "True"},
			},
			wantStatus: "Available",
		},
		{
			name: "Ready wins over Available",
			conditions: []map[string]any{
				{"type": "Available", "status": "True"},
				{"type": "Ready", "status": "True"},
			},
			wantStatus: "Ready",
		},
		{
			name: "Synced used when no Ready or Available",
			conditions: []map[string]any{
				{"type": "Synced", "status": "True"},
			},
			wantStatus: "Synced",
		},
		{
			name:       "no conditions, has phase",
			conditions: nil,
			phase:      "Active",
			wantStatus: "Active",
		},
		{
			name:       "empty — no conditions, no phase",
			conditions: nil,
			wantStatus: "",
		},
		{
			name: "falls back to first condition when none are priority",
			conditions: []map[string]any{
				{"type": "Initialized", "status": "True"},
				{"type": "Progressing", "status": "True"},
			},
			wantStatus: "Initialized",
		},
		{
			name: "first condition False",
			conditions: []map[string]any{
				{"type": "Initialized", "status": "False"},
			},
			wantStatus: "NotInitialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "example.io/v1",
					"kind":       "Widget",
					"metadata":   map[string]any{"name": "test", "namespace": "default"},
				},
			}

			// Build status
			status := map[string]any{}
			if len(tt.conditions) > 0 {
				conds := make([]any, len(tt.conditions))
				for i, c := range tt.conditions {
					conds[i] = c
				}
				status["conditions"] = conds
			}
			if tt.phase != "" {
				status["phase"] = tt.phase
			}
			if len(status) > 0 {
				obj.Object["status"] = status
			}

			raw := MinifyUnstructured(obj, LevelSummary)
			s, ok := raw.(*ResourceSummary)
			if !ok {
				t.Fatalf("Expected *ResourceSummary, got %T", raw)
			}
			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
		})
	}
}

func TestSummary_ArgoApplication(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "my-app", "namespace": "argocd"},
			"spec": map[string]any{
				"source": map[string]any{"repoURL": "https://github.com/org/repo"},
			},
			"status": map[string]any{
				"sync":   map[string]any{"status": "OutOfSync"},
				"health": map[string]any{"status": "Degraded"},
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelSummary)
	s := raw.(*ResourceSummary)

	if s.Status != "OutOfSync" {
		t.Errorf("Status = %q, want OutOfSync", s.Status)
	}
	if s.Issue != "Degraded" {
		t.Errorf("Issue = %q, want Degraded", s.Issue)
	}
	if s.Image != "https://github.com/org/repo" {
		t.Errorf("Image (repo) = %q, want repo URL", s.Image)
	}

	// Healthy app should have no issue
	objHealthy := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "healthy-app", "namespace": "argocd"},
			"status": map[string]any{
				"sync":   map[string]any{"status": "Synced"},
				"health": map[string]any{"status": "Healthy"},
			},
		},
	}
	rawH := MinifyUnstructured(objHealthy, LevelSummary)
	sH := rawH.(*ResourceSummary)
	if sH.Issue != "" {
		t.Errorf("Healthy app Issue = %q, want empty", sH.Issue)
	}
}

func TestSummary_FluxHelmRelease(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2beta1",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"name": "redis", "namespace": "flux-system"},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{"chart": "redis"},
				},
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
				},
				"lastAppliedRevision": "16.8.5",
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelSummary)
	s := raw.(*ResourceSummary)

	if s.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", s.Status)
	}
	if s.Version != "16.8.5" {
		t.Errorf("Version = %q, want 16.8.5", s.Version)
	}
	if s.Image != "redis" {
		t.Errorf("Image (chart) = %q, want redis", s.Image)
	}
}

// Mirrors gateway-policy-status.test.ts. The two implementations feed the UI
// and the MCP answers respectively, so they have to agree about every case a
// controller actually produces. Two deliberate differences: the reason-less
// fallback spelling ("NotAccepted" here, "Not Accepted" there — see
// policyProblem), and where per-ancestor failure detail goes on mixed reasons
// (the UI has a tooltip; MCP only has the text, so here it rides in-text).
func TestGatewayPolicyStatus(t *testing.T) {
	cond := func(t, s, reason string) map[string]any {
		c := map[string]any{"type": t, "status": s}
		if reason != "" {
			c["reason"] = reason
		}
		return c
	}
	anc := func(conds ...map[string]any) map[string]any {
		out := make([]any, 0, len(conds))
		for _, c := range conds {
			out = append(out, c)
		}
		return map[string]any{"conditions": out}
	}
	named := func(ns, name string, a map[string]any) map[string]any {
		ref := map[string]any{"name": name}
		if ns != "" {
			ref["namespace"] = ns
		}
		a["ancestorRef"] = ref
		return a
	}
	withController := func(controller string, a map[string]any) map[string]any {
		a["controllerName"] = controller
		return a
	}
	withSection := func(section string, a map[string]any) map[string]any {
		a["ancestorRef"].(map[string]any)["sectionName"] = section
		return a
	}
	withKind := func(kind string, a map[string]any) map[string]any {
		a["ancestorRef"].(map[string]any)["kind"] = kind
		return a
	}
	policy := func(ancestors ...map[string]any) *unstructured.Unstructured {
		out := make([]any, 0, len(ancestors))
		for _, a := range ancestors {
			out = append(out, a)
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{"ancestors": out},
		}}
	}

	tests := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
		ok   bool
	}{
		// A live EnvoyPatchPolicy: taken up by the controller, then not applied.
		// Reading acceptance first would call this healthy.
		{"accepted then not applied", policy(anc(cond("Accepted", "True", "Accepted"), cond("Programmed", "False", "ResourceNotFound"))), "ResourceNotFound", true},
		{"plain accepted", policy(anc(cond("Accepted", "True", "Accepted"))), "Accepted", true},
		{"rejected carries the reason", policy(anc(cond("Accepted", "False", "Conflicted"))), "Conflicted", true},
		{"rejected without a reason", policy(anc(cond("Accepted", "False", ""))), "NotAccepted", true},
		{"warning is not hidden by acceptance", policy(anc(cond("Accepted", "True", ""), cond("Warning", "True", "ShadowedRules"))), "ShadowedRules", true},
		{"failure anywhere wins and is counted", policy(
			anc(cond("Accepted", "True", "")),
			anc(cond("Accepted", "False", "NotAllowed")),
			anc(cond("Accepted", "True", "")),
		), "NotAllowed (1/3)", true},
		{"no count for a single ancestor", policy(anc(cond("Accepted", "False", "NotAllowed"))), "NotAllowed", true},
		{"an ancestor with no verdict is not accepted", policy(anc(cond("Accepted", "True", "")), anc()), "Pending (1/2)", true},
		{"unknown is undecided, not failed", policy(anc(cond("Accepted", "Unknown", "Pending"))), "Pending", true},
		{"healthy only when every ancestor accepted", policy(anc(cond("Accepted", "True", "")), anc(cond("Accepted", "True", ""))), "Accepted", true},
		// One slot for both kinds of problem reported the warning's reason with
		// the failure's count, reading as though the warning was the failure.
		{"failure wins over an earlier warning", policy(
			anc(cond("Accepted", "True", ""), cond("Warning", "True", "ShadowedRules")),
			anc(cond("Accepted", "False", "NotAllowed")),
		), "NotAllowed (1/2)", true},
		{"warning still reported when nothing failed", policy(
			anc(cond("Accepted", "True", ""), cond("Warning", "True", "ShadowedRules")),
			anc(cond("Accepted", "True", "")),
		), "ShadowedRules (1/2)", true},
		// Accepted=False reads as NotAccepted; Warning=True means there IS a
		// warning, so the same construction would invert it.
		{"reason-less warning keeps its own name", policy(anc(cond("Accepted", "True", ""), cond("Warning", "True", ""))), "Warning", true},
		// Not a verdict: a dual-shape policy falls back to its top-level
		// conditions, and one with nothing else renders as absent.
		{"attaches to nothing", policy(), "", false},
		// GKE reports attachment with its own condition; BackendTLSPolicy
		// requires ResolvedRefs as well as Accepted.
		{"Attached=False is a failure", policy(anc(cond("Accepted", "True", ""), cond("Attached", "False", "InvalidTarget"))), "InvalidTarget", true},
		{"ResolvedRefs=False is a failure", policy(anc(cond("Accepted", "True", ""), cond("ResolvedRefs", "False", "InvalidCACertificateRef"))), "InvalidCACertificateRef", true},
		// GEP-713 lists Reconciling as a legitimate in-flight state.
		{"Reconciling is in-flight, not failed", policy(anc(cond("Accepted", "True", ""), cond("Programmed", "False", "Reconciling"))), "Reconciling", true},
		// GKE's healthy shape carries no Accepted condition at all.
		{"a verdict not spelled Accepted still counts", policy(anc(cond("Attached", "True", ""))), "Attached", true},
		{"Programmed alone is a verdict", policy(anc(cond("Programmed", "True", ""))), "Programmed", true},
		// Refs resolving is a precondition, not a verdict.
		{"ResolvedRefs alone is not a verdict", policy(anc(cond("ResolvedRefs", "True", ""))), "Pending", true},
		{"partially applied is not success", policy(anc(cond("Accepted", "True", ""), cond("Programmed", "True", "PartiallyProgrammed"))), "PartiallyProgrammed", true},
		// Reporting the first reason with a count claims they all failed that way.
		{"mixed failure reasons report a count and each ancestor's reason", policy(
			anc(cond("Accepted", "False", "NotAllowed")),
			anc(cond("Accepted", "False", "ResourceNotFound")),
			anc(cond("Accepted", "True", "")),
		), "2/3 failed (NotAllowed; ResourceNotFound)", true},
		// "2/3 failed" without naming the Gateways sends the reader digging
		// through raw status for which ancestor failed how.
		{"mixed failures name the Gateway that failed each way", policy(
			named("team-a", "gw-a", anc(cond("Accepted", "False", "NotAllowed"))),
			named("", "gw-b", anc(cond("Accepted", "False", "ResourceNotFound"))),
			anc(cond("Accepted", "True", "")),
		), "2/3 failed (team-a/gw-a: NotAllowed; gw-b: ResourceNotFound)", true},
		// GEP-713 keys ancestor status on ancestorRef + controllerName: the same
		// Gateway appears once per controller, and a bare ns/name label would
		// attribute both verdicts to one indistinguishable thing.
		{"colliding labels are told apart by controller", policy(
			withController("ctrl-a.example", named("team-a", "gw", anc(cond("Accepted", "False", "NotAllowed")))),
			withController("ctrl-b.example", named("team-a", "gw", anc(cond("Accepted", "False", "ResourceNotFound")))),
		), "2/2 failed (team-a/gw (ctrl-a.example): NotAllowed; team-a/gw (ctrl-b.example): ResourceNotFound)", true},
		// sectionName and a non-Gateway kind are part of the identity and short
		// enough to always carry.
		{"section and kind ride in the label", policy(
			withSection("tls", named("team-a", "gw", anc(cond("Accepted", "False", "NotAllowed")))),
			withKind("Service", named("team-a", "svc", anc(cond("Accepted", "False", "ResourceNotFound")))),
		), "2/2 failed (team-a/gw:tls: NotAllowed; Service team-a/svc: ResourceNotFound)", true},
		// Reasons may be 1024 chars and foreign CRDs need not honor the
		// 16-ancestor cap, so the list is bounded rather than trusted.
		{"the detail list is capped", policy(
			named("", "gw-1", anc(cond("Accepted", "False", "R1"))),
			named("", "gw-2", anc(cond("Accepted", "False", "R2"))),
			named("", "gw-3", anc(cond("Accepted", "False", "R3"))),
			named("", "gw-4", anc(cond("Accepted", "False", "R4"))),
			named("", "gw-5", anc(cond("Accepted", "False", "R5"))),
			named("", "gw-6", anc(cond("Accepted", "False", "R6"))),
		), "6/6 failed (gw-1: R1; gw-2: R2; gw-3: R3; gw-4: R4; +2 more)", true},
		{"matching failure reasons keep the reason", policy(
			anc(cond("Accepted", "False", "NotAllowed")),
			anc(cond("Accepted", "False", "NotAllowed")),
		), "NotAllowed (2/2)", true},
		{"not a policy", &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{}}}}, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := gatewayPolicyStatus(tc.obj)
			if ok != tc.ok {
				t.Fatalf("recognized = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGatewayPolicyStatusToleratesMalformedInput(t *testing.T) {
	// Entries that are the wrong shape but still valid unstructured content are
	// skipped, not fatal: one usable ancestor still yields a verdict.
	mixed := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"ancestors": []any{
			nil,
			"nope",
			map[string]any{"conditions": "not-a-slice"},
			map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}}},
		}},
	}}
	got, ok := gatewayPolicyStatus(mixed)
	if !ok {
		t.Fatal("a list with one usable ancestor is still a PolicyStatus")
	}
	// Three of the four ancestors produced no verdict, so this is not healthy.
	if got != "Pending (3/4)" {
		t.Errorf("status = %q, want %q", got, "Pending (3/4)")
	}

	// Content the API server could never return — a bare Go int is not valid
	// unstructured JSON. NestedSlice panics on this rather than erroring, which
	// is why the reader takes the field without copying and asserts the shape
	// itself. The entry is skipped and the remaining ancestors still resolve.
	odd := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"ancestors": []any{42}},
	}}
	var got2 string
	var ok2 bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("reading a PolicyStatus must not panic, got: %v", r)
			}
		}()
		got2, ok2 = gatewayPolicyStatus(odd)
	}()
	if !ok2 || got2 != "Pending" {
		t.Errorf("status = %q ok = %v, want %q true", got2, ok2, "Pending")
	}

	// A field that is not a list at all is not a PolicyStatus.
	notAList := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"ancestors": map[string]any{}},
	}}
	if _, ok := gatewayPolicyStatus(notAList); ok {
		t.Error("a non-list ancestors field should fall through, not be reported as a policy")
	}
}
