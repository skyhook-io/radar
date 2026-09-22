package k8s

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

type ReflectionLookup struct{ Cache *ResourceCache }

func (l ReflectionLookup) For(kind, namespace, name string) (*resourcecontext.ReflectionFacts, error) {
	if kind != "Secret" && kind != "ConfigMap" {
		return nil, nil
	}
	if l.Cache == nil {
		return nil, fmt.Errorf("resource cache unavailable")
	}
	var objects []configrefs.Object
	versions := map[configrefs.Ref]string{}
	add := func(o metav1.Object) {
		metadata := configrefs.Metadata(kind, o)
		objects = append(objects, metadata)
		versions[metadata.Ref] = o.GetResourceVersion()
	}
	if kind == "Secret" {
		lister := l.Cache.Secrets()
		if lister == nil {
			return nil, fmt.Errorf("Secret cache unavailable")
		}
		items, err := lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		for _, o := range items {
			add(o)
		}
	} else {
		lister := l.Cache.ConfigMaps()
		if lister == nil {
			return nil, fmt.Errorf("ConfigMap cache unavailable")
		}
		items, err := lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		for _, o := range items {
			add(o)
		}
	}
	subject := configrefs.Ref{Kind: kind, Namespace: namespace, Name: name}
	facts := &resourcecontext.ReflectionFacts{}
	for _, link := range configrefs.BuildReflections(objects).Links {
		if link.Mirror == subject {
			facts.Source = &resourcecontext.ContextRef{Kind: kind, Namespace: link.Source.Namespace, Name: link.Source.Name}
			facts.SourceResourceVersion = versions[link.Source]
		}
		if link.Source == subject {
			facts.Mirrors = append(facts.Mirrors, resourcecontext.ContextRef{Kind: kind, Namespace: link.Mirror.Namespace, Name: link.Mirror.Name})
		}
	}
	return facts, nil
}
