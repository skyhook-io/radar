package topology

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// displayOnlyEdges are edges the Topology screen draws that no relation family
// answers, each with the reason. A key is a whole class,
// "<edge type> <source kind> -> <target kind>" with group-qualified kinds, or
// one edge, "<edge type> <source key> -> <target key>".
var displayOnlyEdges = map[string]string{
	"routes-to Service -> Pod": "the traffic view draws a Service's selected Pods; Related Resources lists a Service's workloads, not its Pods",
	// The Topology screen matches an owner reference by name, so it still draws
	// a replaced Deployment as owning the ReplicaSet its predecessor created.
	// Ownership follows the UID and doesn't.
	"manages apps|Deployment|shop|api -> apps|ReplicaSet|shop|api-6b7": "the display builder ignores owner UIDs",
	"manages apps|Deployment|shop|api -> |Pod|shop|api-6b7-a":          "the display builder ignores owner UIDs",
}

// Family projections pin type and direction. Ownership and the drawer's
// other derived links still have to name the object at the other end.
func TestDisplayEdgesAreFamilyAnswers(t *testing.T) {
	f := charCluster()
	addDisplayFamilyReplicas(f, 1)
	objects := map[string]runtime.Object{}
	var session []runtime.Object
	for _, o := range f.objects {
		obj := o.obj.(runtime.Object).DeepCopyObject()
		if obj.GetObjectKind().GroupVersionKind().Empty() {
			obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: o.group, Version: "v1", Kind: o.kind})
		}
		objects[o.key()] = obj
		session = append(session, obj)
	}
	// The builder draws the checkout Rollout's background analysis run from
	// the Rollout's status. Its object is added here, not to charCluster,
	// whose goldens record every fixture object, and without the owner
	// reference the controller sets, so only the status family answers it.
	run := charUnstructured("argoproj.io/v1alpha1", "AnalysisRun", "shop", "checkout-bg-1", nil, nil, map[string]any{"phase": "Running"})
	objects[resourceid.ResourceKey("argoproj.io", "AnalysisRun", "shop", "checkout-bg-1")] = run
	session = append(session, run)
	s := newRecordingProjectionSession(t, session...)
	projected, required := fixtureFamilyEdges(t, s)
	paired := map[[2]string]bool{}
	for e := range projected {
		paired[[2]string{e.source, e.target}] = true
	}
	answers := map[string]map[string]bool{}
	answer := func(key string) map[string]bool {
		if got, ok := answers[key]; ok {
			return got
		}
		got := answerRefKeys(projectionOwnedAnswer(t, s, objects[key], ownership.Unrestricted()))
		answers[key] = got
		return got
	}

	drift := map[string][]string{}
	drawn := map[string]int{}
	used := map[string]bool{}
	missing := map[string][]string{}
	for _, c := range charCases() {
		topo := charBuild(t, f, c.opts())
		present := map[string]bool{}
		actual := map[displayProjection]bool{}
		nodes := map[string]*Node{}
		for i := range topo.Nodes {
			nodes[topo.Nodes[i].ID] = &topo.Nodes[i]
			present[displayNodeKey(&topo.Nodes[i])] = true
		}
		for _, e := range topo.Edges {
			src, dst := nodes[e.Source], nodes[e.Target]
			if src == nil || dst == nil {
				t.Errorf("%s: edge %s -> %s names a node the topology doesn't have", c.name, e.Source, e.Target)
				continue
			}
			if syntheticDisplayKinds[src.Kind] || syntheticDisplayKinds[dst.Kind] {
				continue
			}
			sk, dk := displayNodeKey(src), displayNodeKey(dst)
			if objects[sk] == nil || objects[dk] == nil {
				edge := fmt.Sprintf("%s -> %s", sk, dk)
				for _, k := range []string{sk, dk} {
					if objects[k] == nil && !slices.Contains(missing[k], edge) {
						missing[k] = append(missing[k], edge)
					}
				}
				continue
			}
			class := fmt.Sprintf("%s %s -> %s", e.Type, displayKindKey(src), displayKindKey(dst))
			drawn[class]++
			edge := displayProjection{sk, dk, e.Type}
			actual[edge] = true
			if (paired[[2]string{sk, dk}] || paired[[2]string{dk, sk}]) && projected[edge] == "" {
				t.Errorf("%s: %s has a family answer with a different type or direction (%s -> %s)", c.name, class, sk, dk)
			}
			if answer(sk)[dk] || answer(dk)[sk] {
				continue
			}
			example := fmt.Sprintf("%s %s -> %s", e.Type, sk, dk)
			if _, ok := displayOnlyEdges[class]; ok {
				used[class] = true
				continue
			}
			if _, ok := displayOnlyEdges[example]; ok {
				used[example] = true
				continue
			}
			if !slices.Contains(drift[class], example) {
				drift[class] = append(drift[class], example)
			}
		}
		for edge, family := range required {
			if present[edge.source] && present[edge.target] && !actual[edge] {
				t.Errorf("%s: missing %s projection %s %s -> %s", c.name, family, edge.kind, edge.source, edge.target)
			}
		}
	}

	for _, key := range slices.Sorted(maps.Keys(missing)) {
		t.Errorf("the Topology screen draws edges to %s, which isn't a fixture object, so they go unchecked: %v", key, missing[key])
	}
	classes := make([]string, 0, len(drift))
	for class := range drift {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		t.Errorf("the Topology screen draws %q but no family answers %d of its %d drawn edges: %v", class, len(drift[class]), drawn[class], drift[class])
	}
	for class := range displayOnlyEdges {
		if !used[class] {
			t.Errorf("display-only entry %q matches no drawn edge; remove it", class)
		}
	}
}

