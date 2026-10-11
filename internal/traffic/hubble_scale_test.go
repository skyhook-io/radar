package traffic

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// statusObserver reports a connected-node count, as Relay does.
type statusObserver struct {
	scriptedObserver
	nodes uint32
}

func (s *statusObserver) ServerStatus(context.Context, *observerpb.ServerStatusRequest) (*observerpb.ServerStatusResponse, error) {
	return &observerpb.ServerStatusResponse{NumConnectedNodes: wrapperspb.UInt32(s.nodes)}, nil
}

func TestHubbleFlowsRequest_PushesNamespacesAndExclusions(t *testing.T) {
	req := hubbleFlowsRequest(FlowOptions{
		Namespaces:        []string{"shop", "pay"},
		ExcludeNamespaces: []string{"kube-system"},
		ExcludeHost:       true,
	}, false)

	wl := req.GetWhitelist()
	want := []string{"shop/", "pay/"}
	if len(wl) != 2 || !slices.Equal(wl[0].GetSourcePod(), want) || !slices.Equal(wl[1].GetDestinationPod(), want) {
		t.Errorf("whitelist = %v, want every namespace as source OR destination", wl)
	}

	bl := req.GetBlacklist()
	if len(bl) != 6 {
		t.Fatalf("blacklist = %v, want the two fixed entries plus source/destination for namespaces and host", bl)
	}
	if !slices.Equal(bl[2].GetSourcePod(), []string{"kube-system/"}) || !slices.Equal(bl[3].GetDestinationPod(), []string{"kube-system/"}) {
		t.Errorf("namespace exclusion = %v / %v, want either endpoint in kube-system dropped", bl[2], bl[3])
	}
	if !slices.Equal(bl[4].GetSourceLabel(), hubbleHostLabels) || !slices.Equal(bl[5].GetDestinationLabel(), hubbleHostLabels) {
		t.Errorf("host exclusion = %v / %v, want either endpoint with a host identity dropped", bl[4], bl[5])
	}
}

