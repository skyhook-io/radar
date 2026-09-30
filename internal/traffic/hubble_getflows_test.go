package traffic

import (
	"context"
	"net"
	"strings"
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	relaypb "github.com/cilium/cilium/api/v1/relay"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// scriptedObserver replays a fixed GetFlows stream, optionally ending in an error.
type scriptedObserver struct {
	observerpb.UnimplementedObserverServer
	responses []*observerpb.GetFlowsResponse
	endErr    error
}

func (s *scriptedObserver) GetFlows(_ *observerpb.GetFlowsRequest, stream grpc.ServerStreamingServer[observerpb.GetFlowsResponse]) error {
	for _, r := range s.responses {
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return s.endErr
}

func connectedHubble(t *testing.T, obs *scriptedObserver) *HubbleSource {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	observerpb.RegisterObserverServer(srv, obs)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	h := NewHubbleSource(fake.NewSimpleClientset())
	h.observerClient = observerpb.NewObserverClient(conn)
	h.isConnected = true
	return h
}

func flowResponse(src, dst string) *observerpb.GetFlowsResponse {
	return &observerpb.GetFlowsResponse{ResponseTypes: &observerpb.GetFlowsResponse_Flow{Flow: &flowpb.Flow{
		Source:      &flowpb.Endpoint{Namespace: "demo", PodName: src},
		Destination: &flowpb.Endpoint{Namespace: "demo", PodName: dst},
		L4:          &flowpb.Layer4{Protocol: &flowpb.Layer4_TCP{TCP: &flowpb.TCP{SourcePort: 40000, DestinationPort: 80}}},
		IsReply:     wrapperspb.Bool(false),
		Verdict:     flowpb.Verdict_FORWARDED,
	}}}
}

func TestHubbleGetFlows_ReportsWhatTheStreamDidNotDeliver(t *testing.T) {
	t.Run("a complete stream carries no warning", func(t *testing.T) {
		h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b")}})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Flows) != 1 || resp.Warning != "" {
			t.Fatalf("got %d flows, warning %q", len(resp.Flows), resp.Warning)
		}
	})

	t.Run("a stream cut short keeps its flows and says so", func(t *testing.T) {
		h := connectedHubble(t, &scriptedObserver{
			responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b"), flowResponse("a", "c")},
			endErr:    status.Error(codes.Unavailable, "relay lost its peer"),
		})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Flows) != 2 {
			t.Fatalf("the flows that did arrive must be kept, got %d", len(resp.Flows))
		}
		if !strings.Contains(resp.Warning, "ended early") || !strings.Contains(resp.Warning, "first 2") {
			t.Errorf("warning = %q, want it to say the stream ended early after 2 flows", resp.Warning)
		}
		if resp.WarningKind != WarningIncomplete {
			t.Errorf("warningKind = %q, want incomplete: the fetch worked but could not see everything", resp.WarningKind)
		}
	})

	t.Run("lost events are reported", func(t *testing.T) {
		h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{
			flowResponse("a", "b"),
			{ResponseTypes: &observerpb.GetFlowsResponse_LostEvents{LostEvents: &flowpb.LostEvent{
				Source: flowpb.LostEventSource_HUBBLE_RING_BUFFER, NumEventsLost: 42,
			}}},
			{ResponseTypes: &observerpb.GetFlowsResponse_LostEvents{LostEvents: &flowpb.LostEvent{NumEventsLost: 8}}},
		}})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Warning, "50 events lost") {
			t.Errorf("warning = %q, want the total of lost events", resp.Warning)
		}
	})

	t.Run("everything lost still says so", func(t *testing.T) {
		h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{
			{ResponseTypes: &observerpb.GetFlowsResponse_LostEvents{LostEvents: &flowpb.LostEvent{NumEventsLost: 3}}},
		}})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Warning, "3 events lost") {
			t.Errorf("warning = %q: an empty result whose events were lost must not read as idle", resp.Warning)
		}
	})

	t.Run("unreachable nodes are reported", func(t *testing.T) {
		status := func(state relaypb.NodeState, nodes ...string) *observerpb.GetFlowsResponse {
			return &observerpb.GetFlowsResponse{ResponseTypes: &observerpb.GetFlowsResponse_NodeStatus{NodeStatus: &relaypb.NodeStatusEvent{StateChange: state, NodeNames: nodes}}}
		}
		h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{
			status(relaypb.NodeState_NODE_CONNECTED, "node-a"),
			status(relaypb.NodeState_NODE_UNAVAILABLE, "node-b"),
			status(relaypb.NodeState_NODE_ERROR, "node-c", "node-b"),
			status(relaypb.NodeState_NODE_GONE, "node-d"),
			flowResponse("a", "b"),
		}})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Warning, "from 2 node(s)") {
			t.Errorf("warning = %q, want the unavailable and errored nodes counted once each", resp.Warning)
		}
		// The warning survives namespace filtering; node names are a
		// cluster-scoped read the viewer may not have.
		if strings.Contains(resp.Warning, "node-") {
			t.Errorf("warning = %q names a node", resp.Warning)
		}
	})

	t.Run("a stream that fails after reporting lost events keeps that evidence", func(t *testing.T) {
		h := connectedHubble(t, &scriptedObserver{
			responses: []*observerpb.GetFlowsResponse{
				{ResponseTypes: &observerpb.GetFlowsResponse_LostEvents{LostEvents: &flowpb.LostEvent{NumEventsLost: 5}}},
			},
			endErr: status.Error(codes.Unavailable, "relay restarted"),
		})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Warning, "5 events lost") || resp.WarningKind != WarningIncomplete {
			t.Errorf("warning = %q (%s), want the lost events reported as incomplete data", resp.Warning, resp.WarningKind)
		}
	})

	t.Run("reply packets draw no edge", func(t *testing.T) {
		reply := flowResponse("b", "a")
		reply.GetFlow().IsReply = wrapperspb.Bool(true)
		h := connectedHubble(t, &scriptedObserver{responses: []*observerpb.GetFlowsResponse{flowResponse("a", "b"), reply}})
		resp, err := h.GetFlows(context.Background(), DefaultFlowOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Flows) != 1 || resp.Flows[0].Source.Name != "a" {
			t.Fatalf("want only the request-direction flow, got %+v", resp.Flows)
		}
	})
}