type displayProjection struct {
	source, target string
	kind           EdgeType
}

func fixtureFamilyEdges(t *testing.T, s *projectionSession) (projected, required map[displayProjection]string) {
	t.Helper()
	projected = map[displayProjection]string{}
	required = map[displayProjection]string{}
	drawnFamilies := map[*relations.Family]bool{
		relations.IngressBackends: true, relations.GatewayClasses: true,
		relations.GatewayParents: true, relations.GatewayBackends: true,
		relations.ClusterClasses: true, relations.ArgoWorkflowTemplates: true,
		relations.CertificateSecrets: true,
	}
	record := func(name string, d relations.Disposition, from, to resourceid.Ref, mustDraw bool) {
		for _, rule := range d.Rules {
			if !rule.Matches(from.GroupKind(), to.GroupKind()) {
				continue
			}
			if rule.Reverse {
				from, to = to, from
			}
			e := displayProjection{from.Key(), to.Key(), EdgeType(rule.Edge)}
			projected[e] = name
			if mustDraw {
				required[e] = name
			}
			return
		}
		if mustDraw {
			t.Errorf("%s has no projection for %s -> %s", name, from, to)
		}
	}
	v := relations.NewView(s, ownership.Unrestricted()).WithLimit(0)
	families := relations.Families("")
	for subject, obj := range s.objects {
		for _, f := range families {
			for _, ref := range v.References(f, subject, obj).Declared {
				if ref.State == relations.TargetPresent {
					record(f.Name, f.Projection, subject, ref.Ref, drawnFamilies[f])
				}
			}
		}
		for _, f := range relations.SelectorFamilies() {
			for _, target := range v.Selected(f, subject, obj).Matches {
				record(f.Name, f.Projection, subject, target, false)
			}
		}
		for _, f := range relations.Compositions() {
			for _, edge := range v.Composed(f, subject, obj).Edges {
				record(f.Name, f.Projection, edge.Producer, edge.Consumer, false)
			}
		}
	}
	covered := slices.Collect(maps.Values(required))
	for f := range drawnFamilies {
		if !slices.Contains(covered, f.Name) {
			t.Errorf("fixture has no projected answer for %s", f.Name)
		}
	}
	return projected, required
}

// syntheticDisplayKinds are the nodes that draw no single object: a group of
// Pods, and the internet in front of an entry point.
var syntheticDisplayKinds = map[NodeKind]bool{KindPodGroup: true, KindInternet: true}

func displayNodeKey(n *Node) string {
	return resourceid.ResourceKey(nodeGroup(n), KubernetesKindForNode(n), nodeNamespaceFromData(n), n.Name)
}

func displayKindKey(n *Node) string {
	if g := nodeGroup(n); g != "" {
		return KubernetesKindForNode(n) + "." + g
	}
	return KubernetesKindForNode(n)
}

// answerRefKeys is every object a Related Resources answer names, in any field.
func answerRefKeys(rel *Relationships) map[string]bool {
	out := map[string]bool{}
	if rel == nil {
		return out
	}
	v := reflect.ValueOf(rel).Elem()
	for i := 0; i < v.NumField(); i++ {
		if !v.Type().Field(i).IsExported() {
			continue
		}
		switch field := v.Field(i).Interface().(type) {
		case []ResourceRef:
			for _, r := range field {
				out[resourceid.ResourceKey(r.Group, r.Kind, r.Namespace, r.Name)] = true
			}
		case *ResourceRef:
			if field != nil {
				out[resourceid.ResourceKey(field.Group, field.Kind, field.Namespace, field.Name)] = true
			}
		}
	}
	return out
}
