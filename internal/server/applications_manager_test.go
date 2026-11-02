package server

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func appTestObject(group, kind, namespace, name string) ownershiptest.Object {
	version := "v1"
	if group != "" {
		version = group + "/v1"
	}
	return ownershiptest.Object{Ref: resourceid.NewRef(group, kind, namespace, name), APIVersion: version, UID: types.UID(group + "/" + kind + "/" + namespace + "/" + name)}
}

func appManagerView(t *testing.T, objects ...ownershiptest.Object) *ownership.View {
	t.Helper()
	world := ownershiptest.World{Objects: objects}
	seen := map[resourceid.GroupKind]bool{}
	for _, obj := range objects {
		gk := obj.Ref.GroupKind()
		if seen[gk] {
			continue
		}
		seen[gk] = true
		version := obj.APIVersion[strings.LastIndex(obj.APIVersion, "/")+1:]
		world.Resources = append(world.Resources, ownership.ServedResource{Group: gk.Group, Kind: gk.Kind, Version: version, Resource: strings.ToLower(gk.Kind) + "s", Namespaced: obj.Ref.Namespace != "", Verbs: []string{"get", "list", "watch"}})
	}
	session, err := ownershiptest.Scan(world).Begin()
	if err != nil {
		t.Fatal(err)
	}
	return ownership.NewView(session, ownership.Unrestricted())
}

// The Applications build answers each workload's source from the caches'
// manager evidence, and a Deployment a controller generated takes its
// controller's answer rather than the Helm annotations it inherited.
func TestListApplicationsReadsSourceFromManagerEvidence(t *testing.T) {
	withOwnershipDiscovery(t)
	controller := true
	helm := map[string]string{"meta.helm.sh/release-name": "checkout", "meta.helm.sh/release-namespace": "prod"}
	release := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "checkout", UID: "checkout", Annotations: helm}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "owner", UID: "owner"}}
	copied := map[string]string{"meta.helm.sh/release-name": "other", "meta.helm.sh/release-namespace": "prod"}
	inherited := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "inherited", UID: "inherited", Annotations: copied,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &controller}}}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), release, rs, inherited)...))
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)

	resp, err := ListApplications(context.Background(), []string{"prod"})
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]*appSourceRef{}
	for _, row := range resp.Applications {
		for _, w := range row.Workloads {
			sources[w.Name] = row.SourceRef
		}
	}
	if got := sources["checkout"]; got == nil || got.Type != "helm" || got.Namespace != "prod" || got.Name != "checkout" {
		t.Fatalf("native Helm source = %+v", got)
	}
	if got, ok := sources["inherited"]; !ok || got != nil {
		t.Fatalf("generated Deployment kept inherited Helm source: present=%v source=%+v", ok, got)
	}
}

// Workflow template rows pass their object as a raw map; their source is still
// read from the managers' evidence.
func TestWorkloadSourceRefReadsTemplateRows(t *testing.T) {
	template := appTestObject("argoproj.io", "WorkflowTemplate", "jobs", "nightly")
	app := appTestObject("argoproj.io", "Application", "argocd", "jobs")
	app.Membership, app.Members = ownership.MembersRecorded, []resourceid.Ref{template.Ref}
	view := appManagerView(t, template, app)
	raw := map[string]any{
		"apiVersion": template.APIVersion,
		"kind":       template.Ref.Kind,
		"metadata":   map[string]any{"namespace": "jobs", "name": "nightly", "uid": string(template.UID)},
	}
	got := workloadSourceRef(view, template.Ref, raw)
	want := &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "jobs"}
	if got == nil || !sameSourceRef(got, want) {
		t.Fatalf("template source = %+v, want %+v", got, want)
	}
}

// A workload's source is the manager its recorded membership and claims
// lead with, through its nearest controller ancestor, not whatever its labels
// name: a manager elsewhere that records it counts, and a label its manager
// doesn't back, or one copied from another object, doesn't.
func TestSourceRefFromManager(t *testing.T) {
	controller := true
	argo := &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "shop"}
	kustomization := &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}
	release := &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "podinfo"}
	nativeHelm := &appSourceRef{Type: "helm", Tool: "helm", Kind: "HelmRelease", Namespace: "prod", Name: "checkout"}
	origin := map[string]string{"helm.toolkit.fluxcd.io/name": "podinfo", "helm.toolkit.fluxcd.io/namespace": "flux-system"}

	for _, tc := range []struct {
		name  string
		build func(subject *ownershiptest.Object) []ownershiptest.Object
		want  *appSourceRef
	}{{
		name: "recorded by a Kustomization in another namespace",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			ks := appTestObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps")
			ks.Membership, ks.Members = ownership.MembersRecorded, []resourceid.Ref{subject.Ref}
			return []ownershiptest.Object{ks}
		},
		want: kustomization,
	}, {
		name: "origin labels of a release without inventory",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			hr := appTestObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "podinfo")
			hr.Membership = ownership.MembersUnreadable
			subject.Labels = origin
			return []ownershiptest.Object{hr}
		},
		want: release,
	}, {
		name: "origin labels a recorded inventory contradicts",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			hr := appTestObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "podinfo")
			hr.Membership = ownership.MembersRecorded
			ks := appTestObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps")
			ks.Membership, ks.Members = ownership.MembersRecorded, []resourceid.Ref{subject.Ref}
			subject.Labels = origin
			return []ownershiptest.Object{hr, ks}
		},
	}, {
		name: "chart instance label only",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			subject.Labels = map[string]string{"app.kubernetes.io/instance": "podinfo"}
			return nil
		},
	}, {
		name: "Argo CD tracking ID copied from another object",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			app := appTestObject("argoproj.io", "Application", "argocd", "shop")
			app.Membership = ownership.MembersRecorded
			subject.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:prod/original"}
			return []ownershiptest.Object{app}
		},
	}, {
		name: "operator resource an Application records",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			operand := appTestObject("example.io", "Widget", "prod", "w")
			app := appTestObject("argoproj.io", "Application", "argocd", "shop")
			app.Membership, app.Members = ownership.MembersRecorded, []resourceid.Ref{operand.Ref}
			subject.OwnerReferences = []metav1.OwnerReference{{APIVersion: operand.APIVersion, Kind: operand.Ref.Kind, Name: operand.Ref.Name, UID: operand.UID, Controller: &controller}}
			return []ownershiptest.Object{operand, app}
		},
		want: argo,
	}, {
		name: "native Helm release annotations",
		build: func(subject *ownershiptest.Object) []ownershiptest.Object {
			subject.Annotations = map[string]string{"meta.helm.sh/release-name": "checkout", "meta.helm.sh/release-namespace": "prod"}
			return nil
		},
		want: nativeHelm,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			subject := appTestObject("apps", "Deployment", "prod", "api")
			others := tc.build(&subject)
			view := appManagerView(t, append([]ownershiptest.Object{subject}, others...)...)
			got := sourceRefFromManager(view.Manager(subject.Ref))
			if (got == nil) != (tc.want == nil) || got != nil && !sameSourceRef(got, tc.want) {
				t.Fatalf("source = %+v, want %+v", got, tc.want)
			}
		})
	}
}