func TestHubbleMatchWhitelist(t *testing.T) {
	pod := func(ns, name string) EndpointRef {
		return EndpointRef{Namespace: ns, Name: name, Kind: EndpointKindPod}
	}

	t.Run("a pod pair is sent both ways round", func(t *testing.T) {
		f := hubbleMatchWhitelist(&FlowMatch{Pairs: []EndpointPair{{Source: pod("a", "web-1"), Destination: pod("b", "db-0")}}})
		if len(f) != 2 ||
			!slices.Equal(f[0].GetSourcePod(), []string{"a/web-1"}) || !slices.Equal(f[0].GetDestinationPod(), []string{"b/db-0"}) ||
			!slices.Equal(f[1].GetSourcePod(), []string{"b/db-0"}) || !slices.Equal(f[1].GetDestinationPod(), []string{"a/web-1"}) {
			t.Errorf("filters = %v, want caller→callee and the reverse, since replies arrive callee→caller", f)
		}
	})

	t.Run("each pair carries its port, on the source side when reversed", func(t *testing.T) {
		f := hubbleMatchWhitelist(&FlowMatch{Pairs: []EndpointPair{
			{Source: pod("a", "web-1"), Destination: pod("b", "db-0"), Port: 5432},
			{Source: pod("a", "web-1"), Destination: pod("b", "cache-0"), Port: 6379},
		}})
		if len(f) != 4 {
			t.Fatalf("got %d filters, want one forward and one reversed per pair", len(f))
		}
		if !slices.Equal(f[0].GetDestinationPort(), []string{"5432"}) || !slices.Equal(f[1].GetSourcePort(), []string{"5432"}) ||
			!slices.Equal(f[2].GetDestinationPod(), []string{"b/cache-0"}) || !slices.Equal(f[2].GetDestinationPort(), []string{"6379"}) {
			t.Errorf("filters = %v, want each pair with its own port, so web-1→cache-0 traffic cannot crowd out web-1→db-0", f)
		}
	})

	t.Run("a side that is not all pods is left open", func(t *testing.T) {
		f := hubbleMatchWhitelist(&FlowMatch{Pairs: []EndpointPair{{Source: pod("a", "web-1"), Destination: EndpointRef{Name: "world", Kind: EndpointKindExternal}}}})
		if len(f) != 2 || len(f[0].GetDestinationPod()) != 0 || !slices.Equal(f[0].GetSourcePod(), []string{"a/web-1"}) {
			t.Errorf("filters = %v, want only the pod side constrained", f)
		}
	})

	t.Run("nothing Hubble can name means no pushdown", func(t *testing.T) {
		host := EndpointRef{Name: "host", Kind: EndpointKindHost}
		if f := hubbleMatchWhitelist(&FlowMatch{Pairs: []EndpointPair{{Source: host, Destination: host}}}); f != nil {
			t.Errorf("filters = %v, want nil", f)
		}
	})

	t.Run("too many pods are filtered here instead", func(t *testing.T) {
		var pairs []EndpointPair
		for i := range hubbleMaxMatchPods + 1 {
			pairs = append(pairs, EndpointPair{Source: pod("a", fmt.Sprintf("p-%d", i)), Destination: pod("b", fmt.Sprintf("q-%d", i))})
		}
		if f := hubbleMatchWhitelist(&FlowMatch{Pairs: pairs}); f != nil {
			t.Errorf("got %d filters, want none above %d pods", len(f), hubbleMaxMatchPods)
		}
	})

	t.Run("an endpoint is sent as its pods on either side", func(t *testing.T) {
		gw := EndpointRef{Namespace: "edge", Name: "gateway", Kind: EndpointKindWorkload, WorkloadKind: "Deployment"}
		f := hubbleMatchWhitelist(&FlowMatch{Endpoints: []EndpointRef{gw}})
		if len(f) != 2 || !slices.Equal(f[0].GetSourcePod(), []string{"edge/gateway-"}) || len(f[0].GetDestinationPod()) != 0 ||
			!slices.Equal(f[1].GetDestinationPod(), []string{"edge/gateway-"}) || len(f[1].GetSourcePod()) != 0 {
			t.Errorf("filters = %v, want the workload's pods as source or as destination", f)
		}
		if f := hubbleMatchWhitelist(&FlowMatch{Endpoints: []EndpointRef{gw, {Name: "world", Kind: EndpointKindExternal}}}); f != nil {
			t.Errorf("filters = %v, want none when one endpoint cannot be named: it would be dropped at the node", f)
		}
	})

	t.Run("a selection inside the view replaces the namespace whitelist", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Namespaces: []string{"a"}, Match: &FlowMatch{Pairs: []EndpointPair{{Source: pod("a", "web-1"), Destination: pod("a", "db-0")}}}}, false)
		if wl := req.GetWhitelist(); len(wl) != 2 || !slices.Equal(wl[0].GetSourcePod(), []string{"a/web-1"}) {
			t.Errorf("whitelist = %v, want the selection's pods", wl)
		}
		world := EndpointRef{Name: "world", Kind: EndpointKindExternal}
		req = hubbleFlowsRequest(FlowOptions{Namespaces: []string{"a"}, Match: &FlowMatch{Pairs: []EndpointPair{{Source: world, Destination: world}}}}, false)
		if wl := req.GetWhitelist(); len(wl) != 2 || !slices.Equal(wl[0].GetSourcePod(), []string{"a/"}) {
			t.Errorf("whitelist = %v, want the namespaces when the selection cannot be pushed", wl)
		}
	})
}

