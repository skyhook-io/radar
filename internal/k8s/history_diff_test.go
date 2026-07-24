package k8s

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Added/removed containers must surface as a single row naming the container
// (with its image) — not vanish because per-field diffs only cover containers
// present in both templates.
func TestDiffPodTemplateConfig_ContainerAddRemove(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Image: "app:v1"},
		{Name: "legacy-sidecar", Image: "sidecar:v3"},
	}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Image: "app:v1"},
		{Name: "otel-agent", Image: "otel/agent:v2"},
	}}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	joined := strings.Join(summary, "; ")
	if !strings.Contains(joined, "container otel-agent added") {
		t.Errorf("added container missing from summary: %q", joined)
	}
	if !strings.Contains(joined, "container legacy-sidecar removed") {
		t.Errorf("removed container missing from summary: %q", joined)
	}
	if len(changes) != 2 {
		t.Errorf("changes = %d, want 2 (one add, one remove): %+v", len(changes), changes)
	}
	// Unchanged shared container must not produce noise.
	if strings.Contains(joined, "app") {
		t.Errorf("unchanged container must not appear in summary: %q", joined)
	}
}

func TestDiffPodTemplateConfig_ImagePullPolicy(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "app:v1", ImagePullPolicy: corev1.PullIfNotPresent,
	}}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "app:v1", ImagePullPolicy: corev1.PullAlways,
	}}}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	if !hasChangePath(changes, "spec.template.spec.containers[app].imagePullPolicy") {
		t.Fatalf("expected imagePullPolicy change, got %+v", changes)
	}
	if joined := strings.Join(summary, "; "); !strings.Contains(joined, "imagePullPolicy(app)") {
		t.Fatalf("expected imagePullPolicy summary, got %q", joined)
	}
}

func TestDiffPodTemplateConfig_CommandArgsRedaction(t *testing.T) {
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "app",
		Image:   "app:v1",
		Command: []string{"server", "--client-secret=old-command-secret"},
		Args:    []string{"--api-key", "old-api-key", "--mode=prod", "password=old-password"},
	}}}
	newSpec := corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "app",
		Image:   "app:v1",
		Command: []string{"server", "--client-secret=new-command-secret"},
		Args:    []string{"--api-key", "new-api-key", "--mode=prod", "password=new-password"},
	}}}

	changes, _ := diffPodTemplateConfig(oldSpec, newSpec)
	for _, path := range []string{
		"spec.template.spec.containers[app].command",
		"spec.template.spec.containers[app].args",
	} {
		change, ok := findChangePath(changes, path)
		if !ok {
			t.Fatalf("expected %s change, got %+v", path, changes)
		}
		values := strings.Join([]string{
			strings.Join(change.OldValue.([]string), " "),
			strings.Join(change.NewValue.([]string), " "),
		}, " ")
		for _, leaked := range []string{"old-command-secret", "new-command-secret", "old-api-key", "new-api-key", "old-password", "new-password"} {
			if strings.Contains(values, leaked) {
				t.Fatalf("%s leaked secret value %q in %+v", path, leaked, change)
			}
		}
		if !strings.Contains(values, "[REDACTED]") {
			t.Fatalf("%s did not redact secret-looking values: %+v", path, change)
		}
	}
}

// Named probe ports must round-trip through the diff — IntVal renders them all
// as 0, hiding edits and conflating distinct names.
func TestNormalizedProbe_NamedPorts(t *testing.T) {
	httpNamed := func(port intstr.IntOrString) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: port, Path: "/healthz"}}}
	}
	a := normalizedProbe(httpNamed(intstr.FromString("admin")))
	b := normalizedProbe(httpNamed(intstr.FromString("metrics")))
	c := normalizedProbe(httpNamed(intstr.FromInt32(9090)))
	if reflect.DeepEqual(a, b) {
		t.Errorf("distinct named ports must not normalize identically: %v", a)
	}
	if reflect.DeepEqual(a, c) {
		t.Errorf("named vs numeric port must differ: %v vs %v", a, c)
	}
}

func hasChangePath(changes []FieldChange, path string) bool {
	_, ok := findChangePath(changes, path)
	return ok
}

