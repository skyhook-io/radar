package k8score

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDurationShort(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m30s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour, "1h"},
		{time.Hour + time.Minute, "1h1m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{49 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := durationShort(tt.d); got != tt.want {
			t.Errorf("durationShort(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func mkHealthObj(kind, condType, condStatus string, created, ltt time.Time) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"kind":     kind,
		"metadata": map[string]any{"name": "w"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               condType,
					"status":             condStatus,
					"lastTransitionTime": ltt.UTC().Format(time.RFC3339),
				},
			},
		},
	}}
	obj.SetCreationTimestamp(metav1.NewTime(created))
	return obj
}

func TestWorkloadHealthWarning(t *testing.T) {
	now := time.Now()

	t.Run("never healthy since creation", func(t *testing.T) {
		// Condition went False essentially at creation → since-creation phrasing.
		obj := mkHealthObj("Deployment", "Available", "False", now.Add(-2*time.Minute), now.Add(-2*time.Minute+time.Second))
		w := workloadHealthWarning(obj, now)
		if !strings.Contains(w, "since about when this resource was created (resource age: ") {
			t.Errorf("want since-creation phrasing, got %q", w)
		}
	})

	t.Run("regressed long after creation", func(t *testing.T) {
		obj := mkHealthObj("Deployment", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		w := workloadHealthWarning(obj, now)
		if strings.Contains(w, "since about when this resource was created") {
			t.Errorf("should not use since-creation phrasing, got %q", w)
		}
		if !strings.Contains(w, "(resource age:") {
			t.Errorf("want regressed phrasing with resource age, got %q", w)
		}
	})

	// A failure present since creation reads the same on a resource created
	// seconds ago and on one broken for weeks; only the age tells them apart,
	// so every form states it and none offers an interpretation of what the
	// timing means for an incident.
	t.Run("every form states the age and nothing more", func(t *testing.T) {
		cases := []struct {
			name string
			obj  *unstructured.Unstructured
			want string
		}{
			{
				name: "young pod failing since creation",
				obj:  mkHealthObj("Pod", "Ready", "False", now.Add(-20*time.Second), now.Add(-20*time.Second)),
				want: "Condition `Ready=False` for ~20s — since about when this resource was created (resource age: 20s).",
			},
			{
				name: "young deployment failing since creation",
				obj:  mkHealthObj("Deployment", "Available", "False", now.Add(-11*time.Second), now.Add(-11*time.Second)),
				want: "Condition `Available=False` for ~11s — since about when this resource was created (resource age: 11s).",
			},
			{
				name: "deployment failing since creation 30 days ago",
				obj:  mkHealthObj("Deployment", "Available", "False", now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)),
				want: "Condition `Available=False` for ~30d — since about when this resource was created (resource age: 30d).",
			},
			{
				// The 30s slop absorbs a healthy window this short, which is
				// why the since-creation form says "about".
				name: "pod ready for 20s then failing",
				obj:  mkHealthObj("Pod", "Ready", "False", now.Add(-25*time.Second), now.Add(-5*time.Second)),
				want: "Condition `Ready=False` for ~5s — since about when this resource was created (resource age: 25s).",
			},
			{
				name: "deployment regressed after a healthy hour",
				obj:  mkHealthObj("Deployment", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute)),
				want: "Condition `Available=False` for ~5m (resource age: 1h).",
			},
		}
		for _, tc := range cases {
			if got := workloadHealthWarning(tc.obj, now); got != tc.want {
				t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("available true yields nothing", func(t *testing.T) {
		obj := mkHealthObj("Deployment", "Available", "True", now.Add(-time.Hour), now.Add(-time.Hour))
		if w := workloadHealthWarning(obj, now); w != "" {
			t.Errorf("healthy workload should yield no warning, got %q", w)
		}
	})

	t.Run("pod uses Ready condition", func(t *testing.T) {
		obj := mkHealthObj("Pod", "Ready", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(obj, now); !strings.Contains(w, "`Ready=False`") {
			t.Errorf("pod should report Ready, got %q", w)
		}
		// A Pod's Available condition is irrelevant — only Ready drives the warning.
		other := mkHealthObj("Pod", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(other, now); w != "" {
			t.Errorf("pod with no Ready condition should yield nothing, got %q", w)
		}
	})

	t.Run("unhandled kind yields nothing", func(t *testing.T) {
		obj := mkHealthObj("DaemonSet", "Available", "False", now.Add(-time.Hour), now.Add(-5*time.Minute))
		if w := workloadHealthWarning(obj, now); w != "" {
			t.Errorf("DaemonSet has no Available condition; want nothing, got %q", w)
		}
	})

	t.Run("future lastTransitionTime is guarded", func(t *testing.T) {
		// Clock skew: condition stamped in the future → negative failingFor must
		// not produce a "~-Xs" warning.
		obj := mkHealthObj("StatefulSet", "Available", "False", now.Add(-time.Hour), now.Add(5*time.Minute))
		if w := workloadHealthWarning(obj, now); w != "" {
			t.Errorf("future lastTransitionTime should yield nothing, got %q", w)
		}
	})

	t.Run("no conditions yields nothing", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "w"}}}
		if w := workloadHealthWarning(obj, now); w != "" {
			t.Errorf("no conditions should yield nothing, got %q", w)
		}
	})
}

// A tracking ID copied from another object names that object's manager, not
// this one's: no warning. A controller the manager answer doesn't model keeps
// its warning beside one.
func TestManagerWarningSkipsCopiesAndKeepsUnmodeledControllers(t *testing.T) {
	secret := func(labels, annotations map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"namespace": "backup", "name": "copy", "labels": labels, "annotations": annotations}}}
	}
	copied := secret(nil, map[string]any{"argocd.argoproj.io/tracking-id": "source:/Secret:shop/original"})
	if got := EnrichObjectWarnings(copied); len(got) != 0 {
		t.Errorf("copied tracking ID warned: %q", got)
	}
	own := secret(nil, map[string]any{"argocd.argoproj.io/tracking-id": "source:/Secret:backup/copy"})
	if got := EnrichObjectWarnings(own); len(got) != 1 {
		t.Errorf("own tracking ID: %q; want one warning", got)
	}
	if got := EnrichRuntimeObjectAnsweredWarnings(own); len(got) != 0 {
		t.Errorf("answered Argo CD warning kept: %q", got)
	}
	certManager := secret(map[string]any{"app.kubernetes.io/managed-by": "cert-manager"}, nil)
	if got := EnrichRuntimeObjectAnsweredWarnings(certManager); len(got) != 1 {
		t.Errorf("cert-manager warning beside an answer: %q; want it kept", got)
	}
	// A copied tracking ID doesn't hide the controller the object's own label names.
	both := secret(map[string]any{"app.kubernetes.io/managed-by": "cert-manager"}, map[string]any{"argocd.argoproj.io/tracking-id": "source:/Secret:shop/original"})
	for name, got := range map[string][]string{"plain": EnrichObjectWarnings(both), "answered": EnrichRuntimeObjectAnsweredWarnings(both)} {
		if len(got) != 1 || !strings.Contains(got[0], "cert-manager") {
			t.Errorf("%s: copied tracking ID beside a cert-manager label: %q; want the cert-manager warning", name, got)
		}
	}

	// Beside the answer, a claim it weighs doesn't hide what it doesn't.
	ownWithCertManager := secret(map[string]any{"app.kubernetes.io/managed-by": "cert-manager"}, map[string]any{"argocd.argoproj.io/tracking-id": "source:/Secret:backup/copy"})
	if got := EnrichRuntimeObjectAnsweredWarnings(ownWithCertManager); len(got) != 1 || !strings.Contains(got[0], "cert-manager") {
		t.Errorf("own tracking ID beside a cert-manager label, answered: %q; want the cert-manager warning", got)
	}
	helmLabels := secret(map[string]any{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "chart"}, nil)
	if got := EnrichRuntimeObjectAnsweredWarnings(helmLabels); len(got) != 1 || !strings.Contains(got[0], "Helm") {
		t.Errorf("Helm labels without a release annotation, answered: %q; want the Helm warning", got)
	}
}