func TestHubbleNodeLimit(t *testing.T) {
	for _, tc := range []struct {
		perNode uint64
		nodes   int
		want    uint64
	}{
		{1000, 0, 1000},  // unknown: keep the caller's limit
		{1000, 10, 1000}, // small cluster: unchanged
		{1000, 50, 1000},
		{1000, 200, 250},
		{1000, 2000, hubbleMinNodeLimit}, // floor; the receive-time cap holds the total
		{300, 10, 300},                   // never above the caller's limit
	} {
		if got := hubbleNodeLimit(tc.perNode, tc.nodes); got != tc.want {
			t.Errorf("hubbleNodeLimit(%d, %d) = %d, want %d", tc.perNode, tc.nodes, got, tc.want)
		}
	}
}

func TestHubbleGetFlows_SplitsTheBudgetAcrossNodes(t *testing.T) {
	obs := &statusObserver{nodes: 200, scriptedObserver: scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}}}
	h := connectedHubble(t, obs)
	if _, err := h.GetFlows(context.Background(), DefaultFlowOptions()); err != nil {
		t.Fatal(err)
	}
	if got := obs.got.GetNumber(); got != 250 {
		t.Errorf("Number = %d, want the budget split over 200 nodes", got)
	}
}

// failingStatusObserver is a Relay waiting on an unreachable node: its
// ServerStatus fails while flows from the other nodes still arrive.
type failingStatusObserver struct {
	scriptedObserver
}

func (s *failingStatusObserver) ServerStatus(context.Context, *observerpb.ServerStatusRequest) (*observerpb.ServerStatusResponse, error) {
	return nil, status.Error(codes.DeadlineExceeded, "a node did not answer")
}

func TestHubbleGetFlows_SplitsANarrowedViewOverEveryNode(t *testing.T) {
	// A namespace's flows are recorded on its peers' nodes too, so only the
	// cluster's node count bounds what a narrowed fetch transfers.
	for name, opts := range map[string]FlowOptions{
		"namespaces": {Namespaces: []string{"a"}},
		"selection":  {Namespaces: []string{"a"}, Match: &FlowMatch{Endpoints: []EndpointRef{{Namespace: "a", Name: "web-1", Kind: EndpointKindPod}}}},
	} {
		t.Run(name, func(t *testing.T) {
			obs := &statusObserver{nodes: 200, scriptedObserver: scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}}}
			h := connectedHubble(t, obs)
			if _, err := h.GetFlows(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if got := obs.got.GetNumber(); got != 250 {
				t.Errorf("Number = %d, want the budget split over all 200 nodes", got)
			}
		})
	}
}

func TestHubbleGetFlows_KeepsTheBudgetSplitWhenStatusFails(t *testing.T) {
	t.Run("the last count is kept", func(t *testing.T) {
		obs := &failingStatusObserver{scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}}}
		h := connectedHubble(t, obs)
		h.nodeCount, h.nodeCountAt = 200, time.Now().Add(-2*hubbleNodeCountTTL)
		if _, err := h.GetFlows(context.Background(), DefaultFlowOptions()); err != nil {
			t.Fatal(err)
		}
		if got := obs.got.GetNumber(); got != 250 {
			t.Errorf("Number = %d, want the budget still split over the 200 nodes last reported", got)
		}
	})

	// The API server answers a one-item page with the count it left out.
	pagedNodes := func(t *testing.T, remaining *int64, cont string) *fake.Clientset {
		client := fake.NewSimpleClientset()
		client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if limit := action.(k8stesting.ListActionImpl).GetListOptions().Limit; limit != 1 {
				t.Errorf("node list Limit = %d, want a one-item page", limit)
			}
			return true, &corev1.NodeList{
				ListMeta: metav1.ListMeta{RemainingItemCount: remaining, Continue: cont},
				Items:    []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}}},
			}, nil
		})
		return client
	}

	t.Run("the cluster's nodes are counted, and win over a stale count", func(t *testing.T) {
		obs := &failingStatusObserver{scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}}}
		h := connectedHubble(t, obs)
		h.nodeCount, h.nodeCountAt = 20, time.Now().Add(-2*hubbleNodeCountTTL)
		h.k8sClient = pagedNodes(t, ptr.To[int64](99), "more")
		if _, err := h.GetFlows(context.Background(), DefaultFlowOptions()); err != nil {
			t.Fatal(err)
		}
		if got := obs.got.GetNumber(); got != 500 {
			t.Errorf("Number = %d, want the budget split over the cluster's 100 nodes", got)
		}
	})

	t.Run("a page without a remaining count is not a count", func(t *testing.T) {
		obs := &failingStatusObserver{scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}}}
		h := connectedHubble(t, obs)
		h.nodeCount, h.nodeCountAt = 200, time.Now().Add(-2*hubbleNodeCountTTL)
		h.k8sClient = pagedNodes(t, nil, "more")
		if _, err := h.GetFlows(context.Background(), DefaultFlowOptions()); err != nil {
			t.Fatal(err)
		}
		if got := obs.got.GetNumber(); got != 250 {
			t.Errorf("Number = %d, want the last count (200 nodes) kept, not a count of 1", got)
		}
	})
}