func findChangePath(changes []FieldChange, path string) (FieldChange, bool) {
	for _, change := range changes {
		if change.Path == path {
			return change, true
		}
	}
	return FieldChange{}, false
}

func TestDiffPodTemplateConfig_VolumesAndMounts(t *testing.T) {
	oldSpec := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
		},
		Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/config", ReadOnly: true}},
		}},
	}
	newSpec := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config-v2"}}}},
			{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
		Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/config", ReadOnly: true}, {Name: "cache", MountPath: "/cache"}},
		}},
	}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	if !hasChangePath(changes, "spec.template.spec.volumes") {
		t.Fatalf("expected volumes change, got %+v", changes)
	}
	if !hasChangePath(changes, "spec.template.spec.containers[app].volumeMounts") {
		t.Fatalf("expected volumeMounts change, got %+v", changes)
	}
	joined := strings.Join(summary, "; ")
	if !strings.Contains(joined, "volumes changed") || !strings.Contains(joined, "volumeMounts(app)") {
		t.Fatalf("summary missing volume changes: %q", joined)
	}
	// Volume diffs must carry source REFERENCES, not contents.
	for _, c := range changes {
		if c.Path == "spec.template.spec.volumes" {
			refs, ok := c.NewValue.([]string)
			if !ok {
				t.Fatalf("volumes NewValue should be []string refs, got %T", c.NewValue)
			}
			if want := "config:configMap/app-config-v2"; !slicesContains(refs, want) {
				t.Fatalf("volume refs = %v, want to include %q", refs, want)
			}
		}
	}
}

func TestDiffPodTemplateConfig_PortsSAandScheduling(t *testing.T) {
	oldSpec := corev1.PodSpec{
		ServiceAccountName: "default",
		NodeSelector:       map[string]string{"disk": "ssd"},
		Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			Ports: []corev1.ContainerPort{{ContainerPort: 8080, Name: "http"}},
		}},
	}
	newSpec := corev1.PodSpec{
		ServiceAccountName: "app-sa",
		NodeSelector:       map[string]string{"disk": "ssd", "zone": "us-east1-b"},
		Tolerations:        []corev1.Toleration{{Key: "dedicated", Value: "infra", Effect: corev1.TaintEffectNoSchedule}},
		Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			Ports: []corev1.ContainerPort{{ContainerPort: 9090, Name: "http"}},
		}},
	}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	for _, path := range []string{
		"spec.template.spec.containers[app].ports",
		"spec.template.spec.serviceAccountName",
		"spec.template.spec.nodeSelector",
		"spec.template.spec.tolerations",
	} {
		if !hasChangePath(changes, path) {
			t.Fatalf("expected change at %s, got %+v", path, changes)
		}
	}
	joined := strings.Join(summary, "; ")
	if !strings.Contains(joined, "serviceAccountName: default→app-sa") {
		t.Fatalf("summary missing serviceAccountName transition: %q", joined)
	}
}

func TestDiffPodTemplateConfig_DNS(t *testing.T) {
	oldNDots := "2"
	newNDots := "5"
	oldSpec := corev1.PodSpec{
		DNSPolicy: corev1.DNSClusterFirst,
		DNSConfig: &corev1.PodDNSConfig{
			Nameservers: []string{"10.96.0.10"},
			Searches:    []string{"default.svc.cluster.local"},
			Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: &oldNDots}, {Name: "single-request-reopen"}},
		},
	}
	newSpec := corev1.PodSpec{
		DNSPolicy: corev1.DNSNone,
		DNSConfig: &corev1.PodDNSConfig{
			Nameservers: []string{"1.1.1.1", "8.8.8.8"},
			Searches:    []string{"svc.cluster.local", "cluster.local"},
			Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: &newNDots}, {Name: "rotate"}},
		},
	}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	expected := map[string]struct {
		old any
		new any
	}{
		"spec.template.spec.dnsPolicy":             {old: "ClusterFirst", new: "None"},
		"spec.template.spec.dnsConfig.nameservers": {old: []string{"10.96.0.10"}, new: []string{"1.1.1.1", "8.8.8.8"}},
		"spec.template.spec.dnsConfig.searches":    {old: []string{"default.svc.cluster.local"}, new: []string{"svc.cluster.local", "cluster.local"}},
		"spec.template.spec.dnsConfig.options":     {old: []string{"ndots=2", "single-request-reopen"}, new: []string{"ndots=5", "rotate"}},
	}
	if len(changes) != len(expected) {
		t.Fatalf("changes = %+v, want exactly the four DNS fields", changes)
	}
	for path, want := range expected {
		change, ok := findChangePath(changes, path)
		if !ok {
			t.Fatalf("missing %s in %+v", path, changes)
		}
		if !reflect.DeepEqual(change.OldValue, want.old) || !reflect.DeepEqual(change.NewValue, want.new) {
			t.Fatalf("%s = %#v→%#v, want %#v→%#v", path, change.OldValue, change.NewValue, want.old, want.new)
		}
	}
	joined := strings.Join(summary, "; ")
	for _, fragment := range []string{"dnsPolicy: ClusterFirst→None", "dnsConfig.nameservers changed", "dnsConfig.searches changed", "dnsConfig.options changed"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("summary %q missing %q", joined, fragment)
		}
	}
}

