package k8score

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ctr(name string, presentFields ...string) map[string]any {
	c := map[string]any{"name": name}
	for _, f := range presentFields {
		c[f] = map[string]any{}
	}
	return c
}

// mkWorkload builds an unstructured workload of the given kind with podSpec
// placed at the kind's PodSpec path, plus optional field-manager names in
// metadata.managedFields.
func mkWorkload(t *testing.T, kind string, podSpec map[string]any, managers ...string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": "w", "namespace": "ns"},
	}}
	if podSpec != nil {
		if err := unstructured.SetNestedMap(obj.Object, podSpec, podSpecPath(kind)...); err != nil {
			t.Fatalf("set podSpec for %s: %v", kind, err)
		}
	}
	if len(managers) > 0 {
		mf := make([]any, 0, len(managers))
		for _, m := range managers {
			mf = append(mf, map[string]any{"manager": m})
		}
		if err := unstructured.SetNestedSlice(obj.Object, mf, "metadata", "managedFields"); err != nil {
			t.Fatalf("set managedFields: %v", err)
		}
	}
	return obj
}

func TestDetectExternalManager(t *testing.T) {
	mk := func(labels, annotations map[string]any) *unstructured.Unstructured {
		md := map[string]any{"name": "x"}
		if labels != nil {
			md["labels"] = labels
		}
		if annotations != nil {
			md["annotations"] = annotations
		}
		return &unstructured.Unstructured{Object: map[string]any{"kind": "Deployment", "metadata": md}}
	}

	tests := []struct {
		name         string
		obj          *unstructured.Unstructured
		wantManager  string
		wantInstance string
	}{
		{
			// Argo apps frequently carry Helm annotations because Argo renders
			// charts — Argo must win the precedence ladder.
			name:         "argo beats helm",
			obj:          mk(nil, map[string]any{"argocd.argoproj.io/instance": "my-app", "meta.helm.sh/release-name": "rel"}),
			wantManager:  "Argo CD",
			wantInstance: "my-app",
		},
		{
			name:         "argo tracking-id only",
			obj:          mk(nil, map[string]any{"argocd.argoproj.io/tracking-id": "track-1"}),
			wantManager:  "Argo CD",
			wantInstance: "track-1",
		},
		{
			name:         "flux kustomization namespace-qualified",
			obj:          mk(nil, map[string]any{"kustomize.toolkit.fluxcd.io/name": "ks", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}),
			wantManager:  "Flux (Kustomization)",
			wantInstance: "flux-system/ks",
		},
		{
			name:         "flux kustomization bare name",
			obj:          mk(nil, map[string]any{"kustomize.toolkit.fluxcd.io/name": "ks"}),
			wantManager:  "Flux (Kustomization)",
			wantInstance: "ks",
		},
		{
			name:         "flux helmrelease namespace-qualified",
			obj:          mk(nil, map[string]any{"helm.toolkit.fluxcd.io/name": "hr", "helm.toolkit.fluxcd.io/namespace": "apps"}),
			wantManager:  "Flux (HelmRelease)",
			wantInstance: "apps/hr",
		},
		{
			name:         "flux kustomization beats helm label",
			obj:          mk(map[string]any{"app.kubernetes.io/managed-by": "Helm"}, map[string]any{"kustomize.toolkit.fluxcd.io/name": "ks"}),
			wantManager:  "Flux (Kustomization)",
			wantInstance: "ks",
		},
		{
			name:         "helm release annotation",
			obj:          mk(nil, map[string]any{"meta.helm.sh/release-name": "rel"}),
			wantManager:  "Helm",
			wantInstance: "rel",
		},
		{
			name:         "helm label only uses instance label",
			obj:          mk(map[string]any{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "inst"}, nil),
			wantManager:  "Helm",
			wantInstance: "inst",
		},
		{
			name:        "kubectl excluded",
			obj:         mk(map[string]any{"app.kubernetes.io/managed-by": "kubectl"}, nil),
			wantManager: "",
		},
		{
			// Case-insensitive exclusion — a regression making this case-sensitive
			// would warn on every radar-applied resource.
			name:        "radar excluded case-insensitive",
			obj:         mk(map[string]any{"app.kubernetes.io/managed-by": "Radar"}, nil),
			wantManager: "",
		},
		{
			name:         "generic operator",
			obj:          mk(map[string]any{"app.kubernetes.io/managed-by": "my-operator", "app.kubernetes.io/instance": "inst"}, nil),
			wantManager:  "my-operator",
			wantInstance: "inst",
		},
		{
			name:        "no metadata",
			obj:         mk(nil, nil),
			wantManager: "",
		},
		{
			name:        "nil object",
			obj:         nil,
			wantManager: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, inst := detectExternalManager(tt.obj)
			if mgr != tt.wantManager {
				t.Errorf("manager = %q, want %q", mgr, tt.wantManager)
			}
			if tt.wantManager != "" && inst != tt.wantInstance {
				t.Errorf("instance = %q, want %q", inst, tt.wantInstance)
			}
		})
	}
}

