package context

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func metalLBObject(apiVersion, kind string, fields map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": "obj", "namespace": "metallb-system"},
	}
	for k, v := range fields {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func poolStatus(assigned4, available4, assigned6, available6 int64) map[string]any {
	return map[string]any{"assignedIPv4": assigned4, "availableIPv4": available4, "assignedIPv6": assigned6, "availableIPv6": available6}
}

func TestMetalLBPoolSummaryStatus(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       map[string]any
		wantStatus   string
		wantCapacity string
	}{
		{"no status", nil, "Not reported", ""},
		{"addresses free", poolStatus(2, 8, 0, 0), "Available", "IPv4 2 assigned, 8 available"},
		{"all assigned", poolStatus(10, 0, 0, 0), "Exhausted", "IPv4 10 assigned, 0 available"},
		{"not loaded by the controller", poolStatus(0, 0, 0, 0), "No addresses", ""},
		{"dual stack, IPv4 out", poolStatus(4, 0, 1, 15), "IPv4 exhausted", "IPv4 4 assigned, 0 available; IPv6 1 assigned, 15 available"},
		{"dual stack, IPv6 out", poolStatus(1, 3, 2, 0), "IPv6 exhausted", "IPv4 1 assigned, 3 available; IPv6 2 assigned, 0 available"},
		{"dual stack, both out", poolStatus(4, 0, 2, 0), "Exhausted", "IPv4 4 assigned, 0 available; IPv6 2 assigned, 0 available"},
		{"IPv6 /120", poolStatus(0, 0, 3, 253), "Available", "IPv6 3 assigned, 253 available"},
		{"IPv6 with 62 or more host bits", poolStatus(0, 0, 3, math.MaxInt64-3), "Available", "IPv6 3 assigned, unlimited available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{"spec": map[string]any{"addresses": []any{"192.168.10.0/24", "fc00:f853:ccd:e799::/124"}}}
			if tc.status != nil {
				fields["status"] = tc.status
			}
			s := summarizeUnstructured(metalLBObject("metallb.io/v1beta1", "IPAddressPool", fields))
			if s.Status != tc.wantStatus || s.Capacity != tc.wantCapacity {
				t.Fatalf("got status=%q capacity=%q", s.Status, s.Capacity)
			}
			if s.Target != "192.168.10.0/24, fc00:f853:ccd:e799::/124" {
				t.Fatalf("target = %q", s.Target)
			}
		})
	}
}

func TestMetalLBPoolSummaryAutoAssignAndTerminating(t *testing.T) {
	u := metalLBObject("metallb.io/v1beta1", "IPAddressPool", map[string]any{
		"spec":   map[string]any{"addresses": []any{"10.0.0.10-10.0.0.20"}, "autoAssign": false},
		"status": poolStatus(0, 11, 0, 0),
	})
	if s := summarizeUnstructured(u); s.Strategy != "Requested only" || s.Status != "Available" {
		t.Fatalf("got %+v", s)
	}
	now := metav1.Now()
	u.SetDeletionTimestamp(&now)
	if s := summarizeUnstructured(u); s.Status != "Terminating" || !s.Terminating {
		t.Fatalf("got %+v", s)
	}
}

func TestMetalLBAdvertisementSummary(t *testing.T) {
	for _, tc := range []struct {
		name, kind, wantType, wantTarget string
		spec                             map[string]any
	}{
		{"every pool", "L2Advertisement", "L2", "all pools", map[string]any{}},
		{"named pools", "L2Advertisement", "L2", "IPAddressPool/first, IPAddressPool/second", map[string]any{"ipAddressPools": []any{"first", "second"}}},
		{"names and selectors", "BGPAdvertisement", "BGP", "IPAddressPool/first, pools matching selectors", map[string]any{
			"ipAddressPools":         []any{"first"},
			"ipAddressPoolSelectors": []any{map[string]any{"matchLabels": map[string]any{"tier": "public"}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summarizeUnstructured(metalLBObject("metallb.io/v1beta1", tc.kind, map[string]any{"spec": tc.spec}))
			if s.Kind != tc.kind || s.Type != tc.wantType || s.Target != tc.wantTarget {
				t.Fatalf("got %+v", s)
			}
		})
	}
}

func TestMetalLBBGPPeerSummaryOmitsPassword(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		spec                             map[string]any
		wantType, wantTarget, wantASNs string
	}{
		{"eBGP with port", map[string]any{"myASN": int64(64500), "peerASN": int64(64501), "peerAddress": "172.18.0.5", "peerPort": int64(179)}, "eBGP", "172.18.0.5:179", "local ASN 64500, peer ASN 64501"},
		{"iBGP over IPv6", map[string]any{"myASN": int64(64500), "peerASN": int64(64500), "peerAddress": "fc00::1", "peerPort": int64(1179)}, "iBGP", "[fc00::1]:1179", "local ASN 64500, peer ASN 64500"},
		{"unnumbered, dynamic ASN", map[string]any{"myASN": int64(64500), "interface": "eth1", "dynamicASN": "external"}, "eBGP", "interface eth1", "local ASN 64500, peer ASN detected (external)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{"password": "s3cret-md5", "passwordSecret": map[string]any{"name": "bgp-auth"}}
			for k, v := range tc.spec {
				spec[k] = v
			}
			s := summarizeUnstructured(metalLBObject("metallb.io/v1beta2", "BGPPeer", map[string]any{"spec": spec}))
			if s.Type != tc.wantType || s.Target != tc.wantTarget || s.Strategy != tc.wantASNs {
				t.Fatalf("got %+v", s)
			}
			raw, _ := json.Marshal(s)
			if strings.Contains(string(raw), "s3cret-md5") {
				t.Fatalf("summary leaks the BGP password: %s", raw)
			}
		})
	}
}

func TestMetalLBConfigurationStateSummary(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		status                map[string]any
		wantStatus, wantIssue string
	}{
		{"accepted", map[string]any{"result": "Valid"}, "Valid", ""},
		{"rejected", map[string]any{"result": "Invalid", "errorSummary": "secret ref not found"}, "Invalid", "MetalLBConfigurationInvalid"},
		{"not reported yet", nil, "Not reported", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := metalLBObject("metallb.io/v1beta1", "ConfigurationState", map[string]any{})
			u.SetLabels(map[string]string{"metallb.io/component-type": "speaker", "metallb.io/node-name": "worker"})
			if tc.status != nil {
				u.Object["status"] = tc.status
			}
			s := summarizeUnstructured(u)
			if s.Status != tc.wantStatus || s.Issue != tc.wantIssue || s.Type != "speaker" || s.Node != "worker" {
				t.Fatalf("got %+v", s)
			}
		})
	}
}

func TestMetalLBSummaryIgnoresLookalikeGroup(t *testing.T) {
	u := metalLBObject("crd.projectcalico.org/v1", "BGPPeer", map[string]any{"spec": map[string]any{"peerIP": "10.0.0.1", "asNumber": int64(64512)}})
	if s := summarizeUnstructured(u); s.Strategy != "" || s.Type != "" {
		t.Fatalf("Calico BGPPeer read as MetalLB: %+v", s)
	}
	pool := metalLBObject("fake.metallb.io/v1beta1", "IPAddressPool", map[string]any{"status": poolStatus(1, 0, 0, 0)})
	if s := summarizeUnstructured(pool); s.Status == "Exhausted" {
		t.Fatalf("lookalike group read as MetalLB: %+v", s)
	}
}