func TestDiffPodTemplateConfig_DNSOrderRemainsObservable(t *testing.T) {
	oldSpec := corev1.PodSpec{DNSConfig: &corev1.PodDNSConfig{
		Nameservers: []string{"1.1.1.1", "8.8.8.8"},
		Searches:    []string{"svc.cluster.local", "cluster.local"},
		Options:     []corev1.PodDNSConfigOption{{Name: "rotate"}, {Name: "single-request-reopen"}},
	}}
	newSpec := corev1.PodSpec{DNSConfig: &corev1.PodDNSConfig{
		Nameservers: []string{"8.8.8.8", "1.1.1.1"},
		Searches:    []string{"cluster.local", "svc.cluster.local"},
		Options:     []corev1.PodDNSConfigOption{{Name: "single-request-reopen"}, {Name: "rotate"}},
	}}

	changes, _ := diffPodTemplateConfig(oldSpec, newSpec)
	for _, path := range []string{
		"spec.template.spec.dnsConfig.nameservers",
		"spec.template.spec.dnsConfig.searches",
		"spec.template.spec.dnsConfig.options",
	} {
		change, ok := findChangePath(changes, path)
		if !ok {
			t.Fatalf("order-only change at %s was lost: %+v", path, changes)
		}
		oldValues := change.OldValue.([]string)
		newValues := change.NewValue.([]string)
		if len(oldValues) != 2 || len(newValues) != 2 || oldValues[0] == newValues[0] {
			t.Fatalf("order-only change at %s lost ordered values: %#v→%#v", path, oldValues, newValues)
		}
	}
}

func TestDiffPodTemplateConfig_DNSDefaultsAndEmptyCollectionsAreEquivalent(t *testing.T) {
	oldSpec := corev1.PodSpec{}
	newSpec := corev1.PodSpec{
		DNSPolicy: corev1.DNSClusterFirst,
		DNSConfig: &corev1.PodDNSConfig{
			Nameservers: []string{},
			Searches:    []string{},
			Options:     []corev1.PodDNSConfigOption{},
		},
	}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	if len(changes) != 0 || len(summary) != 0 {
		t.Fatalf("API defaults and empty DNS collections produced noise: changes=%+v summary=%+v", changes, summary)
	}
}

func TestDiffApplicationCleansFailedOperationMessage(t *testing.T) {
	rawMessage := `rpc error: code = Unknown desc = app path does not exist`
	tests := []struct {
		name string
		old  *unstructured.Unstructured
		new  *unstructured.Unstructured
	}{
		{
			name: "failed operation appeared",
			old:  applicationWithOperation("", ""),
			new:  applicationWithOperation("Failed", rawMessage),
		},
		{
			name: "running operation failed",
			old:  applicationWithOperation("Running", ""),
			new:  applicationWithOperation("Failed", rawMessage),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, summary := diffApplication(tt.old, tt.new)
			joined := strings.Join(summary, "; ")
			if strings.Contains(joined, "rpc error") {
				t.Fatalf("summary leaked controller envelope: %q", joined)
			}
			if !strings.Contains(joined, "error: app path does not exist") {
				t.Fatalf("summary missing cleaned operation error: %q", joined)
			}
		})
	}
}

