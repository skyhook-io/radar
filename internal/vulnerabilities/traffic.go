package vulnerabilities

import (
	"cmp"
	"slices"
	"strings"
	"time"

	pkgtraffic "github.com/skyhook-io/radar/pkg/traffic"
)

// Traffic says which of the answer's workloads Live Traffic saw receive
// connections. It is only ever a positive signal: it is absent when no
// traffic source answered, and a workload it doesn't list is not "no
// traffic", since a source misses what it can't see.
type Traffic struct {
	// Source is the traffic source that answered (hubble, caretta, beyla,
	// istio).
	Source string `json:"source"`
	// FromOutside is true when only callers the source identifies as outside
	// the cluster count: Hubble's world and CIDR identities. An outside
	// client whose address a node rewrote (kube-proxy SNAT, a LoadBalancer
	// with externalTrafficPolicy Cluster) reads as that node and doesn't
	// count. False when the source can't tell an outside client from a node,
	// so any caller but a node counts, inside the cluster or out.
	FromOutside bool `json:"fromOutside"`
	// WindowSeconds is how far back the source answered for.
	WindowSeconds int `json:"windowSeconds"`
	// HistoryLimited is true when the source keeps a bounded history (each
	// node's Hubble flow buffer, or the newest flows past a fetch's budget),
	// so it may reach back less far than WindowSeconds.
	HistoryLimited bool `json:"historyLimited,omitempty"`
	// ObservedAt is when the source was read; the window ends there, which
	// can be minutes before the page shows it.
	ObservedAt string `json:"observedAt,omitempty"`
	// Workloads are the answer's workloads it saw, each once without a
	// container, at most MaxRows of WorkloadsTotal in a list read.
	Workloads      []WorkloadRef `json:"workloads"`
	WorkloadsTotal int           `json:"workloadsTotal"`
}

// TrafficEvidence is what a traffic source saw arrive at workloads, indexed
// for the rollup. Build it with NewTrafficEvidence.
type TrafficEvidence struct {
	Source         string
	FromOutside    bool
	Window         time.Duration
	HistoryLimited bool
	// At is when the source was read.
	At time.Time
	// seen holds, by namespace/name, the lower-cased kinds seen receiving;
	// "" when the source named no kind (metrics sources name a workload
	// only).
	seen map[string]map[string]bool
	// shared holds the namespace/names more than one workload kind in the
	// cluster has: a match by name alone can't say which was seen.
	shared map[string]bool
}

