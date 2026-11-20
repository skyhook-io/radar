package search

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestParse_IPTokensNormalized(t *testing.T) {
	cases := map[string]string{
		"10.244.162.159":       "10.244.162.159",
		"10.244.162.159:55622": "10.244.162.159",
		"[fd00::1]:8080":       "fd00::1",
		"[FD00::1]":            "fd00::1",
		"FD00:0:0::1":          "fd00::1",
		"::ffff:10.0.0.5":      "10.0.0.5",
		"fe80::1%eth0":         "fe80::1",
	}
	for in, want := range cases {
		q := Parse(in)
		if !reflect.DeepEqual(q.Tokens, []string{want}) || !reflect.DeepEqual(q.IPTokens(), []string{want}) {
			t.Errorf("Parse(%q): tokens=%v ips=%v, want %q", in, q.Tokens, q.IPTokens(), want)
		}
	}
	for _, in := range []string{"10.0.0", "10.0.0.0/16", "addr=10.0.0.5:6379", "redis:6379", "kind:Pod"} {
		if ips := Parse(in).IPTokens(); len(ips) != 0 {
			t.Errorf("Parse(%q) read IPs %v", in, ips)
		}
	}
}

func TestIndexIP_WholeAddressOnly(t *testing.T) {
	cases := []struct {
		s, ip string
		v6    bool
		want  bool
	}{
		{"10.0.1.2", "10.0.1.2", false, true},
		{"redis://10.0.1.2:6379/0", "10.0.1.2", false, true},
		{"addr=10.0.1.2, ok", "10.0.1.2", false, true},
		{"peer is 10.0.1.2.", "10.0.1.2", false, true},
		{"10.0.1.23", "10.0.1.2", false, false},
		{"110.0.1.2", "10.0.1.2", false, false},
		{"10.0.1.2.5", "10.0.1.2", false, false},
		{"10.0.1.23 and 10.0.1.2", "10.0.1.2", false, true},
		{"[FD00::1]:80", "fd00::1", true, true},
		{"fd00::12", "fd00::1", true, false},
		{"fd00::1:5", "fd00::1", true, false},
	}
	for _, c := range cases {
		if got := indexIP(c.s, c.ip, c.v6) >= 0; got != c.want {
			t.Errorf("indexIP(%q, %q) >= 0 is %v, want %v", c.s, c.ip, got, c.want)
		}
	}
}

func typedCandidate(t *testing.T, obj runtime.Object, kind string) candidate {
	t.Helper()
	c, ok := fromObject(obj, kind)
	if !ok {
		t.Fatalf("fromObject(%s) failed", kind)
	}
	return c
}

func ipPod(name, ip string, mutate func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase:  corev1.PodRunning,
			PodIP:  ip,
			PodIPs: []corev1.PodIP{{IP: ip}},
			HostIP: "192.168.1.10",
		},
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func TestMatch_IPHolderRoles(t *testing.T) {
	cases := []struct {
		name  string
		query string
		obj   runtime.Object
		kind  string
		site  string
		score int
	}{
		{"pod ip:port", "10.244.1.7:6379", ipPod("redis-0", "10.244.1.7", nil), "Pod", "ip:podIP", scoreIPExact},
		{"hostNetwork pod", "192.168.1.10", ipPod("kube-proxy-x", "192.168.1.10", func(p *corev1.Pod) { p.Spec.HostNetwork = true }), "Pod", "ip:hostNetworkPod", scoreIPExact},
		{"exited pod ranks lower", "10.244.1.7", ipPod("job-x", "10.244.1.7", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }), "Pod", "ip:exitedPodIP", scoreIPExited},
		{"pod hostIP is only a mention", "192.168.1.10", ipPod("redis-0", "10.244.1.7", nil), "Pod", "content:status.hostIP", scoreContentExact},
		{"service clusterIP", "10.96.0.10:53", &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
			Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.10", ClusterIPs: []string{"10.96.0.10"}},
		}, "Service", "ip:clusterIP", scoreIPExact},
		{"service LB ingress", "203.0.113.9", &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
			Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.9"}}}},
		}, "Service", "ip:loadBalancerIP", scoreIPExact},
		{"requested LB IP is only a mention", "203.0.113.9", &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
			Spec:       corev1.ServiceSpec{LoadBalancerIP: "203.0.113.9"},
		}, "Service", "content:spec.loadBalancerIP", scoreContentExact},
		{"node address", "192.168.1.10", &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
			Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: "node-a"}, {Type: corev1.NodeInternalIP, Address: "192.168.1.10"}}},
		}, "Node", "ip:nodeInternalIP", scoreIPExact},
		{"dual-stack pod v6", "[fd00::7]:80", ipPod("web-0", "10.244.1.7", func(p *corev1.Pod) {
			p.Status.PodIPs = append(p.Status.PodIPs, corev1.PodIP{IP: "fd00::7"})
		}), "Pod", "ip:podIP", scoreIPExact},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score, matched, _, ok := match(Parse(c.query), typedCandidate(t, c.obj, c.kind))
			if !ok {
				t.Fatal("expected match")
			}
			if score != c.score || len(matched) != 1 || matched[0].Site != c.site {
				t.Fatalf("score=%d matched=%+v, want %d at %s", score, matched, c.score, c.site)
			}
		})
	}
}

