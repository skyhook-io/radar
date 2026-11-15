package relations

import (
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Families lists the reference families; Istio hosts are resolved with domain.
func Families(domain string) []*Family {
	return []*Family{ConfigReferences, Autoscalers, KEDAAuthentication, IngressBackends,
		RolloutServices, CertificateSecrets, GatewayParents, GatewayBackends, GatewayClasses,
		CertManagerIssuers, FluxSources, NodeClasses, ClusterClasses, KnativeEventDestinations,
		KnativeTriggerBrokers, KnativeRouteRevisions, IstioGateways, IstioServiceHosts(domain),
		ArgoWorkflowTemplates, TraefikBackends, TraefikRouteConfig, TraefikSecrets,
		ContourBackends, ContourSecrets, PodPlacement, Reflections, Storage, MachineNodes,
		AnalysisRuns, SealedSecrets}
}

// ReferrerKey keeps a restricted read in its referrers' namespace buckets.
// "*" asks the store adapter to enumerate its namespace buckets; it is never stored.
func ReferrerKey(target resourceid.Ref, namespace string) string {
	return target.Group + "\x00" + target.Kind + "\x00" + target.Namespace + "\x00" + target.Name + "\x00" + namespace
}

func referrersIndex(name string) *ReferrersIndex {
	return &ReferrersIndex{Name: "relations/" + name, Key: ReferrerKey}
}

// IndexFunc files every alternative a declaration can resolve to. Discovery,
// access and controller resolution are deliberately deferred to Referrers.
func (f *Family) IndexFunc() cache.IndexFunc {
	return func(obj any) ([]string, error) {
		o, ok := obj.(runtime.Object)
		if !ok {
			return nil, nil
		}
		m, err := apimeta.Accessor(o)
		if err != nil {
			return nil, err
		}
		var keys []string
		declare := f.Declared
		if f.ReferrersIndex.Candidates != nil {
			declare = f.ReferrersIndex.Candidates
		}
		err = declare(o, func(d Declaration) {
			add := func(r resourceid.Ref) {
				if !f.target(r.GroupKind()) {
					return
				}
				k := f.ReferrersIndex.Key(r, m.GetNamespace())
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
			add(d.Target)
			for _, r := range d.Or {
				add(r)
			}
		})
		return keys, err
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

var sourceResources = map[string]string{
	"Secret": "secrets", "ConfigMap": "configmaps", "PersistentVolume": "persistentvolumes", "PersistentVolumeClaim": "persistentvolumeclaims",
	"HTTPRoute": "httproutes", "GRPCRoute": "grpcroutes", "TCPRoute": "tcproutes", "TLSRoute": "tlsroutes", "UDPRoute": "udproutes", "Gateway": "gateways",
	"VirtualService": "virtualservices", "DestinationRule": "destinationrules",
	"IngressRoute": "ingressroutes", "IngressRouteTCP": "ingressroutetcps", "IngressRouteUDP": "ingressrouteudps", "TraefikService": "traefikservices", "Middleware": "middlewares",
	"HTTPProxy": "httpproxies", "Trigger": "triggers", "Channel": "channels", "InMemoryChannel": "inmemorychannels",
	"ApiServerSource": "apiserversources", "PingSource": "pingsources", "ContainerSource": "containersources", "SinkBinding": "sinkbindings",
	"Kustomization": "kustomizations", "HelmRelease": "helmreleases", "HelmChart": "helmcharts",
	"Certificate": "certificates", "CertificateRequest": "certificaterequests", "Order": "orders", "Challenge": "challenges",
	"Cluster": "clusters", "Workflow": "workflows", "CronWorkflow": "cronworkflows",
	"ScaledObject": "scaledobjects", "ScaledJob": "scaledjobs", "NodePool": "nodepools", "NodeClaim": "nodeclaims", "Machine": "machines",
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