func relayPod(phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "hubble-relay-0", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "hubble-relay"}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestHubbleDetect_InstalledButUnusableIsPresent(t *testing.T) {
	tlsService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: hubbleRelayService, Namespace: "kube-system"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromInt(4245)}}},
	}
	for _, tc := range []struct {
		name    string
		objects []runtime.Object
		want    string
	}{
		{"relay pods not running", []runtime.Object{relayPod(corev1.PodPending)}, "none are running"},
		{"relay service missing", []runtime.Object{relayPod(corev1.PodRunning)}, "service not found"},
		{"TLS relay without readable client certs", []runtime.Object{relayPod(corev1.PodRunning), tlsService}, "client certs not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewHubbleSource(fake.NewSimpleClientset(tc.objects...)).Detect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.Available {
				t.Fatal("Available = true")
			}
			if !result.Present {
				t.Errorf("Present = false: an installed relay would be reported as never installed")
			}
			if !strings.Contains(result.Message, tc.want) {
				t.Errorf("Message = %q, want it to contain %q", result.Message, tc.want)
			}
		})
	}

	t.Run("no relay at all is absent, not present", func(t *testing.T) {
		result, err := NewHubbleSource(fake.NewSimpleClientset()).Detect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Present {
			t.Error("Present = true with no relay pods")
		}
	})
}

func TestGenerateRecommendation_DoesNotAdviseEnablingAnUnusableHubble(t *testing.T) {
	m := &Manager{}
	unusable := []SourceStatus{{Name: "hubble", Status: "not_found", Message: "Hubble Relay pods running but service not found"}}
	if r := m.generateRecommendation(&ClusterInfo{CNI: "cilium"}, unusable); r != nil {
		t.Errorf("recommended %q (%s) while Hubble is installed but unusable", r.Name, r.Reason)
	}
	if r := m.generateRecommendation(&ClusterInfo{CNI: "cilium"}, nil); r == nil || r.Name != "hubble" {
		t.Errorf("a Cilium cluster without Hubble should still be told to enable it, got %+v", r)
	}
}
