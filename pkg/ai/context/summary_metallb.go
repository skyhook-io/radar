package context

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const metalLBGroup = "metallb.io"

// MetalLB reports math.MaxInt64 for an IPv6 range with 62 or more host bits,
// which it treats as never running out. Anything past 2^53 also loses
// precision in the UI, so both read such counts as unlimited.
const metalLBUnlimited = 1 << 53

type metalLBPoolCounters struct {
	assigned4, available4, assigned6, available6 int64
}

func (c metalLBPoolCounters) capacity4() int64 { return saturatingAdd(c.assigned4, c.available4) }
func (c metalLBPoolCounters) capacity6() int64 { return saturatingAdd(c.assigned6, c.available6) }

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// metalLBPoolCountersOf reads the address counters the MetalLB controller
// writes to an IPAddressPool's status (v0.15 and later). Without a status the
// counters are unknown: an older MetalLB, a pool outside MetalLB's namespace,
// or one the controller has not reconciled. The same goes for a status that
// lacks any of the four counters.
func metalLBPoolCountersOf(obj *unstructured.Unstructured) (metalLBPoolCounters, bool) {
	status, found, _ := unstructured.NestedMap(obj.Object, "status")
	if !found || status == nil {
		return metalLBPoolCounters{}, false
	}
	// The controller writes all four counters together, so a status missing
	// any of them has not been reported, rather than reporting zero.
	var vals [4]int64
	for i, key := range [...]string{"assignedIPv4", "availableIPv4", "assignedIPv6", "availableIPv6"} {
		v, found, err := unstructured.NestedInt64(status, key)
		if !found || err != nil {
			return metalLBPoolCounters{}, false
		}
		vals[i] = v
	}
	return metalLBPoolCounters{assigned4: vals[0], available4: vals[1], assigned6: vals[2], available6: vals[3]}, true
}

// metalLBPoolState reads an IPAddressPool's address availability. Counters
// are addresses, not Services: Services can share an address. A pool the
// controller has not loaded, for example because it rejected the
// configuration, reports zero for everything. Mirrors getMetalLBPoolStatus in
// packages/k8s-ui.
func metalLBPoolState(obj *unstructured.Unstructured) string {
	if obj.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	c, ok := metalLBPoolCountersOf(obj)
	if !ok {
		return "Not reported"
	}
	has4, has6 := c.capacity4() > 0, c.capacity6() > 0
	if !has4 && !has6 {
		return "No addresses"
	}
	out4, out6 := has4 && c.available4 <= 0, has6 && c.available6 <= 0
	switch {
	case (out4 || !has4) && (out6 || !has6):
		return "Exhausted"
	case out4:
		return "IPv4 exhausted"
	case out6:
		return "IPv6 exhausted"
	}
	return "Available"
}

func metalLBCountText(n int64) string {
	if n > metalLBUnlimited {
		return "unlimited"
	}
	return strconv.FormatInt(n, 10)
}

func metalLBPoolCapacity(c metalLBPoolCounters) string {
	var parts []string
	if c.capacity4() > 0 {
		parts = append(parts, fmt.Sprintf("IPv4 %s assigned, %s available", metalLBCountText(c.assigned4), metalLBCountText(c.available4)))
	}
	if c.capacity6() > 0 {
		parts = append(parts, fmt.Sprintf("IPv6 %s assigned, %s available", metalLBCountText(c.assigned6), metalLBCountText(c.available6)))
	}
	return strings.Join(parts, "; ")
}

func summarizeMetalLBIPAddressPool(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "IPAddressPool",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Status:    metalLBPoolState(obj),
	}
	if addresses, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "addresses"); len(addresses) > 0 {
		s.Target = strings.Join(addresses, ", ")
	}
	if c, ok := metalLBPoolCountersOf(obj); ok {
		s.Capacity = metalLBPoolCapacity(c)
	}
	if auto, found, _ := unstructured.NestedBool(obj.Object, "spec", "autoAssign"); found && !auto {
		s.Strategy = "Requested only"
	}
	return s
}

// metalLBAdvertisedPools describes which pools an advertisement covers. With
// neither names nor selectors it covers every pool.
func metalLBAdvertisedPools(obj *unstructured.Unstructured) string {
	names, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "ipAddressPools")
	selectors, _, _ := unstructured.NestedSlice(obj.Object, "spec", "ipAddressPoolSelectors")
	var parts []string
	for _, name := range names {
		parts = append(parts, "IPAddressPool/"+name)
	}
	if len(selectors) > 0 {
		parts = append(parts, "pools matching selectors")
	}
	if len(parts) == 0 {
		return "all pools"
	}
	return strings.Join(parts, ", ")
}

func summarizeMetalLBAdvertisement(obj *unstructured.Unstructured, mode string) *ResourceSummary {
	return &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Type:      mode,
		Target:    metalLBAdvertisedPools(obj),
	}
}

// summarizeMetalLBBGPPeer leaves out spec.password, which holds the session's
// MD5 password inline.
func summarizeMetalLBBGPPeer(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "BGPPeer",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}
	address, _, _ := unstructured.NestedString(obj.Object, "spec", "peerAddress")
	if address == "" {
		if iface, _, _ := unstructured.NestedString(obj.Object, "spec", "interface"); iface != "" {
			address = "interface " + iface
		}
	} else if port, found, _ := unstructured.NestedInt64(obj.Object, "spec", "peerPort"); found && port > 0 {
		address = net.JoinHostPort(address, strconv.FormatInt(port, 10))
	}
	s.Target = address
	myASN, _, _ := unstructured.NestedInt64(obj.Object, "spec", "myASN")
	peerASN, _, _ := unstructured.NestedInt64(obj.Object, "spec", "peerASN")
	dynamic, _, _ := unstructured.NestedString(obj.Object, "spec", "dynamicASN")
	switch {
	case peerASN > 0 && myASN > 0 && peerASN == myASN:
		s.Type = "iBGP"
	case peerASN > 0 && myASN > 0:
		s.Type = "eBGP"
	case dynamic == "internal":
		s.Type = "iBGP"
	case dynamic == "external":
		s.Type = "eBGP"
	}
	var asns []string
	if myASN > 0 {
		asns = append(asns, "local ASN "+strconv.FormatInt(myASN, 10))
	}
	if peerASN > 0 {
		asns = append(asns, "peer ASN "+strconv.FormatInt(peerASN, 10))
	} else if dynamic != "" {
		asns = append(asns, "peer ASN detected ("+dynamic+")")
	}
	s.Strategy = strings.Join(asns, ", ")
	return s
}

func summarizeMetalLBConfigurationState(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "ConfigurationState",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Type:      obj.GetLabels()["metallb.io/component-type"],
		Node:      obj.GetLabels()["metallb.io/node-name"],
	}
	result, _, _ := unstructured.NestedString(obj.Object, "status", "result")
	s.Status = result
	if result == "" {
		s.Status = "Not reported"
	}
	if obj.GetDeletionTimestamp() != nil {
		s.Status = "Terminating"
		return s
	}
	if result == "Invalid" {
		s.Issue = "MetalLBConfigurationInvalid"
	}
	return s
}
