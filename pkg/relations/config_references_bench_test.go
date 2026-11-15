package relations

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// stores is a Session over informer-style stores, read through their
// namespace index the way the cache source reads them.
type stores struct {
	ownership.ReadSession
	byKind map[resourceid.GroupKind]cache.Indexer
}

func (s stores) Indexed(gk resourceid.GroupKind, index string, keys []string, _ ownership.SearchScope) IndexedRead {
	var r IndexedRead
	st := s.byKind[gk]
	if st == nil {
		return r
	}
	for _, key := range keys {
		items, _ := st.ByIndex(index, key)
		for _, item := range items {
			obj := item.(runtime.Object)
			m, _ := apimeta.Accessor(obj)
			r.Objects = append(r.Objects, IndexedObject{Ref: resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: m.GetNamespace(), Name: m.GetName()}, Object: obj})
		}
	}
	return r
}

func (stores) Borrow(resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	return nil, ownership.ObjectRead{State: ownership.Absent}
}

// Object finds every subject present: the benchmark asks about objects the
// namespace's declarers name.
func (stores) Object(resourceid.Ref) ownership.ObjectRead {
	return ownership.ObjectRead{State: ownership.Present}
}

// appSpec declares what a typical application Pod does.
func appSpec(app string) corev1.PodSpec {
	return corev1.PodSpec{
		ServiceAccountName: app,
		ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry"}},
		Containers: []corev1.Container{{
			Name:    "app",
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: app}}}},
			Env: []corev1.EnvVar{{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: app + "-db"}, Key: "password"}}}},
		}},
		Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: app + "-tls"}}}},
	}
}

// denseNamespace holds pods Pods, two ReplicaSets per Deployment (one an old
// revision scaled to zero) and a Deployment per two Pods, all in one
// namespace.
func denseNamespace(pods int) stores {
	s := stores{byKind: map[resourceid.GroupKind]cache.Indexer{}}
	add := func(gk resourceid.GroupKind, obj runtime.Object) {
		st := s.byKind[gk]
		if st == nil {
			st = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			s.byKind[gk] = st
		}
		_ = st.Add(obj)
	}
	zero, two := int32(0), int32(2)
	for i := range pods / 2 {
		app := fmt.Sprintf("app-%05d", i)
		add(resourceid.GroupKind{Group: "apps", Kind: "Deployment"}, &appsv1.Deployment{ObjectMeta: meta(app), Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: appSpec(app)}}})
		add(resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}, &appsv1.ReplicaSet{ObjectMeta: meta(app + "-new"), Spec: appsv1.ReplicaSetSpec{Replicas: &two, Template: corev1.PodTemplateSpec{Spec: appSpec(app)}}})
		add(resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}, &appsv1.ReplicaSet{ObjectMeta: meta(app + "-old"), Spec: appsv1.ReplicaSetSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: appSpec(app)}}})
		for p := range 2 {
			add(resourceid.GroupKind{Kind: "Pod"}, &corev1.Pod{ObjectMeta: meta(fmt.Sprintf("%s-%d", app, p)), Spec: appSpec(app)})
		}
	}
	return s
}

// BenchmarkConfigConsumers answers one ConfigMap's consumers, and a Secret
// every Pod pulls with, by reading the namespace's Pods, ReplicaSets and
// Deployments and evaluating what each declares. typical is the field
// census's average namespace (68 Pods); dense is one namespace holding
// 5,000.
func BenchmarkConfigConsumers(b *testing.B) {
	for _, c := range []struct {
		name string
		pods int
	}{{"typical", 68}, {"dense", 5000}} {
		s := denseNamespace(c.pods)
		for _, subject := range []resourceid.Ref{ref("", "ConfigMap", "app-00000"), ref("", "Secret", "registry")} {
			b.Run(c.name+"/"+subject.Kind, func(b *testing.B) {
				b.ReportAllocs()
				var found int
				for b.Loop() {
					found = len(NewView(s, ownership.Unrestricted()).ConfigConsumers(subject, nil).Consumers)
				}
				b.ReportMetric(float64(found), "consumers")
			})
		}
	}
}
