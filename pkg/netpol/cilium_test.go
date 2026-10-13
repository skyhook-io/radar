package netpol

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCiliumSelectorEvidence(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Labels: map[string]string{
		"app": "web", "io.kubernetes.pod.namespace": "spoofed",
		"io.cilium.k8s.policy.serviceaccount": "spoofed", "io.cilium.k8s.namespace.labels.fake": "spoofed",
	}}, Spec: corev1.PodSpec{ServiceAccountName: "frontend"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"team": "platform"}}}
	expression := func(key, operator string, values ...any) map[string]any {
		return map[string]any{"matchExpressions": []any{map[string]any{"key": key, "operator": operator, "values": values}}}
	}
	for _, tc := range []struct {
		name             string
		selector         map[string]any
		namespace        *corev1.Namespace
		matches, decided bool
	}{
		{"empty", map[string]any{}, nil, true, true},
		{"source normalization", map[string]any{"matchLabels": map[string]any{"k8s:app": "web"}}, nil, true, true},
		{"source conjunction", map[string]any{"matchLabels": map[string]any{"k8s:app": "web", "any:app": "db"}}, nil, false, true},
		{"namespace", expression("k8s:io.kubernetes.pod.namespace", "In", "app"), nil, true, true},
		{"account", expression("any:io.cilium.k8s.policy.serviceaccount", "In", "frontend"), nil, true, true},
		{"namespace labels", expression("k8s:io.cilium.k8s.namespace.labels.team", "In", "platform"), namespace, true, true},
		{"namespace labels mismatch", expression("k8s:io.cilium.k8s.namespace.labels.team", "In", "other"), namespace, false, true},
		{"namespace labels unreadable", expression("k8s:io.cilium.k8s.namespace.labels.team", "In", "platform"), nil, false, false},
		{"negative unknown", expression("k8s:io.cilium.k8s.namespace.labels.team", "DoesNotExist"), nil, false, false},
		{"known absence", expression("k8s:io.cilium.k8s.namespace.labels.team", "DoesNotExist"), &corev1.Namespace{}, true, true},
		{"derived spoof removed", expression("k8s:io.cilium.k8s.namespace.labels.fake", "DoesNotExist"), namespace, true, true},
		{"cluster unknown", expression("k8s:io.cilium.k8s.policy.cluster", "NotIn", "other"), namespace, false, false},
		{"other source unknown", expression("container:app", "DoesNotExist"), namespace, false, false},
		{"ingress identity", expression("reserved:ingress", "Exists"), nil, false, true},
		{"health identity", map[string]any{"matchLabels": map[string]any{"reserved:health": ""}}, nil, false, true},
		{"host identity absent", expression("reserved:host", "DoesNotExist"), nil, true, true},
		{"world identity exclusion", expression("reserved:world", "NotIn", ""), nil, true, true},
		{"init identity unknown", expression("reserved:init", "Exists"), namespace, false, false},
		{"unmanaged identity unknown", expression("reserved:unmanaged", "DoesNotExist"), namespace, false, false},
		{"future reserved identity unknown", expression("reserved:future", "Exists"), namespace, false, false},
		{"api server identity unknown", expression("reserved:kube-apiserver", "Exists"), namespace, false, false},
		{"invalid reserved expression", expression("reserved:ingress", "In"), namespace, false, false},
		{"reserved miss precedes unreadable namespace", map[string]any{"matchLabels": map[string]any{"reserved:ingress": "", "k8s:io.cilium.k8s.namespace.labels.team": "platform"}}, nil, false, true},
		{"reserved miss despite unknown cluster", map[string]any{"matchLabels": map[string]any{"reserved:ingress": "", "k8s:io.cilium.k8s.policy.cluster": "remote"}}, nil, false, true},
		{"filtered labels unknown", expression("pod-template-hash", "DoesNotExist"), namespace, false, false},
		{"malformed map", map[string]any{"matchLabels": []any{"app"}}, namespace, false, false},
		{"malformed expression", expression("app", "In", int64(42)), namespace, false, false},
		{"invalid operator", expression("app", "INVALID", "web"), namespace, false, false},
		{"definite miss despite unknown", map[string]any{"matchLabels": map[string]any{"app": "db", "k8s:io.cilium.k8s.policy.cluster": "remote"}}, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches, decided, why := CompileCiliumSelector(tc.selector).Matches(CiliumIdentityFor(pod, tc.namespace))
			if matches != tc.matches || decided != tc.decided || (!decided && why == "") {
				t.Fatalf("got %v/%v (%s), want %v/%v", matches, decided, why, tc.matches, tc.decided)
			}
		})
	}
}

