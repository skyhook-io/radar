package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

var metalLBBGPPeerGVR = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta2", Resource: "bgppeers"}

const bgpPassword = "inline-md5-s3cret"

func setupMetalLBBGPPeers(t *testing.T) {
	t.Helper()
	addPackageNamespaces(t, "metallb-system")
	gvr := metalLBBGPPeerGVR
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"myASN": int64(64500), "peerASN": int64(64501), "peerAddress": "172.18.0.5", "password": bgpPassword,
	}}}
	u.SetAPIVersion("metallb.io/v1beta2")
	u.SetKind("BGPPeer")
	u.SetNamespace("metallb-system")
	u.SetName("router")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "BGPPeerList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, u)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "BGPPeer", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("BGPPeer cache did not sync")
	}
}

// Seeing MetalLB's namespace is not enough to read its BGP peers, whose spec
// can hold the session password inline: like Secrets, they need the caller's
// own permission on the kind.
func TestProxyAuth_MetalLBBGPPeersFollowPeerRBAC(t *testing.T) {
	setupMetalLBBGPPeers(t)
	const key = "metallb.io/BGPPeer"
	for _, tt := range []struct {
		name    string
		granted bool
	}{{"without bgppeers RBAC", false}, {"with bgppeers RBAC", true}} {
		t.Run(tt.name, func(t *testing.T) {
			env := newAuthTestServer(t)
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"metallb-system"}}
			for _, ns := range []string{"", "metallb-system"} {
				perms.SetCanI("list", metalLBBGPPeerGVR.Group, metalLBBGPPeerGVR.Resource, ns, tt.granted)
				perms.SetCanI("get", metalLBBGPPeerGVR.Group, metalLBBGPPeerGVR.Resource, ns, tt.granted)
			}
			env.srv.permCache.Set("viewer", nil, perms)

			for _, path := range []string{
				"/api/resources/bgppeers?group=metallb.io&namespaces=metallb-system",
				"/api/resources/bgppeers/metallb-system/router?group=metallb.io",
				"/api/ai/resources/bgppeers/metallb-system/router?group=metallb.io",
			} {
				resp := env.authGet(t, path, "viewer", "")
				body := readBody(t, resp)
				if tt.granted && resp.StatusCode != http.StatusOK {
					t.Errorf("%s: status %d, want 200", path, resp.StatusCode)
				}
				if !tt.granted && (resp.StatusCode != http.StatusForbidden || strings.Contains(body, bgpPassword) || strings.Contains(body, "172.18.0.5")) {
					t.Errorf("%s: status %d body %s, want 403 without the peer", path, resp.StatusCode, body)
				}
			}

			if !tt.granted {
				resp := env.authGet(t, "/api/search?q=router&include=raw", "viewer", "")
				if body := readBody(t, resp); strings.Contains(body, "BGPPeer") {
					t.Errorf("search returned the peer: %s", body)
				}
				resp = env.authGet(t, "/api/dashboard/crds", "viewer", "")
				if body := readBody(t, resp); strings.Contains(body, "BGPPeer") {
					t.Errorf("dashboard CRD counts include the peer: %s", body)
				}
			}

			resp := env.authGet(t, "/api/resource-counts", "viewer", "")
			var counts testCounts
			if err := json.Unmarshal([]byte(readBody(t, resp)), &counts); err != nil {
				t.Fatal(err)
			}
			if tt.granted && counts.Counts[key] != 1 {
				t.Errorf("count = %d, want 1", counts.Counts[key])
			}
			if !tt.granted && !slices.Contains(counts.Forbidden, key) {
				t.Errorf("counts forbidden = %v, want %s", counts.Forbidden, key)
			}
		})
	}
}