func applicationWithOperation(phase, message string) *unstructured.Unstructured {
	status := map[string]any{}
	if phase != "" {
		op := map[string]any{"phase": phase}
		if message != "" {
			op["message"] = message
		}
		status["operationState"] = op
	}
	return &unstructured.Unstructured{Object: map[string]any{"status": status}}
}

func TestDiffPodTemplateConfig_SecurityContextAndAffinityBooleanLevel(t *testing.T) {
	runAsNonRoot := true
	oldSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:v1"}}}
	newSpec := corev1.PodSpec{
		Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}},
		Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &runAsNonRoot},
		}},
	}

	changes, summary := diffPodTemplateConfig(oldSpec, newSpec)
	joined := strings.Join(summary, "; ")
	if !strings.Contains(joined, "securityContext(app) changed") {
		t.Fatalf("summary missing securityContext marker: %q", joined)
	}
	if !strings.Contains(joined, "affinity changed") {
		t.Fatalf("summary missing affinity marker: %q", joined)
	}
	// Boolean-level only: values must be the "changed" marker, not the structs.
	for _, c := range changes {
		if c.Path == "spec.template.spec.containers[app].securityContext" || c.Path == "spec.template.spec.affinity" {
			if c.NewValue != "changed" {
				t.Fatalf("%s NewValue = %v, want boolean-level \"changed\" marker", c.Path, c.NewValue)
			}
		}
	}
}

func slicesContains(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

func TestDiffResourceQuota_HardChanges(t *testing.T) {
	mk := func(mem string) *corev1.ResourceQuota {
		return &corev1.ResourceQuota{
			Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
				"limits.memory": resource.MustParse(mem),
				"pods":          resource.MustParse("20"),
			}},
		}
	}
	changes, summary := diffResourceQuota(mk("4Gi"), mk("1Gi"))
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the limits.memory change", changes)
	}
	if changes[0].Path != "spec.hard.limits.memory" {
		t.Fatalf("path = %q", changes[0].Path)
	}
	if joined := strings.Join(summary, "; "); !strings.Contains(joined, "quota limits.memory: 4Gi→1Gi") {
		t.Fatalf("summary = %q", joined)
	}

	// status.used churn must NOT produce a diff (would flood the timeline).
	withUsed := mk("4Gi")
	withUsed.Status.Used = corev1.ResourceList{"limits.memory": resource.MustParse("3Gi")}
	if c, _ := diffResourceQuota(mk("4Gi"), withUsed); len(c) != 0 {
		t.Fatalf("status.used-only update produced a diff: %+v", c)
	}
}

func TestDiffLimitRange_ItemChanges(t *testing.T) {
	mk := func(maxMem string) *corev1.LimitRange {
		return &corev1.LimitRange{
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				Max:  corev1.ResourceList{"memory": resource.MustParse(maxMem)},
			}}},
		}
	}
	changes, _ := diffLimitRange(mk("1Gi"), mk("512Mi"))
	if len(changes) != 1 || changes[0].Path != "spec.limits" {
		t.Fatalf("changes = %+v", changes)
	}
	if c, _ := diffLimitRange(mk("1Gi"), mk("1Gi")); len(c) != 0 {
		t.Fatalf("identical LimitRanges produced a diff: %+v", c)
	}
}