func TestCiliumReservedIdentityRequirementsStaySeparateFromPodLabels(t *testing.T) {
	id := CiliumIdentityFor(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Labels: map[string]string{"ingress": "", "health": "", "host": "", "world": ""}}}, nil)
	for _, key := range []string{"host", "world", "world-ipv4", "world-ipv6", "remote-node", "health", "ingress", "none"} {
		for _, tc := range []struct {
			op     string
			values []any
			match  bool
		}{
			{"Exists", nil, false}, {"In", []any{""}, false},
			{"DoesNotExist", nil, true}, {"NotIn", []any{""}, true},
		} {
			t.Run(key+"/"+tc.op, func(t *testing.T) {
				sel := map[string]any{"matchExpressions": []any{map[string]any{"key": "reserved:" + key, "operator": tc.op, "values": tc.values}}}
				matches, decided, why := CompileCiliumSelector(sel).Matches(id)
				if matches != tc.match || !decided || why != "" {
					t.Fatalf("got %v/%v (%s), want %v/true", matches, decided, why, tc.match)
				}
			})
		}
	}
	for _, key := range []string{"ingress", "health", "host", "world"} {
		matches, decided, why := CompileCiliumSelector(map[string]any{"matchLabels": map[string]any{"k8s:" + key: ""}}).Matches(id)
		if !matches || !decided || why != "" {
			t.Fatalf("ordinary Pod label %s lost: %v/%v (%s)", key, matches, decided, why)
		}
	}
}

func TestCiliumReservedIdentityShortcutDoesNotClaimHostNetworkMatches(t *testing.T) {
	id := CiliumIdentityFor(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: corev1.PodSpec{HostNetwork: true}}, nil)
	for _, op := range []string{"Exists", "DoesNotExist"} {
		selector := map[string]any{"matchExpressions": []any{map[string]any{"key": "reserved:host", "operator": op}}}
		matches, decided, why := CompileCiliumSelector(selector).Matches(id)
		if matches || decided || why == "" {
			t.Fatalf("host-network shortcut claimed %s: %v/%v (%s)", op, matches, decided, why)
		}
	}
}

func TestCiliumPolicySpecAlternativesAndDirection(t *testing.T) {
	id := CiliumIdentityFor(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Labels: map[string]string{"app": "web"}}}, nil)
	selected := map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "ingress": []any{}}
	unknown := map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"k8s:io.cilium.k8s.policy.cluster": "unknown"}}}
	for _, tc := range []struct {
		name                  string
		object                map[string]any
		direction             Direction
		matches, decided, all bool
	}{
		{"specs positive overrides unknown", map[string]any{"specs": []any{unknown, selected}}, DirectionIngress, true, true, false},
		{"specs direction", map[string]any{"specs": []any{selected}}, DirectionEgress, false, true, false},
		{"either direction coverage", map[string]any{"specs": []any{selected}}, "", true, true, false},
		{"node only", map[string]any{"spec": map[string]any{"nodeSelector": map[string]any{}, "ingress": []any{}}}, "", false, true, false},
		{"empty endpoint", map[string]any{"spec": map[string]any{"endpointSelector": map[string]any{}}}, "", true, true, true},
		{"malformed endpoint", map[string]any{"spec": map[string]any{"endpointSelector": "bad"}}, "", false, false, false},
		{"malformed specs", map[string]any{"specs": []any{"bad"}}, "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := CompileCiliumPolicy(&unstructured.Unstructured{Object: tc.object})
			matches, decided, _ := m.Selects(id, tc.direction)
			if matches != tc.matches || decided != tc.decided || m.SelectsAll() != tc.all {
				t.Fatalf("got %v/%v all=%v", matches, decided, m.SelectsAll())
			}
		})
	}
}

func TestCiliumDefaultExclusionsAreAnchoredPrefixes(t *testing.T) {
	for _, key := range []string{"pod-template-hash", "pod-template-hash-custom", "controller-uid-custom", "batch.kubernetes.io/controller-uid-custom"} {
		if !ciliumFilteredIdentityKey(key) {
			t.Errorf("default filter must exclude prefix %q", key)
		}
	}
	if ciliumFilteredIdentityKey("my-pod-template-hash") {
		t.Fatal("filter must stay anchored")
	}
}
