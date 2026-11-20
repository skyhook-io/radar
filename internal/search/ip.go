package search

import (
	"net/netip"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// scoreIPExact is an IP token equal to an address a Pod, Service or Node
// holds. It outranks every content mention of the same address. An exited
// Pod keeps its podIP, which a running pod may have been given since, so its
// match ranks below a live holder's.
const (
	scoreIPExact  = 90
	scoreIPExited = 40
)

// ipField is an address an object holds, with the role that says what the
// address is: "podIP", "hostNetworkPod", "exitedPodIP", "clusterIP",
// "externalIP", "loadBalancerIP", or "node" plus the Node address type
// ("nodeInternalIP").
type ipField struct {
	Role  string
	Addr  netip.Addr
	Score int
}

// parseIPToken reports whether a free token is an IP address, as written in
// logs: a bare address, ip:port, [v6]:port or [v6]. It returns the canonical
// address, so 10.0.0.5:6379 searches for 10.0.0.5.
func parseIPToken(tok string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(tok); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(tok); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
		if a, err := netip.ParseAddr(tok[1 : len(tok)-1]); err == nil && a.Is6() {
			return a.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}

// ipsForTyped lists the addresses a Pod, Service or Node holds. A Pod's
// hostIP is the node's address, not the pod's, so it is left to content
// matching, where it ranks below the Node itself.
func ipsForTyped(obj runtime.Object) []ipField {
	var out []ipField
	score := scoreIPExact
	add := func(role, s string) {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, ipField{Role: role, Addr: a.Unmap(), Score: score})
		}
	}
	switch o := obj.(type) {
	case *corev1.Pod:
		role := "podIP"
		switch {
		case o.Status.Phase == corev1.PodSucceeded || o.Status.Phase == corev1.PodFailed:
			role, score = "exitedPodIP", scoreIPExited
		case o.Spec.HostNetwork:
			role = "hostNetworkPod"
		}
		add(role, o.Status.PodIP)
		for _, ip := range o.Status.PodIPs {
			add(role, ip.IP)
		}
	case *corev1.Service:
		add("clusterIP", o.Spec.ClusterIP)
		for _, ip := range o.Spec.ClusterIPs {
			add("clusterIP", ip)
		}
		for _, ip := range o.Spec.ExternalIPs {
			add("externalIP", ip)
		}
		// spec.loadBalancerIP is only a request; the ingress is what was assigned.
		for _, ing := range o.Status.LoadBalancer.Ingress {
			add("loadBalancerIP", ing.IP)
		}
	case *corev1.Node:
		for _, a := range o.Status.Addresses {
			add("node"+string(a.Type), a.Address)
		}
	}
	return out
}

// indexIP returns the byte offset where s mentions ip as a whole address, or
// -1: 10.0.1.2 matches "10.0.1.2:80" and "addr=10.0.1.2," but not
// "10.0.1.23" or "110.0.1.2". ip is canonical, so an IPv6 address is matched
// lowercase.
func indexIP(s, ip string, v6 bool) int {
	if v6 {
		s = lowerASCII(s)
	}
	for from := 0; ; {
		i := strings.Index(s[from:], ip)
		if i < 0 {
			return -1
		}
		start := from + i
		end := start + len(ip)
		if !ipContinues(s, start-1, -1, v6) && !ipContinues(s, end, 1, v6) {
			return start
		}
		from = start + 1
	}
}

// lowerASCII lowercases ASCII letters only, so byte offsets into the result
// are offsets into s (strings.ToLower can change a non-ASCII rune's length).
func lowerASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// ipContinues reports whether the byte at i extends the address next to it:
// a digit (or hex digit for IPv6), or a separator followed, in direction dir,
// by one. A trailing "." ending a sentence or a ":port" after an IPv4
// address doesn't extend it.
func ipContinues(s string, i, dir int, v6 bool) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	isDigit := func(b byte) bool {
		return b >= '0' && b <= '9' || v6 && (b >= 'a' && b <= 'f')
	}
	b := s[i]
	if isDigit(b) {
		return true
	}
	sep := b == '.' || v6 && b == ':'
	next := i + dir
	return sep && next >= 0 && next < len(s) && isDigit(s[next])
}
