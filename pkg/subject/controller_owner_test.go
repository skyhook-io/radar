package subject

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type ownerFixtureCatalog map[Ref]metav1.Object

func (c ownerFixtureCatalog) resolver() ControllerOwnerResolver {
	return ControllerOwnerResolver{Lookup: func(ref Ref) (metav1.Object, bool) {
		obj, ok := c[ref]
		return obj, ok
	}}
}

func TestControllerOwnerResolverChainParity(t *testing.T) {
	type fixture struct {
		name             string
		chain            []Ref
		ownerAPIVersions []string
		terminalOwner    *metav1.OwnerReference
	}
	nonController := false
	fixtures := []fixture{
		{
			name: "deployment",
			chain: []Ref{
				{Kind: "Pod", Namespace: "apps", Name: "web-6d8f9-abcde"},
				{Group: "apps", Kind: "ReplicaSet", Namespace: "apps", Name: "web-6d8f9"},
				{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web"},
			},
			ownerAPIVersions: []string{"apps/v1", "apps/v1"},
		},
		{
			name: "cronjob",
			chain: []Ref{
				{Kind: "Pod", Namespace: "ops", Name: "backup-29384-abcd"},
				{Group: "batch", Kind: "Job", Namespace: "ops", Name: "backup-29384"},
				{Group: "batch", Kind: "CronJob", Namespace: "ops", Name: "backup"},
			},
			ownerAPIVersions: []string{"batch/v1", "batch/v1"},
		},
		{
			name: "argo rollout",
			chain: []Ref{
				{Kind: "Pod", Namespace: "apps", Name: "canary-7548f-abcde"},
				{Group: "apps", Kind: "ReplicaSet", Namespace: "apps", Name: "canary-7548f"},
				{Group: "argoproj.io", Kind: "Rollout", Namespace: "apps", Name: "canary"},
			},
			ownerAPIVersions: []string{"apps/v1", "argoproj.io/v1alpha1"},
		},
		{
			name: "jobset",
			chain: []Ref{
				{Kind: "Pod", Namespace: "training", Name: "train-worker-0-abcde"},
				{Group: "batch", Kind: "Job", Namespace: "training", Name: "train-worker-0"},
				{Group: "jobset.x-k8s.io", Kind: "JobSet", Namespace: "training", Name: "train"},
			},
			ownerAPIVersions: []string{"batch/v1", "jobset.x-k8s.io/v1alpha2"},
		},
		{
			name: "ray service",
			chain: []Ref{
				{Kind: "Pod", Namespace: "serving", Name: "serve-raycluster-head-abcde"},
				{Group: "ray.io", Kind: "RayCluster", Namespace: "serving", Name: "serve-raycluster"},
				{Group: "ray.io", Kind: "RayService", Namespace: "serving", Name: "serve"},
			},
			ownerAPIVersions: []string{"ray.io/v1", "ray.io/v1"},
		},
		{
			name: "cloudnativepg",
			chain: []Ref{
				{Kind: "Pod", Namespace: "data", Name: "postgres-1"},
				{Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "data", Name: "postgres"},
			},
			ownerAPIVersions: []string{"postgresql.cnpg.io/v1"},
		},
		{
			name: "strimzi",
			chain: []Ref{
				{Kind: "Pod", Namespace: "data", Name: "events-brokers-0"},
				{Group: "core.strimzi.io", Kind: "StrimziPodSet", Namespace: "data", Name: "events-brokers"},
			},
			ownerAPIVersions: []string{"core.strimzi.io/v1beta2"},
			terminalOwner: &metav1.OwnerReference{
				APIVersion: "kafka.strimzi.io/v1",
				Kind:       "KafkaNodePool",
				Name:       "brokers",
				Controller: &nonController,
			},
		},
		{
			name: "crossplane controller reference",
			chain: []Ref{
				{Group: "database.example.io", Kind: "ManagedDatabase", Namespace: "data", Name: "orders"},
				{Group: "platform.example.io", Kind: "XDatabase", Namespace: "data", Name: "orders"},
			},
			ownerAPIVersions: []string{"platform.example.io/v1alpha1"},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			if len(fixture.ownerAPIVersions) != len(fixture.chain)-1 {
				t.Fatalf("fixture has %d owner API versions for %d edges", len(fixture.ownerAPIVersions), len(fixture.chain)-1)
			}
			catalog := ownerFixtureCatalog{}
			for i, ref := range fixture.chain {
				if i+1 < len(fixture.chain) {
					next := fixture.chain[i+1]
					catalog[ref] = obj(ref.Namespace, ref.Name, nil, nil,
						ctrlRef(next.Kind, next.Name, fixture.ownerAPIVersions[i]))
					continue
				}
				if fixture.terminalOwner != nil {
					catalog[ref] = obj(ref.Namespace, ref.Name, nil, nil, *fixture.terminalOwner)
					continue
				}
				catalog[ref] = obj(ref.Namespace, ref.Name, nil, nil)
			}

			resolver := catalog.resolver()
			root := fixture.chain[len(fixture.chain)-1]
			if fixture.terminalOwner != nil {
				if parent, ok := resolver.ParentOf(root); ok {
					t.Fatalf("non-controller terminal owner produced parent %+v", parent)
				}
			}
			for _, start := range fixture.chain {
				got := ResolveSubject(start, resolver, nil)
				if got.Ref != root {
					t.Errorf("ResolveSubject(%+v).Ref = %+v, want %+v", start, got.Ref, root)
				}
			}
		})
	}
}

