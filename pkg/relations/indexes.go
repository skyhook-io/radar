package relations

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ReferrerKey is the key a family's index files its sources under for a
// target they may name.
func ReferrerKey(target resourceid.Ref) string {
	return target.Group + "\x00" + target.Kind + "\x00" + target.Namespace + "\x00" + target.Name
}

func referrersIndex(name string) *ReferrersIndex {
	return &ReferrersIndex{Name: "relations/" + name, Key: ReferrerKey}
}

// unreadReferrerKey files a source whose declarations couldn't all be read.
// Every reverse search reads it too, so the read error reaches the answer as a
// gap: returning it from the index function would panic the store instead.
const unreadReferrerKey = "\x00unread"

// IndexFunc files every alternative a declaration can resolve to. Discovery,
// access and controller resolution are deliberately deferred to Referrers.
func (f *Family) IndexFunc() cache.IndexFunc {
	return func(obj any) ([]string, error) {
		o, ok := obj.(runtime.Object)
		if !ok {
			return nil, nil
		}
		var keys []string
		declare := f.Declared
		if f.ReferrersIndex.IndexDeclarations != nil {
			declare = f.ReferrersIndex.IndexDeclarations
		}
		err := declare(o, func(d Declaration) {
			add := func(r resourceid.Ref) {
				if !f.target(r.GroupKind()) {
					return
				}
				k := f.ReferrersIndex.Key(r)
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
			add(d.Target)
			for _, r := range d.Or {
				add(r)
			}
		})
		if err != nil {
			keys = append(keys, unreadReferrerKey)
		}
		return keys, nil
	}
}

// ReferrerIndexers registers by GroupResource so every served version and
// namespace informer receives the same indexes.
var ReferrerIndexers = func() map[schema.GroupResource]cache.Indexers {
	out := map[schema.GroupResource]cache.Indexers{}
	for _, f := range Families("") {
		if f.ReferrersIndex == nil {
			continue
		}
		for _, source := range f.Sources {
			gr := schema.GroupResource{Group: source.Kind.Group, Resource: sourceResources[source.Kind.Kind]}
			if gr.Resource == "" {
				panic("missing relation source resource: " + source.Kind.Kind)
			}
			if out[gr] == nil {
				out[gr] = cache.Indexers{}
			}
			out[gr][f.ReferrersIndex.Name] = f.IndexFunc()
		}
	}
	return out
}()

// sourceResources names the resource of each indexed family's source kinds.
var sourceResources = map[string]string{
	"Secret": "secrets", "ConfigMap": "configmaps",
	"HTTPRoute": "httproutes", "GRPCRoute": "grpcroutes", "TCPRoute": "tcproutes", "TLSRoute": "tlsroutes", "UDPRoute": "udproutes", "Gateway": "gateways",
	"VirtualService": "virtualservices", "DestinationRule": "destinationrules",
	"Trigger": "triggers", "Channel": "channels", "InMemoryChannel": "inmemorychannels",
	"ApiServerSource": "apiserversources", "PingSource": "pingsources", "ContainerSource": "containersources", "SinkBinding": "sinkbindings",
	"Certificate": "certificates", "CertificateRequest": "certificaterequests", "Order": "orders", "Challenge": "challenges",
	"Cluster": "clusters", "ScaledObject": "scaledobjects", "ScaledJob": "scaledjobs", "NodePool": "nodepools", "NodeClaim": "nodeclaims", "Machine": "machines",
}

func istioHostCandidates(obj runtime.Object, visit func(Declaration)) error {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	host := func(host string) {
		name, ns, rest := host, u.GetNamespace(), ""
		if first, tail, dotted := strings.Cut(host, "."); dotted {
			name = first
			ns, rest, _ = strings.Cut(tail, ".")
			if rest != "" && !strings.HasPrefix(rest, "svc.") {
				return
			}
		}
		if name != "" && ns != "" {
			visit(Declaration{Target: resourceid.NewRef("", "Service", ns, name)})
		}
	}
	if u.GetKind() == "DestinationRule" {
		h, _, _ := unstructured.NestedString(u.Object, "spec", "host")
		host(h)
	} else {
		configrefs.VisitIstioVirtualServiceHosts(u, func(value, _ string) { host(value) })
	}
	return nil
}