func TestCheckFieldRemoval(t *testing.T) {
	t.Run("deployment container probe survives", func(t *testing.T) {
		pre := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}})
		subm := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app")}})
		post := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}}, "kube-controller-manager", "radar")

		w := checkFieldRemoval(subm, pre, post)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "spec.template.spec.containers[name=app].readinessProbe") {
			t.Errorf("fieldRef wrong: %s", w[0])
		}
		if !strings.Contains(w[0], "kube-controller-manager") {
			t.Errorf("expected other-manager attribution: %s", w[0])
		}
	})

	t.Run("cronjob field path", func(t *testing.T) {
		pre := mkWorkload(t, "CronJob", map[string]any{"containers": []any{ctr("app", "livenessProbe")}})
		subm := mkWorkload(t, "CronJob", map[string]any{"containers": []any{ctr("app")}})
		post := mkWorkload(t, "CronJob", map[string]any{"containers": []any{ctr("app", "livenessProbe")}})

		w := checkFieldRemoval(subm, pre, post)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "spec.jobTemplate.spec.template.spec.containers[name=app].livenessProbe") {
			t.Errorf("cronjob fieldRef wrong: %s", w[0])
		}
	})

	t.Run("pod field path", func(t *testing.T) {
		pre := mkWorkload(t, "Pod", map[string]any{"containers": []any{ctr("app", "resources")}})
		subm := mkWorkload(t, "Pod", map[string]any{"containers": []any{ctr("app")}})
		post := mkWorkload(t, "Pod", map[string]any{"containers": []any{ctr("app", "resources")}})

		w := checkFieldRemoval(subm, pre, post)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "spec.containers[name=app].resources") {
			t.Errorf("pod fieldRef wrong: %s", w[0])
		}
	})

	t.Run("pod-level nodeSelector survives", func(t *testing.T) {
		pre := mkWorkload(t, "Deployment", map[string]any{"nodeSelector": map[string]any{"disktype": "ssd"}, "containers": []any{ctr("app")}})
		subm := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app")}})
		post := mkWorkload(t, "Deployment", map[string]any{"nodeSelector": map[string]any{"disktype": "ssd"}, "containers": []any{ctr("app")}})

		w := checkFieldRemoval(subm, pre, post)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "spec.template.spec.nodeSelector") {
			t.Errorf("nodeSelector fieldRef wrong: %s", w[0])
		}
		// No other manager → the plain "set it to null" remediation.
		if !strings.Contains(w[0], "set its value to null") {
			t.Errorf("expected null-removal remediation: %s", w[0])
		}
	})

	t.Run("field still in submission is not flagged", func(t *testing.T) {
		pre := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}})
		subm := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}})
		post := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}})

		if w := checkFieldRemoval(subm, pre, post); len(w) != 0 {
			t.Fatalf("want no warning when field kept, got %v", w)
		}
	})

	t.Run("successful removal is not flagged", func(t *testing.T) {
		pre := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "readinessProbe")}})
		subm := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app")}})
		post := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app")}})

		if w := checkFieldRemoval(subm, pre, post); len(w) != 0 {
			t.Fatalf("want no warning when field actually removed, got %v", w)
		}
	})

	t.Run("matches container by name not index", func(t *testing.T) {
		pre := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app", "resources"), ctr("side")}})
		subm := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("side"), ctr("app")}})
		post := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("side"), ctr("app", "resources")}})

		w := checkFieldRemoval(subm, pre, post)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %d: %v", len(w), w)
		}
		if !strings.Contains(w[0], "name=app") || strings.Contains(w[0], "name=side") {
			t.Errorf("warning should key on app, not side: %s", w[0])
		}
	})

	t.Run("nil inputs", func(t *testing.T) {
		ok := mkWorkload(t, "Deployment", map[string]any{"containers": []any{ctr("app")}})
		if w := checkFieldRemoval(nil, ok, ok); w != nil {
			t.Errorf("nil submitted should yield nil, got %v", w)
		}
		if w := checkFieldRemoval(ok, nil, ok); w != nil {
			t.Errorf("nil pre should yield nil, got %v", w)
		}
	})
}

