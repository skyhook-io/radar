package traffic

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// istiodDeployment builds a Deployment shaped like the one Istio's chart
// installs: app=istiod on both the Deployment and its pod template, with
// istio.io/rev on the template when the install is revisioned.
func istiodDeployment(name, ns, rev string, ready int32, labels map[string]string) *appsv1.Deployment {
	replicas := int32(1)
	tmplLabels := map[string]string{}
	for k, v := range labels {
		tmplLabels[k] = v
	}
	if rev != "" {
		tmplLabels["istio.io/rev"] = rev
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: tmplLabels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "discovery", Image: "docker.io/istio/pilot:1.30.1"},
				}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestIstioSource_Detect(t *testing.T) {
	istiodLabels := map[string]string{"app": "istiod"}

	tests := []struct {
		name        string
		objects     []runtime.Object
		wantAvail   bool
		wantVersion string
		msgContains []string
	}{
		{
			name:        "default istiod in istio-system, ready",
			objects:     []runtime.Object{istiodDeployment("istiod", "istio-system", "default", 1, istiodLabels)},
			wantAvail:   true,
			wantVersion: "default",
			msgContains: []string{"istio-system"},
		},
		{
			name:        "revisioned istiod, ready",
			objects:     []runtime.Object{istiodDeployment("istiod-1-30-1", "istio-system", "1-30-1", 1, istiodLabels)},
			wantAvail:   true,
			wantVersion: "1-30-1",
			msgContains: []string{"istio-system"},
		},
		{
			name: "two revisions, both ready",
			objects: []runtime.Object{
				istiodDeployment("istiod-canary", "istio-system", "1-30-1", 1, istiodLabels),
				istiodDeployment("istiod-stable", "istio-system", "1-29-0", 1, istiodLabels),
			},
			wantAvail:   true,
			msgContains: []string{"2 istiod revisions running in namespace istio-system: 1-29-0, 1-30-1"},
		},
		{
			name: "one revision ready, one not",
			objects: []runtime.Object{
				istiodDeployment("istiod-1-29-0", "istio-system", "1-29-0", 0, istiodLabels),
				istiodDeployment("istiod-1-30-1", "istio-system", "1-30-1", 1, istiodLabels),
			},
			wantAvail:   true,
			wantVersion: "1-30-1",
		},
		{
			name: "unready revision in istio-system, ready one in istio",
			objects: []runtime.Object{
				istiodDeployment("istiod-1-29-0", "istio-system", "1-29-0", 0, istiodLabels),
				istiodDeployment("istiod-1-30-1", "istio", "1-30-1", 1, istiodLabels),
			},
			wantAvail:   true,
			wantVersion: "1-30-1",
			msgContains: []string{"namespace istio "},
		},
		{
			name:        "matching deployment with no ready replicas",
			objects:     []runtime.Object{istiodDeployment("istiod-1-30-1", "istio-system", "1-30-1", 0, istiodLabels)},
			wantAvail:   false,
			msgContains: []string{"found in istio-system but not ready"},
		},
		{
			name:        "no matching deployment anywhere",
			objects:     nil,
			wantAvail:   false,
			msgContains: []string{"Istio not detected"},
		},
		{
			name: "istiod-prefixed deployment without app=istiod",
			objects: []runtime.Object{
				istiodDeployment("istiod-something", "istio-system", "", 1, map[string]string{"app": "something-else"}),
			},
			wantAvail:   false,
			msgContains: []string{"Istio not detected"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewIstioSource(fake.NewSimpleClientset(tt.objects...))

			result, err := src.Detect(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Available != tt.wantAvail {
				t.Fatalf("Available = %v, want %v (message %q)", result.Available, tt.wantAvail, result.Message)
			}
			if tt.wantVersion != "" && result.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", result.Version, tt.wantVersion)
			}
			for _, s := range tt.msgContains {
				if !strings.Contains(result.Message, s) {
					t.Errorf("Message = %q, want it to contain %q", result.Message, s)
				}
			}

			for i := 0; i < 5; i++ {
				again, err := src.Detect(context.Background())
				if err != nil {
					t.Fatalf("pass %d: unexpected error: %v", i, err)
				}
				if again.Version != result.Version || again.Message != result.Message {
					t.Fatalf("pass %d chose differently: got (%q, %q), first pass (%q, %q)",
						i, again.Version, again.Message, result.Version, result.Message)
				}
			}
		})
	}
}

func TestIstioSource_Detect_ListDeniedIsNotDetected(t *testing.T) {
	client := fake.NewSimpleClientset(istiodDeployment("istiod-1-30-1", "istio-system", "1-30-1", 1, map[string]string{"app": "istiod"}))
	client.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
	})

	result, err := NewIstioSource(client).Detect(context.Background())
	if err != nil {
		t.Fatalf("a denied list should read as not detected, got error: %v", err)
	}
	if result.Available {
		t.Fatalf("Available = true with list denied (message %q)", result.Message)
	}
}
