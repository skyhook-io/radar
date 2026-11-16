package topology

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// displayOnlyEdges are edges the Topology screen draws that no relation family
// answers, each with the reason. A key is a whole class,
// "<edge type> <source kind> -> <target kind>" with group-qualified kinds, or
// one edge, "<source key> -> <target key>".
var displayOnlyEdges = map[string]string{
	"routes-to Service -> Pod": "the traffic view draws a Service's selected Pods; Related Resources lists a Service's workloads, not its Pods",
	// The Topology screen matches an owner reference by name, so it still draws
	// a replaced Deployment as owning the ReplicaSet its predecessor created.
	// Ownership follows the UID and doesn't.
	"apps|Deployment|shop|api -> apps|ReplicaSet|shop|api-6b7": "the display builder ignores owner UIDs",
	"apps|Deployment|shop|api -> |Pod|shop|api-6b7-a":          "the display builder ignores owner UIDs",
}

// The Topology screen draws relationships with the display builder; the drawer
// and every other reader answer from the relation families. An edge drawn
// between two objects that neither end's family answer names is drift: the
// screen and the drawer disagree about the cluster.
func TestDisplayEdgesAreFamilyAnswers(t *testing.T) {
	f := charCluster()
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
	s := newProjectionSession(t, session...)
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
	for _, c := range charCases() {
		topo := charBuild(t, f, c.opts())
		nodes := map[string]*Node{}
		for i := range topo.Nodes {
			nodes[topo.Nodes[i].ID] = &topo.Nodes[i]
		}
		for _, e := range topo.Edges {
			src, dst := nodes[e.Source], nodes[e.Target]
			if src == nil || dst == nil {
				continue
			}
			sk, dk := displayNodeKey(src), displayNodeKey(dst)
			if objects[sk] == nil || objects[dk] == nil {
				continue // a node that draws no single object, such as a grouping
			}
			class := fmt.Sprintf("%s %s -> %s", e.Type, displayKindKey(src), displayKindKey(dst))
			drawn[class]++
			if answer(sk)[dk] || answer(dk)[sk] {
				continue
			}
			example := fmt.Sprintf("%s -> %s", sk, dk)
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
