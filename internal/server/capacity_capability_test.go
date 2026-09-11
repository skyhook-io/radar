package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/karpenter"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKarpenterCapabilityReportsAbsenceBeforeRBACDenial(t *testing.T) {
	initCapacityContractDynamicState(t, false, false)
	previousConnection := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer k8s.SetConnectionStatus(previousConnection)
	previousClient := k8s.SetTestClient(&kubernetes.Clientset{})
	defer k8s.SetTestClient(previousClient)

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", karpenterGroup, karpenterNodePoolResource, "", false)
	s.permCache.Set("alice", nil, perms)

	r := requestWithUser(http.MethodGet, "/api/capabilities", &auth.User{Username: "alice"})
	got := s.karpenterCapability(r)
	if got.State != capacityapi.IntegrationNotDetected {
		t.Fatalf("state = %q, want %q", got.State, capacityapi.IntegrationNotDetected)
	}
	if got.ReasonCode != "" {
		t.Fatalf("reasonCode = %q, want empty", got.ReasonCode)
	}
}

func TestKarpenterCapabilityShortCircuitsWithoutUsableConnection(t *testing.T) {
	tests := []struct {
		name       string
		connection k8s.ConnectionStatus
		client     *kubernetes.Clientset
		seedDenied bool
		wantReason string
	}{
		{
			name:       "connecting",
			connection: k8s.ConnectionStatus{State: k8s.StateConnecting},
			client:     &kubernetes.Clientset{},
			seedDenied: true,
			wantReason: "cluster_connecting",
		},
		{
			name:       "disconnected",
			connection: k8s.ConnectionStatus{State: k8s.StateDisconnected},
			client:     &kubernetes.Clientset{},
			seedDenied: true,
			wantReason: "cluster_disconnected",
		},
		{
			name:       "client unavailable",
			connection: k8s.ConnectionStatus{State: k8s.StateConnected},
			client:     nil,
			wantReason: "cluster_client_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previousConnection := k8s.GetConnectionStatus()
			k8s.SetConnectionStatus(tt.connection)
			defer k8s.SetConnectionStatus(previousConnection)
			previousClient := k8s.SetTestClient(tt.client)
			defer k8s.SetTestClient(previousClient)

			s := newAuthServer(auth.Config{Mode: "proxy"})
			perms := &auth.UserPermissions{AllowedNamespaces: nil}
			if tt.seedDenied {
				perms.SetCanI("list", karpenterGroup, karpenterNodePoolResource, "", false)
			}
			s.permCache.Set("alice", nil, perms)
			r := requestWithUser(http.MethodGet, "/api/capabilities", &auth.User{Username: "alice"})

			got := s.karpenterCapability(r)
			if got.State != capacityapi.IntegrationSyncing {
				t.Fatalf("state = %q, want %q", got.State, capacityapi.IntegrationSyncing)
			}
			if got.ReasonCode != tt.wantReason {
				t.Fatalf("reasonCode = %q, want %q", got.ReasonCode, tt.wantReason)
			}
		})
	}
}

func TestLoadCapacityNodeClassesDistinguishesPartialDiscoveryFromAbsentAPI(t *testing.T) {
	const nodeClassGroup = "karpenter.k8s.aws"
	pool := capacityContractNodePool("general")
	pool.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{
		"nodeClassRef": map[string]any{"group": nodeClassGroup, "kind": "EC2NodeClass", "name": "default"},
	}}}
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name       string
		partial    bool
		wantStatus capacityapi.CoverageStatus
		wantReason string
	}{
		{name: "partial group discovery", partial: true, wantStatus: capacityapi.CoveragePartial, wantReason: "nodeclasses_discovery_partial"},
		{name: "clean discovery without API", wantStatus: capacityapi.CoverageUnavailable, wantReason: "nodeclass_kinds_not_discovered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDiscovery.Resources = []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}},
			}}
			if test.partial {
				fakeDiscovery.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
						{Group: nodeClassGroup, Version: "v1"}: apierrors.NewForbidden(schema.GroupResource{Group: nodeClassGroup, Resource: "ec2nodeclasses"}, "", nil),
					}}
				})
			}
			coreDiscovery, err := k8score.NewResourceDiscovery(fakeDiscovery)
			if err != nil {
				t.Fatalf("NewResourceDiscovery: %v", err)
			}
			discoveryView := &k8s.ResourceDiscovery{ResourceDiscovery: coreDiscovery}
			meta := newCapacityResponseMeta(now, currentCapacityClusterIdentity())
			(&Server{}).loadCapacityNodeClasses(nil, discoveryView, nil, []*unstructured.Unstructured{pool}, &meta, now)
			coverage := meta.Coverage[capacityapi.CoverageNodeClasses]
			if coverage.Status != test.wantStatus || coverage.ReasonCode != test.wantReason {
				t.Fatalf("NodeClass coverage = %#v, want %q/%q", coverage, test.wantStatus, test.wantReason)
			}
		})
	}
}

func TestLoadCapacityNodeClaimsDistinguishesPartialDiscoveryFromAbsentAPI(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		partial    bool
		wantStatus capacityapi.CoverageStatus
		wantReason string
	}{
		{name: "partial Karpenter discovery", partial: true, wantStatus: capacityapi.CoveragePartial, wantReason: "nodeclaims_discovery_partial"},
		{name: "clean discovery without API", wantStatus: capacityapi.CoverageUnavailable, wantReason: "nodeclaims_not_discovered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDiscovery.Resources = []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}},
			}}
			if test.partial {
				fakeDiscovery.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
						{Group: karpenter.Group, Version: "v1"}: apierrors.NewForbidden(schema.GroupResource{Group: karpenter.Group, Resource: "nodeclaims"}, "", nil),
					}}
				})
			}
			coreDiscovery, err := k8score.NewResourceDiscovery(fakeDiscovery)
			if err != nil {
				t.Fatalf("NewResourceDiscovery: %v", err)
			}
			discoveryView := &k8s.ResourceDiscovery{ResourceDiscovery: coreDiscovery}
			meta := newCapacityResponseMeta(now, currentCapacityClusterIdentity())
			request := httptest.NewRequest(http.MethodGet, "/api/capacity", nil)
			claims := (&Server{}).loadCapacityNodeClaims(request, discoveryView, nil, &meta, now)
			if len(claims) != 0 {
				t.Fatalf("claims = %#v, want none without a discovered API", claims)
			}
			coverage := meta.Coverage[capacityapi.CoverageNodeClaims]
			if coverage.Status != test.wantStatus || coverage.ReasonCode != test.wantReason {
				t.Fatalf("NodeClaim coverage = %#v, want %q/%q", coverage, test.wantStatus, test.wantReason)
			}
		})
	}
}
