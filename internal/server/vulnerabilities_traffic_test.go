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
// the wait leaves the page without the marks rather than holding it; and a
// new traffic manager (a context switch) never gets the old answer.
func TestVulnerabilityTrafficMemo(t *testing.T) {
	var memo vulnTrafficMemo
	var calls atomic.Int32
	ev := vulnerabilities.NewTrafficEvidence("caretta", time.Minute, false, nil, nil)
	active := func() (string, bool) { return "caretta", true }
	settled := func() string { return "caretta" }
	fetch := func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { calls.Add(1); return ev, true }
	mgr := &traffic.Manager{}
	for range 3 {
		if got := memo.get(context.Background(), mgr, active, settled, vulnTrafficWait, fetch); got != ev {
			t.Fatalf("got %v", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d fetches for three reads", n)
	}

	release := make(chan struct{})
	slow := func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { <-release; return ev, true }
	other := &traffic.Manager{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := memo.get(ctx, other, active, settled, vulnTrafficWait, slow); got != nil {
		t.Fatalf("a new cluster got the old answer: %v", got)
	}
	close(release)
}

// Switching the active source on the same manager drops the old source's
// answer, and a fetch of the old source that ends after the switch doesn't
// publish.
func TestVulnerabilityTrafficMemoSourceSwitch(t *testing.T) {
	var memo vulnTrafficMemo
	mgr := &traffic.Manager{}
	var source atomic.Value
	source.Store("hubble")
	active := func() (string, bool) { return source.Load().(string), true }
	settled := func() string { return source.Load().(string) }
	hubble := vulnerabilities.NewTrafficEvidence("hubble", time.Minute, true, nil, nil)
	if got := memo.get(context.Background(), mgr, active, settled, vulnTrafficWait, func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { return hubble, true }); got != hubble {
		t.Fatalf("got %v", got)
	}
	source.Store("caretta")
	release := make(chan struct{})
	late := func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { <-release; return hubble, true }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := memo.get(ctx, mgr, active, settled, vulnTrafficWait, late); got != nil {
		t.Fatalf("after the switch: %v", got)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	memo.mu.Lock()
	if memo.ev != nil {
		t.Fatalf("the old source's late answer was published: %v", memo.ev)
	}
	memo.mu.Unlock()

	// A switch while a refresh runs, before any read resets the memo: the
	// standing answer of the old source isn't given.
	var fresh vulnTrafficMemo
	source.Store("hubble")
	fresh.get(context.Background(), mgr, active, settled, vulnTrafficWait, func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { return hubble, true })
	fresh.mu.Lock()
	fresh.tried = fresh.tried.Add(-vulnTrafficFresh)
	fresh.mu.Unlock()
	hold := make(chan struct{})
	defer close(hold)
	switching := func(context.Context) (*vulnerabilities.TrafficEvidence, bool) {
		source.Store("caretta")
		<-hold
		return nil, false
	}
	short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	if got := fresh.get(short, mgr, active, settled, vulnTrafficWait, switching); got != nil {
		t.Fatalf("the old source's standing answer after a switch: %v", got)
	}
}

// A fetch that fails keeps the last good answer standing, and is asked
// again only after vulnTrafficFresh.
func TestVulnerabilityTrafficMemoKeepsAnswerOnFailure(t *testing.T) {
	var memo vulnTrafficMemo
	mgr := &traffic.Manager{}
	active := func() (string, bool) { return "beyla", true }
	settled := func() string { return "beyla" }
	ev := vulnerabilities.NewTrafficEvidence("beyla", time.Minute, false, nil, nil)
	memo.get(context.Background(), mgr, active, settled, vulnTrafficWait, func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { return ev, true })
	memo.mu.Lock()
	memo.tried = memo.tried.Add(-vulnTrafficFresh)
	memo.mu.Unlock()
	var calls atomic.Int32
	failing := func(context.Context) (*vulnerabilities.TrafficEvidence, bool) { calls.Add(1); return nil, false }
	for range 2 {
		if got := memo.get(context.Background(), mgr, active, settled, vulnTrafficWait, failing); got != ev {
			t.Fatalf("a failed fetch replaced the answer: %v", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d fetches after a failure", n)
	}

	// The manager busy detecting sources: the standing answer, at once,
	// without a fetch.
	busy := func() (string, bool) { return "", false }
	start := time.Now()
	if got := memo.get(context.Background(), mgr, busy, settled, vulnTrafficWait, failing); got != ev || time.Since(start) > time.Second {
		t.Fatalf("busy manager: %v after %s", got, time.Since(start))
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a busy manager started a fetch")
	}
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
		{traffic.Endpoint{Namespace: "external", Name: "api-1-x", Kind: traffic.EndpointKindPod, IP: "10.244.0.9", Workload: "api"}, false},
	} {
		if got := isNode(tc.e); got != tc.want {
			t.Errorf("%+v: isNode %v, want %v", tc.e, got, tc.want)
		}
	}
}
