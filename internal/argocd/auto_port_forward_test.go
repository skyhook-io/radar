package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward/forwardtest"
	"k8s.io/client-go/kubernetes"

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
	m.portForward = func(context.Context, probeSnapshot, string, string, int) (activeForward, error) {
		t.Fatal("discovery opened a port-forward while automatic port-forwarding is turned off")
		return nil, nil
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-server", Labels: map[string]string{"app.kubernetes.io/name": "argocd-server"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
	}

	_, fwd, err := m.discover(context.Background(), probeSnapshot{probeCluster: probeCluster{k8sClient: fake.NewSimpleClientset(svc)}})
	if fwd != nil || !errors.Is(err, portforward.ErrAutoDisabled) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("discover = %v, %v; want ErrUnreachable wrapping ErrAutoDisabled", fwd, err)
	}
	// The direct connection is the only lane left and may come back (a VPN
	// coming up), so the failure stays one that is retried.
	if !transientConnectFailure(err) {
		t.Fatal("a failed direct connection must stay retryable when port-forwarding is off")
	}
}

func TestArgoForwardFollowsConnectionLifecycle(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			arrived, ended, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			cfg := forwardtest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				if (r.Method == http.MethodPost || pending) && first.CompareAndSwap(false, true) {
					close(arrived)
					defer close(ended)
				}
				if pending {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				forwardtest.ServeTunnel(w, r)
			})
			defer close(release)
			if _, err := k8s.InstallTestServingConnection("ctx", cfg, nil, fake.NewSimpleClientset().Discovery()); err != nil {
				t.Fatal(err)
			}
			k8s.SetTestClient(kubernetes.NewForConfigOrDie(cfg))
			m := NewManager()
			snap := m.snapshotLocked()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				forward activeForward
				err     error
			}
			done := make(chan result, 1)
			go func() {
				fwd, err := m.startPortForward(ctx, snap, "ns", "svc", 8080)
				if fwd != nil {
					t.Cleanup(func() { fwd.Stop() })
				}
				done <- result{fwd, err}
			}()
			select {
			case <-arrived:
			case got := <-done:
				t.Fatalf("upgrade never arrived: %v", got.err)
			}
			if !pending {
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				k8s.RetireServingConnection()
				k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "ctx"})
				regular, err := k8s.Current().PortForwards().Start(ctx, portforward.OwnerArgoCD, "ns", "svc", 8080)
				if err != nil {
					t.Fatal(err)
				}
				if regular.Info().LocalPort == got.forward.Info().LocalPort {
					t.Error("regular Start reused the Argo CD tunnel")
				}
				regular.Stop()
				if !got.forward.Alive() {
					t.Fatal("readmission did not preserve the live Argo CD handle")
				}
				if contextName := got.forward.Info().ContextName; contextName != "ctx" {
					t.Fatalf("forward context = %q, want ctx", contextName)
				}
				select {
				case <-ended:
					t.Fatal("readmission closed the tunnel")
				default:
				}
			}
			k8s.ResetAllSubsystems()
			select {
			case <-ended:
			case <-time.After(time.Second):
				t.Fatal("rescope left the upgrade open")
			}
			if pending {
				if got := <-done; got.err == nil {
					t.Fatal("retired dial succeeded")
				}
			}
		})
	}
}

func TestDiscoveryOnRescopedConnectionIsSuperseded(t *testing.T) {
	m := newTestManager(config.Config{})
	m.inCluster = func() bool { return false }
	manager := portforward.NewManager(nil, nil, "ctx")
	manager.Close()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "svc", Labels: map[string]string{"app.kubernetes.io/name": "argocd-server"}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}}
	_, _, err := m.discover(t.Context(), probeSnapshot{probeCluster: probeCluster{k8sClient: fake.NewSimpleClientset(svc), forwards: manager}})
	if !errors.Is(err, errStaleProbe) {
		t.Fatalf("retired discovery returned %v; want superseded", err)
	}
}
