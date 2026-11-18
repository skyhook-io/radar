package relations

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
				keys = append(keys, f.ReferrersIndex.Key(r))
			}
			add(d.Target)
			for _, r := range d.Or {
				add(r)
			}
		})
		if err != nil {
			keys = append(keys, unreadReferrerKey)
		}
		slices.Sort(keys)
		return slices.Compact(keys), nil
	}
}

// ReferrerIndexers are the families' indexes by source kind, so every served
// version and namespace informer of a kind receives the same ones, whatever
// its resource is called.
var ReferrerIndexers = func() map[resourceid.GroupKind]cache.Indexers {
	out := map[resourceid.GroupKind]cache.Indexers{}
	for _, f := range Families("") {
		if f.ReferrersIndex == nil {
			continue
		}
		for _, source := range f.Sources {
			if out[source.Kind] == nil {
				out[source.Kind] = cache.Indexers{}
			}
			out[source.Kind][f.ReferrersIndex.Name] = f.IndexFunc()
		}
	}
	return out
}()

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
