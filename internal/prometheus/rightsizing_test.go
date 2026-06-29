package prometheus

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func mustQuantity(t *testing.T, s string) *resource.Quantity {
	t.Helper()
	q := resource.MustParse(s)
	return &q
}

func TestClassifyRightsizing(t *testing.T) {
	q := func(s string) *resource.Quantity { return mustQuantity(t, s) }

	tests := []struct {
		name     string
		p95      float64
		req, lim *resource.Quantity
		resKind  string
		wantTone Tone
		wantMsg  string
		wantRec  bool
	}{
		// Memory OOM rule — the only path that fires critical.
		{"memory p95 at 95% of limit fires critical", 0.95 * 256 * 1024 * 1024, q("128Mi"), q("256Mi"), "memory", ToneCritical, "OOM risk", true},
		// p95 well below limit AND below request → falls into ratio branch.
		{"memory p95 below 95% of limit does not fire critical", 100 * 1024 * 1024, q("256Mi"), q("512Mi"), "memory", ToneOK, "", false},
		{"memory p95 above limit still critical", 300 * 1024 * 1024, q("128Mi"), q("256Mi"), "memory", ToneCritical, "OOM risk", true},

		// CPU throttle rule — strict greater than limit.
		{"cpu p95 above limit fires alert", 1.001, q("100m"), q("1"), "cpu", ToneAlert, "throttling", true},
		// p95 == limit → strict > fails, falls into ratio branch. ratio=1.0 → well-sized.
		{"cpu p95 exactly at limit does not fire alert", 1.0, q("1"), q("1"), "cpu", ToneOK, "Well-sized", false},

		// No request set → warning, not critical.
		{"no cpu request", 0.5, nil, q("1"), "cpu", ToneWarning, "No cpu request", true},
		{"no memory request", 200 * 1024 * 1024, nil, q("1Gi"), "memory", ToneWarning, "No memory request", true},

		// P95 exceeds request but within limit → warning.
		{"cpu p95 exceeds request", 0.15, q("100m"), q("1"), "cpu", ToneWarning, "exceeds request", true},

		// "Well-sized" thresholds — the user-facing nag policy. Boundary at 3×.
		{"ratio 1.0 well-sized", 0.1, q("100m"), q("500m"), "cpu", ToneOK, "Well-sized", false},
		{"ratio 3.0 well-sized", 0.1, q("300m"), q("1"), "cpu", ToneOK, "Well-sized", false},
		{"ratio just over 3.0 shows headroom", 0.0999, q("300m"), q("1"), "cpu", ToneOK, "headroom", false},

		// CPU over-provisioned threshold = 8× (strict greater).
		{"cpu ratio 8.0 shows headroom only", 0.1, q("800m"), q("2"), "cpu", ToneOK, "headroom", false},
		{"cpu ratio above 8 surfaces as info", 0.0999, q("800m"), q("2"), "cpu", ToneInfo, "Over-provisioned", true},

		// Memory over-provisioned threshold = 5× (strict greater).
		{"memory ratio 5.0 shows headroom only", 50 * 1024 * 1024, q("250Mi"), q("1Gi"), "memory", ToneOK, "headroom", false},
		{"memory ratio above 5 surfaces as info", 49.9 * 1024 * 1024, q("250Mi"), q("1Gi"), "memory", ToneInfo, "Over-provisioned", true},

		// Defensive: zero request short-circuits without crashing.
		{"zero cpu request", 0.5, q("0"), nil, "cpu", ToneOK, "", false},

		// p95 == 0 must not produce "+Inf headroom" via reqVal/p95. Treat idle
		// containers as well-sized and emit no recommendation.
		{"cpu p95 zero is idle, not +Inf", 0, q("100m"), q("1"), "cpu", ToneOK, "Idle", false},
		{"memory p95 zero is idle, not +Inf", 0, q("128Mi"), q("256Mi"), "memory", ToneOK, "Idle", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := RightsizingRow{Tone: ToneOK}
			classifyRightsizing(&row, tc.p95, tc.req, tc.lim, tc.resKind)
			if row.Tone != tc.wantTone {
				t.Errorf("tone = %s, want %s (msg=%q)", row.Tone, tc.wantTone, row.Message)
			}
			if tc.wantMsg != "" && !strings.Contains(row.Message, tc.wantMsg) {
				t.Errorf("message = %q, want substring %q", row.Message, tc.wantMsg)
			}
			if tc.wantRec && row.RecommendedReq == nil {
				t.Errorf("expected RecommendedReq populated, got nil")
			}
			if !tc.wantRec && row.RecommendedReq != nil {
				t.Errorf("expected no RecommendedReq, got %q", *row.RecommendedReq)
			}
		})
	}
}

