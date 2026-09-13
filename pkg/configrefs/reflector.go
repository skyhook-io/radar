package configrefs

import (
	"cmp"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const reflectorPrefix = "reflector.v1.k8s.emberstack.com/"

type Ref struct{ Kind, Namespace, Name string }

func (r Ref) Key() string { return resourceid.ResourceKey("", r.Kind, r.Namespace, r.Name) }

type Object struct {
	Ref
	Annotations map[string]string
}

func Metadata(kind string, obj metav1.Object) Object {
	return Object{Ref: Ref{kind, obj.GetNamespace(), obj.GetName()}, Annotations: obj.GetAnnotations()}
}

type Reflection struct{ Source, Mirror Ref }

type Reflections struct {
	Links      []Reflection
	Sources    map[Ref]bool
	Automatic  map[Ref]bool
	Unresolved map[Ref]bool
}

// BuildReflections resolves declared dependencies only between observed endpoints.
// It does not assert that the controller has synchronized their contents.
func BuildReflections(objects []Object) Reflections {
	result := Reflections{Sources: map[Ref]bool{}, Automatic: map[Ref]bool{}, Unresolved: map[Ref]bool{}}
	observed := map[Ref]bool{}
	for _, o := range objects {
		if o.Kind == "Secret" || o.Kind == "ConfigMap" {
			observed[o.Ref] = true
		}
	}
	seen := map[Reflection]bool{}
	for _, o := range objects {
		if !observed[o.Ref] {
			continue
		}
		if strings.EqualFold(o.Annotations[reflectorPrefix+"reflection-allowed"], "true") {
			result.Sources[o.Ref] = true
		}
		ns, name, ok := strings.Cut(o.Annotations[reflectorPrefix+"reflects"], "/")
		if !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
			continue
		}
		source := Ref{o.Kind, ns, name}
		if source == o.Ref {
			continue
		}
		if !observed[source] {
			result.Unresolved[o.Ref] = true
			continue
		}
		link := Reflection{Source: source, Mirror: o.Ref}
		result.Sources[source] = true
		if !seen[link] {
			result.Links = append(result.Links, link)
			seen[link] = true
		}
		if strings.EqualFold(o.Annotations[reflectorPrefix+"auto-reflects"], "true") {
			result.Automatic[o.Ref] = true
		}
	}
	slices.SortFunc(result.Links, func(a, b Reflection) int {
		if c := cmp.Compare(a.Source.Key(), b.Source.Key()); c != 0 {
			return c
		}
		return cmp.Compare(a.Mirror.Key(), b.Mirror.Key())
	})
	return result
}

func (r Reflections) PropagateUse(used map[Ref]bool) {
	parents := map[Ref][]Ref{}
	for _, link := range r.Links {
		parents[link.Mirror] = append(parents[link.Mirror], link.Source)
	}
	queue := make([]Ref, 0, len(used))
	for ref, yes := range used {
		if yes {
			queue = append(queue, ref)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, parent := range parents[queue[i]] {
			if !used[parent] {
				used[parent] = true
				queue = append(queue, parent)
			}
		}
	}
}
