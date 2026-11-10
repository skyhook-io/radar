package topology

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestPreviewDeletionControllerPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, group, kind, finalizer string
		spec                         map[string]any
		membership                   ownership.MembershipState
		teardown, resources          bool
		destination                  string
	}{
		{"Argo local", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io", map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, ownership.MembersRecorded, true, true, "local"},
		{"Argo local uncached volume", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io", map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, ownership.MembersRecorded, true, false, "local"},
		{"Argo app of apps", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io", map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, ownership.MembersRecorded, true, true, "local"},
		{"Argo StatefulSet member", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io", map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}}, ownership.MembersRecorded, true, true, "local"},
		{"Argo remote", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io/background", map[string]any{"destination": map[string]any{"name": "prod"}}, ownership.MembersRemote, true, false, "remote"},
		{"Argo unknown", "argoproj.io", "Application", "resources-finalizer.argocd.argoproj.io", nil, ownership.MembersUnreadable, true, false, "unknown"},
		{"Argo no finalizer", "argoproj.io", "Application", "", nil, ownership.MembersRecorded, false, false, ""},
		{"Flux prune", "kustomize.toolkit.fluxcd.io", "Kustomization", "finalizers.fluxcd.io", map[string]any{"prune": true}, ownership.MembersRecorded, true, true, "local"},
		{"Flux orphan", "kustomize.toolkit.fluxcd.io", "Kustomization", "finalizers.fluxcd.io", map[string]any{"prune": true, "deletionPolicy": "Orphan"}, ownership.MembersRecorded, false, false, ""},
		{"Flux suspended", "kustomize.toolkit.fluxcd.io", "Kustomization", "finalizers.fluxcd.io", map[string]any{"prune": true, "suspend": true}, ownership.MembersRecorded, false, false, ""},
		{"Flux Delete overrides prune", "kustomize.toolkit.fluxcd.io", "Kustomization", "finalizers.fluxcd.io", map[string]any{"prune": false, "deletionPolicy": "Delete"}, ownership.MembersRecorded, true, true, "local"},
		{"Flux remote", "kustomize.toolkit.fluxcd.io", "Kustomization", "finalizers.fluxcd.io", map[string]any{"prune": true, "kubeConfig": map[string]any{}}, ownership.MembersRemote, true, false, "remote"},
		{"Helm uninstall without inventory", "helm.toolkit.fluxcd.io", "HelmRelease", "finalizers.fluxcd.io", nil, ownership.MembersUnreadable, true, false, "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := resourceid.Ref{Group: tc.group, Kind: tc.kind, Namespace: "demo", Name: "root"}
			member := resourceid.Ref{Kind: "ConfigMap", Namespace: "demo", Name: "member"}
			switch tc.name {
			case "Argo local uncached volume":
				member.Kind = "PersistentVolumeClaim"
			case "Argo app of apps":
				member.Group, member.Kind = "argoproj.io", "Application"
			case "Argo StatefulSet member":
				member.Group, member.Kind = "apps", "StatefulSet"
			}
			memberVersion := "v1"
			if member.Group != "" {
				memberVersion = member.Group + "/v1"
			}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1", "kind": tc.kind, "metadata": map[string]any{"name": "root", "namespace": "demo", "uid": "root", "resourceVersion": "1"}}}
			if tc.spec != nil {
				obj.Object["spec"] = tc.spec
			}
			if tc.finalizer != "" {
				obj.SetFinalizers([]string{tc.finalizer})
			}
			w := ownershiptest.World{Resources: []ownership.ServedResource{
				{Group: tc.group, Version: "v1", Kind: tc.kind, Resource: "roots", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			}, Objects: []ownershiptest.Object{{Ref: ref, APIVersion: tc.group + "/v1", UID: types.UID("root"), ResourceVersion: "1", Membership: tc.membership, Members: []resourceid.Ref{member}}, {Ref: member, APIVersion: memberVersion, UID: "member"}}}
			if member.GroupKind() != ref.GroupKind() {
				w.Resources = append(w.Resources, ownership.ServedResource{Group: member.Group, Version: "v1", Kind: member.Kind, Resource: strings.ToLower(member.Kind) + "s", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}})
			}
			if tc.name == "Argo local uncached volume" {
				w.Objects = w.Objects[:1]
			}
			s, _ := ownershiptest.Scan(w).Begin()
			p := PreviewDeletion(context.Background(), ownership.NewView(s, ownership.Unrestricted()), obj)
			if !p.RootResolved || (p.ControllerTeardown != nil) != tc.teardown {
				t.Fatalf("preview = %+v", p)
			}
			if tc.teardown && (p.ControllerTeardown.Destination != tc.destination || (len(p.ControllerTeardown.Resources) > 0) != tc.resources) {
				t.Fatalf("teardown = %+v", p.ControllerTeardown)
			}
			if len(p.Dependents) != 0 {
				t.Fatalf("inventory became garbage collection: %+v", p)
			}
			wantStorageRisk := tc.teardown && (!tc.resources || tc.destination != "local" || member.Kind == "Application" || member.Kind == "StatefulSet")
			if tc.name == "Argo app of apps" && !strings.Contains(strings.Join(p.NotEnumerated, " "), "dependent GitOps managers") {
				t.Fatalf("nested controller cleanup hidden: %+v", p)
			}
			if p.MayReclaimStorage != wantStorageRisk {
				t.Fatalf("controller storage risk = %v, want %v", p.MayReclaimStorage, wantStorageRisk)
			}
		})
	}
}

func TestPreviewDeletionRootIncarnationAndOrphanPolicy(t *testing.T) {
	w := ownershiptest.Fixture()
	var root ownershiptest.Object
	for _, o := range w.Objects {
		if o.Ref.Kind == "Deployment" && o.Ref.Name == "web" && o.Ref.Namespace == "alpha" {
			root = o
			break
		}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": root.APIVersion, "kind": root.Ref.Kind}}
	obj.SetName(root.Ref.Name)
	obj.SetNamespace(root.Ref.Namespace)
	obj.SetUID(root.UID)
	obj.SetResourceVersion(root.ResourceVersion)
	for _, mismatch := range []string{"uid", "resourceVersion", "orphan"} {
		t.Run(mismatch, func(t *testing.T) {
			u := obj.DeepCopy()
			if mismatch == "uid" {
				u.SetUID("replacement")
			}
			if mismatch == "resourceVersion" {
				u.SetResourceVersion("new")
			}
			if mismatch == "orphan" {
				u.SetFinalizers([]string{metav1.FinalizerOrphanDependents})
			}
			s, _ := ownershiptest.Scan(w).Begin()
			p := PreviewDeletion(context.Background(), ownership.NewView(s, ownership.Unrestricted()), u)
			if mismatch == "uid" {
				if p.RootResolved || p.UnavailableReason == "" {
					t.Fatalf("mismatched preview %+v", p)
				}
				return
			}
			if mismatch == "resourceVersion" {
				if !p.RootResolved {
					t.Fatalf("status lag refused %+v", p)
				}
				return
			}
			if p.Propagation != "Orphan" || len(p.Dependents) == 0 || !p.RootResolved {
				t.Fatalf("orphan preview %+v", p)
			}
		})
	}
}

func TestPreviewDeletionStorageLimitIsExplicit(t *testing.T) {
	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume"} {
		ns := ""
		if kind == "PersistentVolumeClaim" {
			ns = "demo"
		}
		ref := resourceid.Ref{Kind: kind, Namespace: ns, Name: "data"}
		w := ownershiptest.World{Resources: []ownership.ServedResource{{Version: "v1", Kind: kind, Resource: strings.ToLower(kind) + "s", Namespaced: ns != "", Verbs: []string{"get", "list", "watch", "delete"}}}, Objects: []ownershiptest.Object{{Ref: ref, APIVersion: "v1", UID: "data"}}}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": kind}}
		obj.SetNamespace(ns)
		obj.SetName("data")
		obj.SetUID("data")
		session, _ := ownershiptest.Scan(w).Begin()
		p := PreviewDeletion(context.Background(), ownership.NewView(session, ownership.Unrestricted()), obj)
		if !p.MayReclaimStorage || !strings.Contains(strings.Join(p.NotEnumerated, " "), "storage/data") {
			t.Fatalf("storage consequence hidden: %+v", p)
		}
	}
}

func TestPreviewDeletionWarnsForDependentStorage(t *testing.T) {
	for _, kind := range []string{"Pod", "StatefulSet"} {
		apiVersion, group := "v1", ""
		if kind == "StatefulSet" {
			apiVersion, group = "apps/v1", "apps"
		}
		root := ownershiptest.Object{Ref: resourceid.Ref{Group: group, Kind: kind, Namespace: "demo", Name: "root"}, APIVersion: apiVersion, UID: "root"}
		pvc := ownershiptest.Object{Ref: resourceid.Ref{Kind: "PersistentVolumeClaim", Namespace: "demo", Name: "data"}, APIVersion: "v1", UID: "data", OwnerReferences: []metav1.OwnerReference{root.OwnerRef(false)}}
		w := ownershiptest.World{Resources: []ownership.ServedResource{
			{Group: group, Version: "v1", Kind: kind, Resource: strings.ToLower(kind) + "s", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			{Version: "v1", Kind: "PersistentVolumeClaim", Resource: "persistentvolumeclaims", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
		}, Objects: []ownershiptest.Object{root, pvc}}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": "root", "namespace": "demo", "uid": "root"}}}
		s, _ := ownershiptest.Scan(w).Begin()
		p := PreviewDeletion(context.Background(), ownership.NewView(s, ownership.Unrestricted()), obj)
		if len(p.Dependents) != 1 || !p.MayReclaimStorage || !strings.Contains(strings.Join(p.NotEnumerated, " "), "storage/data") {
			t.Fatalf("%s storage consequence hidden: %+v", kind, p)
		}
	}
}