func TestConnectedNodesStoresNothingForACancelledRequest(t *testing.T) {
	h := connectedHubble(t, &failingStatusObserver{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.connectedNodes(ctx, h.observerClient)
	if !h.nodeCountAt.IsZero() {
		t.Errorf("stored a count of %d: a cancelled request's lookups would set every user's split for the next minute", h.nodeCount)
	}
}

func TestHubbleGetFlows_KeepsTheNewestFlowsWithinTheBudget(t *testing.T) {
	old := hubbleFlowBudget
	hubbleFlowBudget = 3
	t.Cleanup(func() { hubbleFlowBudget = old })

	now := time.Now()
	var responses []*observerpb.GetFlowsResponse
	for i := range 8 {
		responses = append(responses, timedFlow(fmt.Sprintf("n%d", i), now.Add(-time.Duration(8-i)*time.Second)))
	}
	h := connectedHubble(t, &scriptedObserver{responses: responses})
	resp, err := h.GetFlows(context.Background(), FlowOptions{Since: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 3 {
		t.Fatalf("got %d flows, want the budget of 3", len(resp.Flows))
	}
	for _, f := range resp.Flows {
		if f.LastSeen.Before(now.Add(-3 * time.Second)) {
			t.Errorf("kept a flow from %v; only the three newest should remain", now.Sub(f.LastSeen))
		}
	}
	if resp.FlowLimit != 3 {
		t.Errorf("FlowLimit = %d, want 3 so the view can say the total cap applied", resp.FlowLimit)
	}
	if resp.CoveredSince == nil || !resp.CoveredSince.Equal(now.Add(-3*time.Second)) {
		t.Errorf("CoveredSince = %v, want the oldest kept flow (%v)", resp.CoveredSince, now.Add(-3*time.Second))
	}
}

func TestHubbleGetFlows_UnderTheBudgetNothingChanges(t *testing.T) {
	h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b"), flowResponse("a", "c")}})
	resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 2 || resp.FlowLimit != 0 || resp.CoveredSince != nil {
		t.Errorf("flows=%d flowLimit=%d coveredSince=%v, want all flows and no cap reported", len(resp.Flows), resp.FlowLimit, resp.CoveredSince)
	}
}

func TestHubblePodPrefix(t *testing.T) {
	wl := func(name, kind string) EndpointRef {
		return EndpointRef{Namespace: "shop", Name: name, Kind: EndpointKindWorkload, WorkloadKind: kind}
	}
	long := strings.Repeat("a", 50)
	for _, tc := range []struct {
		ref    EndpointRef
		want   string
		pushed bool
	}{
		{EndpointRef{Namespace: "shop", Name: "web-1", Kind: EndpointKindPod}, "shop/web-1", true},
		{wl("web", "Deployment"), "shop/web-", true},
		{wl("db", "StatefulSet"), "shop/db-", true},
		{wl(long, "Deployment"), "shop/" + long[:hubbleWorkloadPrefixLen], true}, // generated names are cut; never claim the dash
		{wl("pg", "Cluster"), "", false},                                         // a CRD may name its pods any way it likes
		{EndpointRef{Name: "world", Kind: EndpointKindExternal}, "", false},
	} {
		got, ok := hubblePodPrefix(tc.ref)
		if got != tc.want || ok != tc.pushed {
			t.Errorf("hubblePodPrefix(%+v) = %q, %v; want %q, %v", tc.ref, got, ok, tc.want, tc.pushed)
		}
	}
}

func TestConvertEndpointTakesCiliumsWorkload(t *testing.T) {
	ep := convertEndpoint(&flowpb.Endpoint{
		Namespace: "shop", PodName: "web-7d9f-x2k4q",
		Labels:    []string{"k8s:app=frontend"},
		Workloads: []*flowpb.Workload{{Name: "web", Kind: "Deployment"}},
	}, "10.0.0.1")
	if ep.Workload != "web" || ep.WorkloadKind != "Deployment" {
		t.Errorf("workload = %q/%q, want Cilium's owner, not a label", ep.Workload, ep.WorkloadKind)
	}
	bare := convertEndpoint(&flowpb.Endpoint{Namespace: "shop", PodName: "debug", Labels: []string{"k8s:app=frontend"}}, "")
	if bare.Workload != "" {
		t.Errorf("workload = %q, want none: an app label is not an owner, and two Deployments can share one", bare.Workload)
	}
}

func TestConvertEndpointNamesARolloutCiliumStoppedAt(t *testing.T) {
	rs := func(name string, labels ...string) Endpoint {
		return convertEndpoint(&flowpb.Endpoint{
			Namespace: "shop", PodName: name + "-x2k4q", Labels: labels,
			Workloads: []*flowpb.Workload{{Name: name, Kind: "ReplicaSet"}},
		}, "")
	}
	if ep := rs("checkout-6b8f9c", "k8s:app=checkout", "k8s:rollouts-pod-template-hash=6b8f9c"); ep.Workload != "checkout" || ep.WorkloadKind != "Rollout" {
		t.Errorf("workload = %q/%q, want the Rollout checkout", ep.Workload, ep.WorkloadKind)
	}
	if ep := rs("batch-6b8f9c", "k8s:app=batch"); ep.Workload != "batch-6b8f9c" || ep.WorkloadKind != "ReplicaSet" {
		t.Errorf("workload = %q/%q, want a ReplicaSet without the Rollout label left as Cilium named it", ep.Workload, ep.WorkloadKind)
	}
	if ep := rs("other", "k8s:rollouts-pod-template-hash=6b8f9c"); ep.Workload != "other" || ep.WorkloadKind != "ReplicaSet" {
		t.Errorf("workload = %q/%q, want a name not ending in the hash left alone", ep.Workload, ep.WorkloadKind)
	}
}

func TestHubbleFlowsRequest_TiesASelectionToTheNamespacesInView(t *testing.T) {
	pod := func(ns, name string) EndpointRef {
		return EndpointRef{Namespace: ns, Name: name, Kind: EndpointKindPod}
	}
	view := []string{"shop"}

	t.Run("an endpoint outside the view keeps only its traffic with the view", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Namespaces: view, Match: &FlowMatch{Endpoints: []EndpointRef{pod("kube-system", "coredns-1")}}}, false)
		wl := req.GetWhitelist()
		if len(wl) != 2 ||
			!slices.Equal(wl[0].GetSourcePod(), []string{"kube-system/coredns-1"}) || !slices.Equal(wl[0].GetDestinationPod(), []string{"shop/"}) ||
			!slices.Equal(wl[1].GetSourcePod(), []string{"shop/"}) || !slices.Equal(wl[1].GetDestinationPod(), []string{"kube-system/coredns-1"}) {
			t.Errorf("whitelist = %v, want coredns paired with the view's namespace on the other end", wl)
		}
	})

	t.Run("an endpoint inside the view is sent as it is", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Namespaces: view, Match: &FlowMatch{Endpoints: []EndpointRef{pod("shop", "web-1")}}}, false)
		wl := req.GetWhitelist()
		if len(wl) != 2 || !slices.Equal(wl[0].GetSourcePod(), []string{"shop/web-1"}) || len(wl[0].GetDestinationPod()) != 0 {
			t.Errorf("whitelist = %v, want the pod on either side, unchanged", wl)
		}
	})

	t.Run("a pair keeps its port, and a pair wholly outside is dropped", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Namespaces: view, Match: &FlowMatch{Pairs: []EndpointPair{
			{Source: pod("shop", "web-1"), Destination: pod("pay", "api-1"), Port: 8080},
			{Source: pod("pay", "api-1"), Destination: pod("pay", "db-0"), Port: 5432},
		}}}, false)
		wl := req.GetWhitelist()
		if len(wl) != 2 || !slices.Equal(wl[0].GetDestinationPort(), []string{"8080"}) || !slices.Equal(wl[1].GetSourcePort(), []string{"8080"}) {
			t.Errorf("whitelist = %v, want only the shop pair, both ways, with its port", wl)
		}
	})

	t.Run("a selection wholly outside falls back to the namespaces", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Namespaces: view, Match: &FlowMatch{Pairs: []EndpointPair{
			{Source: pod("pay", "api-1"), Destination: pod("pay", "db-0")},
		}}}, false)
		wl := req.GetWhitelist()
		if len(wl) != 2 || !slices.Equal(wl[0].GetSourcePod(), []string{"shop/"}) || !slices.Equal(wl[1].GetDestinationPod(), []string{"shop/"}) {
			t.Errorf("whitelist = %v, want the view's namespaces", wl)
		}
	})

	t.Run("a long namespace list falls back to the namespaces", func(t *testing.T) {
		many := make([]string, 300)
		for i := range many {
			many[i] = fmt.Sprintf("team-%d", i)
		}
		var outside []EndpointRef
		for i := range 20 {
			outside = append(outside, pod("kube-system", fmt.Sprintf("agent-%d", i)))
		}
		req := hubbleFlowsRequest(FlowOptions{Namespaces: many, Match: &FlowMatch{Endpoints: outside}}, false)
		if wl := req.GetWhitelist(); len(wl) != 2 || len(wl[0].GetSourcePod()) != 300 || len(wl[0].GetDestinationPod()) != 0 {
			t.Errorf("whitelist has %d filters, want the 300 namespaces on either side instead of 40 filters with 300 prefixes each", len(wl))
		}
	})

	t.Run("pairs inside a long namespace list are sent as they are", func(t *testing.T) {
		many := make([]string, 25)
		for i := range many {
			many[i] = fmt.Sprintf("team-%d", i)
		}
		var pairs []EndpointPair
		for i := range 45 {
			pairs = append(pairs, EndpointPair{Source: pod("team-1", fmt.Sprintf("web-%d", i)), Destination: pod("team-2", "db-0"), Port: 5432})
		}
		req := hubbleFlowsRequest(FlowOptions{Namespaces: many, Match: &FlowMatch{Pairs: pairs}}, false)
		if wl := req.GetWhitelist(); len(wl) != 90 {
			t.Errorf("whitelist has %d filters, want the 45 pairs both ways: nothing needs tying", len(wl))
		}
	})

	t.Run("with every namespace in view the selection is sent as it is", func(t *testing.T) {
		req := hubbleFlowsRequest(FlowOptions{Match: &FlowMatch{Endpoints: []EndpointRef{pod("kube-system", "coredns-1")}}}, false)
		if wl := req.GetWhitelist(); len(wl) != 2 || len(wl[0].GetDestinationPod()) != 0 {
			t.Errorf("whitelist = %v, want coredns on either side, unconstrained", wl)
		}
	})
}
