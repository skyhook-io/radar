package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
)

// One fetch answers every read for vulnTrafficFresh; a fetch slower than
// vulnTrafficWait leaves the page without the marks rather than holding it;
// and a new traffic manager (a context switch) never gets the old answer.
func TestVulnerabilityTrafficMemo(t *testing.T) {
	var memo vulnTrafficMemo
	var calls atomic.Int32
	ev := vulnerabilities.NewTrafficEvidence("caretta", time.Minute, false, nil, nil)
	fetch := func(context.Context) *vulnerabilities.TrafficEvidence { calls.Add(1); return ev }
	mgr := &traffic.Manager{}
	for range 3 {
		if got := memo.get(context.Background(), mgr, fetch); got != ev {
			t.Fatalf("got %v", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d fetches for three reads", n)
	}

	release := make(chan struct{})
	slow := func(context.Context) *vulnerabilities.TrafficEvidence { <-release; return ev }
	other := &traffic.Manager{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := memo.get(ctx, other, slow); got != nil {
		t.Fatalf("a new cluster got the old answer: %v", got)
	}
	close(release)
}

// A node is a node however a source names it: by name, by an address the
// Node lists or its CNI recorded, or by its gateway on the Pod network,
// which Caretta reports as an outside address (kubelet probes on kind come
// from 10.244.0.1). An outside address and a Pod in a namespace are not.
func TestNodeMatcher(t *testing.T) {
	isNode := nodeMatcher([]*corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{Name: "vuln-review-control-plane", Annotations: map[string]string{"projectcalico.org/IPv4VXLANTunnelAddr": "10.250.3.0"}},
		Spec:       corev1.NodeSpec{PodCIDR: "10.244.0.0/24", PodCIDRs: []string{"10.244.0.0/24"}},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.24.0.6"}}},
	}})
	for _, tc := range []struct {
		e    traffic.Endpoint
		want bool
	}{
		{traffic.Endpoint{Namespace: "node", Name: "vuln-review-control-plane", Kind: "node"}, true},
		{traffic.Endpoint{Name: "172.24.0.6", Kind: traffic.EndpointKindExternal}, true},
		{traffic.Endpoint{Namespace: "external", Name: "10.244.0.1", Kind: "external"}, true},
		{traffic.Endpoint{Namespace: "external", Name: "10.250.3.0", Kind: "external"}, true},
		{traffic.Endpoint{Name: "world", Kind: traffic.EndpointKindExternal, IP: "10.244.0.1"}, true},
		{traffic.Endpoint{Namespace: "external", Name: "203.0.113.9", Kind: "external"}, false},
		{traffic.Endpoint{Namespace: "shop", Name: "10.244.0.7", Kind: traffic.EndpointKindPod}, false},
	} {
		if got := isNode(tc.e); got != tc.want {
			t.Errorf("%+v: isNode %v, want %v", tc.e, got, tc.want)
		}
	}
}