func TestRecommendRequest(t *testing.T) {
	tests := []struct {
		name    string
		p95     float64
		resKind string
		want    string
	}{
		// CPU — 15% headroom, round to a clean 10m step, floor at 10m. Exact
		// step depends on float repr (1.15× a non-representable value can
		// round down by one step), but the result is always a clean 10m
		// boundary — the "no noisy 137m recommendations" promise.
		{"cpu sub-milli rounds to 1m", 0.0001, "cpu", "1m"},
		{"cpu 100m → ~115m → clean 110m step", 0.100, "cpu", "110m"},
		{"cpu 1 core → ~1150m → 1.1 cores (round-half-to-even)", 1.0, "cpu", "1.1"},
		{"cpu integer cores trim trailing zero", 0.870, "cpu", "1"},
		{"cpu floor at 10m", 0.001, "cpu", "10m"},

		// Memory — 10% headroom, round up to next 16Mi, floor at 16Mi.
		{"memory tiny floors at 16Mi", 1024, "memory", "16Mi"},
		{"memory 100Mi → 110Mi → next 16Mi step", 100 * 1024 * 1024, "memory", "112Mi"},
		{"memory 1Gi exact boundary", 1024 * 1024 * 1024, "memory", "1.1Gi"},
		{"memory just under 1Gi shows Mi", 900 * 1024 * 1024, "memory", "992Mi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := recommendRequest(tc.p95, tc.resKind)
			if got != tc.want {
				t.Errorf("recommendRequest(%g, %q) = %q, want %q", tc.p95, tc.resKind, got, tc.want)
			}
		})
	}
}

func TestExtractRuntimeContainers(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	onFailure := corev1.ContainerRestartPolicy("OnFailure")

	tests := []struct {
		name      string
		spec      *corev1.PodSpec
		wantNames []string
	}{
		{"regular containers only", &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}, {Name: "proxy"}},
		}, []string{"app", "proxy"}},

		{"pure init excluded", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "migrate"}},
		}, []string{"app"}},

		// Load-bearing native-sidecar behavior — without this the request/limit
		// overlay misses the sidecar's contribution.
		{"native sidecar included", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "envoy", RestartPolicy: &always}},
		}, []string{"app", "envoy"}},

		{"non-Always init excluded even with restart policy set", &corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{{Name: "boot", RestartPolicy: &onFailure}},
		}, []string{"app"}},

		{"init-only pod returns empty runtime", &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "job"}},
		}, []string{}},

		{"regular + sidecar + pure init mix", &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
			InitContainers: []corev1.Container{
				{Name: "wait-db"},
				{Name: "envoy", RestartPolicy: &always},
			},
		}, []string{"app", "envoy"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRuntimeContainers(tc.spec)
			gotNames := make([]string, len(got))
			for i, c := range got {
				gotNames[i] = c.name
			}
			if !slicesEqual(gotNames, tc.wantNames) {
				t.Errorf("names = %v, want %v", gotNames, tc.wantNames)
			}
		})
	}
}

func TestFormatRightsizingValue(t *testing.T) {
	tests := []struct {
		v       float64
		resKind string
		want    string
	}{
		{0.0005, "cpu", "1m"},
		{2.0, "cpu", "2"},
		{1.5, "cpu", "1.5"},
		{1024, "memory", "16Mi"},
		{0, "memory", "16Mi"},
		{float64(2 * 1024 * 1024 * 1024), "memory", "2.0Gi"},
		{1.0, "disk", ""},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatRightsizingValue(tc.v, tc.resKind)
			if got != tc.want {
				t.Errorf("formatRightsizingValue(%g, %q) = %q, want %q", tc.v, tc.resKind, got, tc.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
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