func TestDiffAdmissionWebhookConfiguration(t *testing.T) {
	// caBundle bytes are a long base64 blob; the differ must show its LENGTH,
	// never the bytes, and a rotation must register as a change.
	const caV1 = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5"         // 48 chars
	const caV2 = "WllYV1ZVVFNSUVBPTk1MS0pJSEdGRURDQkE5ODc2NTQzMjEwemFiY2Q=" // 56 chars

	mk := func(failurePolicy, ca string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "ValidatingWebhookConfiguration",
			"metadata":   map[string]any{"name": "pod-policy"},
			"webhooks": []any{map[string]any{
				"name":          "pod-policy.validation.k8s.io",
				"failurePolicy": failurePolicy,
				"sideEffects":   "None",
				"clientConfig": map[string]any{
					"caBundle": ca,
					"service": map[string]any{
						"namespace": "policy-system", "name": "pod-policy-webhook", "path": "/validate",
					},
				},
				"rules": []any{map[string]any{
					"operations": []any{"CREATE", "UPDATE"},
					"resources":  []any{"pods"},
				}},
			}},
		}}
	}

	// A caBundle rotation (the tls_mismatch fault class) registers as a change.
	changes, summary := diffAdmissionWebhookConfiguration(mk("Fail", caV1), mk("Fail", caV2))
	if len(changes) != 1 || changes[0].Path != "webhooks[pod-policy.validation.k8s.io]" {
		t.Fatalf("caBundle rotation: changes = %+v", changes)
	}
	if !strings.Contains(strings.Join(summary, "; "), "changed") {
		t.Fatalf("caBundle rotation: summary = %q", summary)
	}
	// SECURITY: the cert bytes must never appear — only a digest.
	rendered := fmt.Sprintf("%v", changes)
	if strings.Contains(rendered, caV1) || strings.Contains(rendered, caV2) {
		t.Fatalf("caBundle bytes leaked into the diff: %s", rendered)
	}
	if !strings.Contains(rendered, "caBundle=sha256:") {
		t.Fatalf("caBundle digest missing from diff: %s", rendered)
	}

	// A same-LENGTH rotation (different CA, identical base64 length) must still
	// register — a length-only signal would miss it.
	caSameLenA := strings.Repeat("A", 64)
	caSameLenB := strings.Repeat("B", 64)
	if c, _ := diffAdmissionWebhookConfiguration(mk("Fail", caSameLenA), mk("Fail", caSameLenB)); len(c) != 1 {
		t.Fatalf("same-length caBundle rotation must register a change, got %+v", c)
	}

	// failurePolicy Ignore→Fail (turns a soft webhook into a hard gate).
	if c, s := diffAdmissionWebhookConfiguration(mk("Ignore", caV1), mk("Fail", caV1)); len(c) != 1 || !strings.Contains(strings.Join(s, ";"), "changed") {
		t.Fatalf("failurePolicy flip: changes=%+v summary=%v", c, s)
	}

	// Identical configs produce no diff (no timeline spam on no-op resyncs).
	if c, _ := diffAdmissionWebhookConfiguration(mk("Fail", caV1), mk("Fail", caV1)); len(c) != 0 {
		t.Fatalf("identical webhook configs produced a diff: %+v", c)
	}

	// A webhook appearing is its own add entry.
	empty := &unstructured.Unstructured{Object: map[string]any{"webhooks": []any{}}}
	c, s := diffAdmissionWebhookConfiguration(empty, mk("Fail", caV1))
	if len(c) != 1 || !strings.Contains(strings.Join(s, ";"), "added") {
		t.Fatalf("webhook add: changes=%+v summary=%v", c, s)
	}
}

// A namespaceSelector re-scoping (match-everything → match prod) re-targets the
// webhook's blast radius and MUST register — otherwise the selector-only update
// diffs empty and recordToTimelineStore drops it silently.
func TestDiffAdmissionWebhookConfiguration_SelectorRescope(t *testing.T) {
	mk := func(withSelector bool) *unstructured.Unstructured {
		hook := map[string]any{
			"name":         "pod-policy.validation.k8s.io",
			"clientConfig": map[string]any{"caBundle": "QUJD"},
		}
		if withSelector {
			hook["namespaceSelector"] = map[string]any{
				"matchLabels": map[string]any{"env": "prod"},
			}
		}
		return &unstructured.Unstructured{Object: map[string]any{"webhooks": []any{hook}}}
	}
	changes, summary := diffAdmissionWebhookConfiguration(mk(false), mk(true))
	if len(changes) != 1 {
		t.Fatalf("selector rescope must register a change, got %+v", changes)
	}
	if rendered := fmt.Sprintf("%v", changes); !strings.Contains(rendered, "env=prod") {
		t.Fatalf("new selector missing from diff: %s", rendered)
	}
	if !strings.Contains(strings.Join(summary, ";"), "changed") {
		t.Fatalf("selector rescope summary = %v", summary)
	}
}