func TestPodSpecReferences(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		podSpec map[string]any
		refKind string
		refName string
		want    bool
	}{
		{
			name:    "env configMapKeyRef",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "X", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "cfg"}}}}}}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
		{
			name:    "env secretKeyRef",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "X", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "sec"}}}}}}},
			refKind: "Secret", refName: "sec", want: true,
		},
		{
			name:    "envFrom configMapRef",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "cfg"}}}}}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
		{
			name:    "volume configMap.name",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{ctr("app")}, "volumes": []any{map[string]any{"name": "v", "configMap": map[string]any{"name": "cfg"}}}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
		{
			// Secret volumes key on secretName, not name — a regression copying
			// the ConfigMap shape would silently miss every secret-volume consumer.
			name:    "volume secret.secretName",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{ctr("app")}, "volumes": []any{map[string]any{"name": "v", "secret": map[string]any{"secretName": "sec"}}}},
			refKind: "Secret", refName: "sec", want: true,
		},
		{
			name:    "volume secret.name does not match",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{ctr("app")}, "volumes": []any{map[string]any{"name": "v", "secret": map[string]any{"name": "sec"}}}},
			refKind: "Secret", refName: "sec", want: false,
		},
		{
			name:    "projected configMap source",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{ctr("app")}, "volumes": []any{map[string]any{"name": "v", "projected": map[string]any{"sources": []any{map[string]any{"configMap": map[string]any{"name": "cfg"}}}}}}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
		{
			// Projected secret sources key on name (unlike a plain secret volume).
			name:    "projected secret source uses name",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{ctr("app")}, "volumes": []any{map[string]any{"name": "v", "projected": map[string]any{"sources": []any{map[string]any{"secret": map[string]any{"name": "sec"}}}}}}},
			refKind: "Secret", refName: "sec", want: true,
		},
		{
			name:    "initContainer env match",
			kind:    "Deployment",
			podSpec: map[string]any{"initContainers": []any{map[string]any{"name": "init", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "cfg"}}}}}, "containers": []any{ctr("app")}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
		{
			name:    "name mismatch",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "other"}}}}}},
			refKind: "ConfigMap", refName: "cfg", want: false,
		},
		{
			name:    "wrong refKind",
			kind:    "Deployment",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "cfg"}}}}}},
			refKind: "Secret", refName: "cfg", want: false,
		},
		{
			name:    "cronjob nested path",
			kind:    "CronJob",
			podSpec: map[string]any{"containers": []any{map[string]any{"name": "app", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "cfg"}}}}}},
			refKind: "ConfigMap", refName: "cfg", want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := mkWorkload(t, tt.kind, tt.podSpec)
			if got := podSpecReferences(obj, tt.refKind, tt.refName); got != tt.want {
				t.Errorf("podSpecReferences = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatConsumerWarning(t *testing.T) {
	if got := formatConsumerWarning("ConfigMap", "cfg", nil, false); got != "" {
		t.Errorf("verified-empty should yield no warning, got %q", got)
	}
	if got := formatConsumerWarning("ConfigMap", "cfg", nil, true); !strings.Contains(got, "Could not fully enumerate") {
		t.Errorf("partial-empty should hedge, got %q", got)
	}
	full := formatConsumerWarning("ConfigMap", "cfg", []string{"Deployment/a", "Deployment/b"}, false)
	if !strings.Contains(full, "2 workload(s)") || strings.Contains(full, "incomplete") {
		t.Errorf("complete list should not be marked incomplete: %q", full)
	}
	partial := formatConsumerWarning("ConfigMap", "cfg", []string{"Deployment/a"}, true)
	if !strings.Contains(partial, "incomplete") {
		t.Errorf("partial list should be flagged incomplete: %q", partial)
	}
	many := make([]string, 7)
	for i := range many {
		many[i] = "Deployment/d"
	}
	if got := formatConsumerWarning("Secret", "s", many, false); !strings.Contains(got, "(+2 more)") {
		t.Errorf("expected truncation marker for >5 consumers: %q", got)
	}
}
