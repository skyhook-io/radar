package argocd

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/portforward"
)

func TestDiscoverSkipsPortForwardWhenAutoDisabled(t *testing.T) {
	portforward.SetAutoDisabled(true)
	t.Cleanup(func() { portforward.SetAutoDisabled(false) })

	m := newTestManager(config.Config{})
	m.inCluster = func() bool { return false }
	m.portForward = func(context.Context, probeSnapshot, string, string, int) (*activeForward, error) {
		t.Fatal("discovery opened a port-forward while automatic port-forwarding is turned off")
		return nil, nil
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-server", Labels: map[string]string{"app.kubernetes.io/name": "argocd-server"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
	}

	_, fwd, err := m.discover(context.Background(), probeSnapshot{k8sClient: fake.NewSimpleClientset(svc)})
	if fwd != nil || !errors.Is(err, portforward.ErrAutoDisabled) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("discover = %v, %v; want ErrUnreachable wrapping ErrAutoDisabled", fwd, err)
	}
	// The direct connection is the only lane left and may come back (a VPN
	// coming up), so the failure stays one that is retried.
	if !transientConnectFailure(err) {
		t.Fatal("a failed direct connection must stay retryable when port-forwarding is off")
	}
}