// MarkSharedNames records which of the names seen without a kind more than
// one workload in the cluster has, by the whole cluster's inventory rather
// than one caller's view, so what a caller can read never decides whose
// traffic it was.
func (e *TrafficEvidence) MarkSharedNames(shared func(namespace, name string) bool) {
	if e == nil || shared == nil {
		return
	}
	for key, kinds := range e.seen {
		if !kinds[""] {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		if shared(ns, name) {
			if e.shared == nil {
				e.shared = map[string]bool{}
			}
			e.shared[key] = true
		}
	}
}

// trafficFromOutsideSources are the sources whose endpoint kinds tell a
// caller outside the cluster from a node: Hubble reads Cilium's identities.
// The others call any endpoint without a namespace External, nodes included.
var trafficFromOutsideSources = map[string]bool{"hubble": true}

// NewTrafficEvidence indexes the flows a source returned for its window.
// isNode says whether an endpoint is one of the cluster's nodes, for a
// source that can't tell them from outside callers; nil reads as none.
func NewTrafficEvidence(source string, window time.Duration, historyLimited bool, flows []pkgtraffic.Flow, isNode func(pkgtraffic.Endpoint) bool) *TrafficEvidence {
	e := &TrafficEvidence{
		Source: source, FromOutside: trafficFromOutsideSources[source], Window: window, HistoryLimited: historyLimited, At: time.Now(),
		seen: map[string]map[string]bool{},
	}
	for _, f := range flows {
		ns, name, kind, ok := receiver(f, e.FromOutside, isNode)
		if !ok {
			continue
		}
		key := ns + "/" + name
		if e.seen[key] == nil {
			e.seen[key] = map[string]bool{}
		}
		e.seen[key][strings.ToLower(kind)] = true
	}
	return e
}

// Len is how many workloads it saw receive connections.
func (e *TrafficEvidence) Len() int { return len(e.seen) }

// receiver is the workload a flow shows receiving a connection that counts,
// and its kind when the source names one.
func receiver(f pkgtraffic.Flow, fromOutside bool, isNode func(pkgtraffic.Endpoint) bool) (namespace, name, kind string, ok bool) {
	// Without a known direction either end may have called the other.
	if f.DirectionUnknown || f.Verdict == "dropped" {
		return "", "", "", false
	}
	// Hubble's error verdict is a flow it couldn't deliver; elsewhere it
	// marks responses that failed, after the request arrived.
	if fromOutside && f.Verdict == "error" {
		return "", "", "", false
	}
	if f.Connections <= 0 && f.BytesSent <= 0 && f.BytesRecv <= 0 && f.RequestRate <= 0 {
		return "", "", "", false
	}
	d, s := f.Destination, f.Source
	if d.Namespace == "" || notWorkload(d) {
		return "", "", "", false
	}
	name, kind = d.Workload, d.WorkloadKind
	switch {
	case name == "" && strings.EqualFold(d.Kind, pkgtraffic.EndpointKindPod):
		// A Pod nothing owns is its own workload.
		name, kind = d.Name, "Pod"
	case name == "":
		name = d.Name
	}
	// Caretta names the workload's own kind (Deployment, StatefulSet).
	if kind == "" && !strings.EqualFold(d.Kind, pkgtraffic.EndpointKindPod) && !strings.EqualFold(d.Kind, pkgtraffic.EndpointKindWorkload) {
		kind = d.Kind
	}
	if fromOutside {
		return d.Namespace, name, kind, s.Kind == pkgtraffic.EndpointKindExternal
	}
	// A node is where kubelet probes come from, so counting one would mark
	// nearly every workload.
	if nodeLike(s) || (isNode != nil && isNode(s)) {
		return "", "", "", false
	}
	// A caller the source couldn't attribute (Istio's "unknown" peer) may
	// be a node too.
	if unattributed(s) {
		return "", "", "", false
	}
	// A workload's own replicas talking to each other aren't callers. Two
	// kinds sharing a name are two workloads when both kinds are known.
	if s.Namespace == d.Namespace && cmp.Or(s.Workload, s.Name) == name {
		if sk := endpointWorkloadKind(s); sk == "" || kind == "" || strings.EqualFold(sk, kind) {
			return "", "", "", false
		}
	}
	return d.Namespace, name, kind, true
}

// endpointWorkloadKind is the kind of the workload an endpoint belongs to,
// when the source names one.
func endpointWorkloadKind(e pkgtraffic.Endpoint) string {
	if e.WorkloadKind != "" {
		return e.WorkloadKind
	}
	if e.Workload == "" && strings.EqualFold(e.Kind, pkgtraffic.EndpointKindPod) {
		return "Pod"
	}
	if !strings.EqualFold(e.Kind, pkgtraffic.EndpointKindPod) && !strings.EqualFold(e.Kind, pkgtraffic.EndpointKindWorkload) {
		return e.Kind
	}
	return ""
}

// unattributed is a caller with no identity: no name and no address, or
// the "unknown" a metrics source records for a peer it couldn't identify.
func unattributed(e pkgtraffic.Endpoint) bool {
	if e.IP != "" {
		return false
	}
	return cmp.Or(e.Workload, e.Name) == "" || strings.EqualFold(e.Name, "unknown") || strings.EqualFold(e.Namespace, "unknown")
}

// Caretta files a node under namespace and kind "node", and an address it
// couldn't resolve under "external", rather than leaving the namespace empty.
const (
	carettaNode     = "node"
	carettaExternal = "external"
)

// notWorkload is whether an endpoint is something other than a workload's
// Pods: outside the cluster, a node, a Service address, or unidentified.
func notWorkload(e pkgtraffic.Endpoint) bool {
	for _, k := range []string{pkgtraffic.EndpointKindExternal, pkgtraffic.EndpointKindHost, pkgtraffic.EndpointKindUnknown, "Service", carettaNode} {
		if strings.EqualFold(e.Kind, k) {
			return true
		}
	}
	return e.Namespace == carettaNode || e.Namespace == carettaExternal
}

// nodeLike is whether the source itself identified an endpoint as a node.
func nodeLike(e pkgtraffic.Endpoint) bool {
	return e.Kind == pkgtraffic.EndpointKindHost || strings.EqualFold(e.Kind, carettaNode) || e.Namespace == carettaNode
}

// trafficIndex answers whether the source saw a workload receive.
type trafficIndex struct {
	ev *TrafficEvidence
	// kinds counts the distinct kinds of the answer's workloads by
	// namespace/name: a source that names no kind matches a name only when
	// one workload has it.
	kinds map[string]int
}

func newTrafficIndex(ev *TrafficEvidence, targets []target) *trafficIndex {
	if ev == nil {
		return nil
	}
	ix := &trafficIndex{ev: ev, kinds: map[string]int{}}
	seen := map[WorkloadRef]bool{}
	for _, t := range targets {
		w := t.workload
		w.Container = ""
		if !seen[w] {
			seen[w] = true
			ix.kinds[w.Namespace+"/"+w.Name]++
		}
	}
	return ix
}

func (ix *trafficIndex) seen(w WorkloadRef) bool {
	if ix == nil {
		return false
	}
	key := w.Namespace + "/" + w.Name
	kinds := ix.ev.seen[key]
	if kinds == nil {
		return false
	}
	return kinds[strings.ToLower(w.Kind)] || (kinds[""] && ix.kinds[key] == 1 && !ix.ev.shared[key])
}

// answer is the wire Traffic for the workloads in targets, at most n listed.
// The page marks only exposed workloads, so they are listed first and a cap
// leaves out unexposed ones before any it would mark.
func (ix *trafficIndex) answer(targets []target, n int, exposed func(WorkloadRef) bool) *Traffic {
	if ix == nil {
		return nil
	}
	t := &Traffic{
		Source: ix.ev.Source, FromOutside: ix.ev.FromOutside, WindowSeconds: int(ix.ev.Window / time.Second),
		HistoryLimited: ix.ev.HistoryLimited, ObservedAt: ix.ev.At.UTC().Format(time.RFC3339), Workloads: []WorkloadRef{},
	}
	done := map[WorkloadRef]bool{}
	for _, tg := range targets {
		w := tg.workload
		w.Container = ""
		if done[w] {
			continue
		}
		done[w] = true
		if ix.seen(w) {
			t.Workloads = append(t.Workloads, w)
		}
	}
	rank := func(w WorkloadRef) int {
		if exposed != nil && exposed(w) {
			return 0
		}
		return 1
	}
	slices.SortFunc(t.Workloads, func(a, b WorkloadRef) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Kind, b.Kind))
	})
	t.WorkloadsTotal = len(t.Workloads)
	t.Workloads = t.Workloads[:min(len(t.Workloads), n)]
	return t
}