func TestControllerOwnerResolverExactGroupIdentity(t *testing.T) {
	batchJob := Ref{Group: "batch", Kind: "Job", Namespace: "jobs", Name: "same"}
	volcanoJob := Ref{Group: "batch.volcano.sh", Kind: "Job", Namespace: "jobs", Name: "same"}
	catalog := ownerFixtureCatalog{
		batchJob:   obj("jobs", "same", nil, nil, ctrlRef("CronJob", "nightly", "batch/v1")),
		volcanoJob: obj("jobs", "same", nil, nil, ctrlRef("JobFlow", "pipeline", "batch.volcano.sh/v1alpha1")),
	}
	resolver := catalog.resolver()

	gotBatch, ok := resolver.ParentOf(batchJob)
	if !ok || gotBatch != (Ref{Group: "batch", Kind: "CronJob", Namespace: "jobs", Name: "nightly"}) {
		t.Fatalf("batch Job parent = %+v, %v", gotBatch, ok)
	}
	gotVolcano, ok := resolver.ParentOf(volcanoJob)
	if !ok || gotVolcano != (Ref{Group: "batch.volcano.sh", Kind: "JobFlow", Namespace: "jobs", Name: "pipeline"}) {
		t.Fatalf("Volcano Job parent = %+v, %v", gotVolcano, ok)
	}
	if immediate, ok := resolver.ImmediateOwner(volcanoJob); !ok || immediate != gotVolcano {
		t.Fatalf("ImmediateOwner = %+v, %v; ParentOf = %+v", immediate, ok, gotVolcano)
	}
}

func TestControllerOwnerResolverCoreGroupAndUID(t *testing.T) {
	child := Ref{Group: "example.io", Kind: "Worker", Namespace: "apps", Name: "worker"}
	owner := ctrlRef("Pod", "worker-pod", "v1")
	owner.UID = types.UID("observed-owner-uid")
	catalog := ownerFixtureCatalog{
		child: obj("apps", "worker", nil, nil, owner),
	}

	got, ok := catalog.resolver().ParentOf(child)
	want := Ref{Kind: "Pod", Namespace: "apps", Name: "worker-pod"}
	if !ok || got != want {
		t.Fatalf("ParentOf = %+v, %v; want %+v, true", got, ok, want)
	}
	owner.UID = types.UID("replacement-owner-uid")
	catalog[child] = obj("apps", "worker", nil, nil, owner)
	if replacement, ok := catalog.resolver().ParentOf(child); !ok || replacement != got {
		t.Fatalf("replacement UID changed parent identity: %+v, %v; want %+v, true", replacement, ok, got)
	}
}

func TestControllerOwnerResolverPreservesOwnerName(t *testing.T) {
	child := Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "apps", Name: "web-6d8f9"}
	resolver := ownerFixtureCatalog{
		child: obj("apps", child.Name, nil, nil, ctrlRef("Deployment", "web-controller-6d8f9", "apps/v1")),
	}.resolver()

	got, ok := resolver.ParentOf(child)
	want := Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web-controller-6d8f9"}
	if !ok || got != want {
		t.Fatalf("ParentOf = %+v, %v; want exact owner-ref name %+v, true", got, ok, want)
	}
}

func TestControllerOwnerResolverRequiresCompleteControllerReference(t *testing.T) {
	child := Ref{Kind: "Pod", Namespace: "apps", Name: "worker"}
	controller := true
	nonController := false
	tests := map[string]metav1.OwnerReference{
		"non-controller":     {APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Name: "apps", Controller: &nonController},
		"missing apiVersion": {Kind: "Deployment", Name: "worker", Controller: &controller},
		"missing kind":       {APIVersion: "apps/v1", Name: "worker", Controller: &controller},
		"missing name":       {APIVersion: "apps/v1", Kind: "Deployment", Controller: &controller},
	}
	for name, owner := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := ownerFixtureCatalog{child: obj("apps", "worker", nil, nil, owner)}.resolver()
			if got, ok := resolver.ParentOf(child); ok {
				t.Fatalf("ParentOf = %+v, true; want no evidenced parent", got)
			}
		})
	}
}

func TestControllerOwnerResolverStopsAfterUnobservedParent(t *testing.T) {
	pod := Ref{Kind: "Pod", Namespace: "training", Name: "train-worker-abcde"}
	job := Ref{Group: "batch", Kind: "Job", Namespace: "training", Name: "train-worker"}
	resolver := ownerFixtureCatalog{
		pod: obj("training", pod.Name, nil, nil, ctrlRef("Job", job.Name, "batch/v1")),
	}.resolver()

	parent, ok := resolver.ParentOf(pod)
	if !ok || parent != job {
		t.Fatalf("ParentOf(Pod) = %+v, %v; want controllerRef-evidenced %+v", parent, ok, job)
	}
	if next, ok := resolver.ParentOf(job); ok {
		t.Fatalf("ParentOf(unobserved Job) = %+v, true; want the walk to stop", next)
	}
	got := ResolveSubject(pod, resolver, nil)
	if got.Ref != job {
		t.Fatalf("ResolveSubject(Pod).Ref = %+v, want last evidenced parent %+v", got.Ref, job)
	}
}

func TestControllerOwnerResolverNilLookup(t *testing.T) {
	if got, ok := (ControllerOwnerResolver{}).ParentOf(Ref{Kind: "Pod", Namespace: "apps", Name: "worker"}); ok {
		t.Fatalf("ParentOf with nil lookup = %+v, true", got)
	}
}