func TestMatch_IPNoPrefixOverlap(t *testing.T) {
	pod := typedCandidate(t, ipPod("redis-0", "10.244.1.23", func(p *corev1.Pod) {
		p.Spec.Containers = []corev1.Container{{Name: "c", Env: []corev1.EnvVar{{Name: "PEER", Value: "10.244.1.23:6379"}}}}
	}), "Pod")
	if _, matched, _, ok := match(Parse("10.244.1.2"), pod); ok {
		t.Fatalf("10.244.1.2 matched a pod holding 10.244.1.23: %+v", matched)
	}
	cm := candidate{Kind: "ConfigMap", Namespace: "prod", Name: "cfg", Content: []ContentField{{Path: "data.url", Value: "redis://10.244.1.2:6379"}}}
	_, matched, snippets, ok := match(Parse("10.244.1.2:41000"), cm)
	if !ok || matched[0].Site != "content:data.url" || len(snippets) != 1 {
		t.Fatalf("ConfigMap mention: ok=%v matched=%+v snippets=%+v", ok, matched, snippets)
	}
}

func TestMatch_IPSnippetShowsTheWholeAddress(t *testing.T) {
	long := "upstream 10.0.1.23:80 " + strings.Repeat("x", 300) + " fallback 10.0.1.2:80"
	cm := candidate{Kind: "ConfigMap", Namespace: "prod", Name: "cfg", Content: []ContentField{{Path: "data.conf", Value: long}}}
	_, _, snippets, ok := match(Parse("10.0.1.2"), cm)
	if !ok || len(snippets) != 1 || !strings.Contains(snippets[0].Snippet, "fallback 10.0.1.2:80") {
		t.Fatalf("ok=%v snippets=%+v", ok, snippets)
	}
}

func TestMatch_IPMentionsBelowHolders(t *testing.T) {
	named := candidate{Kind: "ConfigMap", Namespace: "prod", Name: "10.0.1.2"}
	if score, _, _, ok := match(Parse("10.0.1.2"), named); !ok || score >= scoreIPExact {
		t.Fatalf("name mention: ok=%v score=%d, want below %d", ok, score, scoreIPExact)
	}
	registry := candidate{Kind: "Deployment", Namespace: "prod", Name: "app", Images: []string{"10.0.1.2:5000/app:v1"}}
	if _, matched, _, ok := match(Parse("10.0.1.2"), registry); !ok || matched[0].Site != "image" {
		t.Fatalf("registry image: ok=%v matched=%+v", ok, matched)
	}
}

func TestMatch_IPv6SnippetAfterNonASCII(t *testing.T) {
	// "İ" lowercases to a longer byte sequence; offsets must stay in the original.
	long := strings.Repeat("İ", 200) + " peer [FD00::1]:80"
	cm := candidate{Kind: "ConfigMap", Namespace: "prod", Name: "cfg", Content: []ContentField{{Path: "data.conf", Value: long}}}
	_, _, snippets, ok := match(Parse("fd00::1"), cm)
	if !ok || len(snippets) != 1 || !strings.Contains(snippets[0].Snippet, "[FD00::1]:80") {
		t.Fatalf("ok=%v snippets=%+v", ok, snippets)
	}
}
