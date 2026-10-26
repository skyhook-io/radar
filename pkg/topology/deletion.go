package topology

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// PreviewDeletion projects observed owner-reference garbage collection for a
// fetched root, independently of display topology. A host must validate the
// view's session before serving it. The result assumes cascading propagation;
// an orphan delete still permits namespace, CRD and controller cleanup.
func PreviewDeletion(ctx context.Context, v *ownership.View, obj *unstructured.Unstructured) *CascadeDeletePreview {
	ref := resourceid.Ref{Group: obj.GroupVersionKind().Group, Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	p := &CascadeDeletePreview{Root: refOf(ref), Dependents: []ResourceRef{}, Basis: "ownerReferences"}
	v.Seed(ownership.Observe(ref, obj))
	subject, _ := v.References(ref)
	if subject.Access != ownership.Allowed {
		p.UnavailableReason = string(readreason.UserPermissionCheckFailed)
		if subject.Access == ownership.Denied {
			p.UnavailableReason = string(readreason.UserForbidden)
		}
		return p
	}
	if !subject.Present() {
		p.UnavailableReason = string(readreason.ReadError)
		return p
	}
	if subject.Read.Object.UID != obj.GetUID() {
		p.UnavailableReason = string(readreason.KindSyncPending)
		return p
	}
	p.RootResolved = true
	policy, pending := k8score.PendingDeletionPolicy(obj.GetFinalizers())
	if !pending {
		policy = metav1.DeletePropagationBackground
	}
	p.Propagation = string(policy)
	a := v.Deletion(ctx, ref, 1000)
	p.Coverage = &ChildrenCoverage{Complete: len(a.Gaps) == 0 && !a.Truncated, Narrowed: a.Narrowed, Truncated: a.Truncated, NotChecked: notChecked(a.Gaps)}
	for _, o := range a.Dependents {
		p.Dependents = append(p.Dependents, refOf(o.Ref))
	}
	for _, o := range a.Possible {
		p.PossibleDependents = append(p.PossibleDependents, refOf(o.Ref))
	}
	if len(obj.GetFinalizers()) > 0 {
		p.NotEnumerated = []string{"cleanup performed by unrecognized finalizers or other controllers"}
	}
	if ref.Group == "" && ref.Kind == "Namespace" {
		p.NotEnumerated = append(p.NotEnumerated, "all namespace contents; deleting a namespace removes them regardless of owner references or orphan propagation")
	}
	if ref.Group == "apiextensions.k8s.io" && ref.Kind == "CustomResourceDefinition" {
		p.NotEnumerated = append(p.NotEnumerated, "all CRD instances; deleting a CRD removes them regardless of owner references or orphan propagation")
	}
	var members []ownership.Observation
	p.ControllerTeardown, members = previewControllerTeardown(v, ref, obj)
	if p.ControllerTeardown != nil {
		p.NotEnumerated = nil
		for _, f := range obj.GetFinalizers() {
			known := f == metav1.FinalizerDeleteDependents || f == metav1.FinalizerOrphanDependents
			if p.ControllerTeardown.Controller == "Argo CD" {
				known = known || f == argoResourcesFinalizer || strings.HasPrefix(f, argoResourcesFinalizer+"/") || f == "foreground-cascade.argocd.argoproj.io"
			} else {
				known = known || fluxTeardownFinalizers[f]
			}
			if !known {
				p.NotEnumerated = []string{"cleanup performed by other finalizers"}
				break
			}
		}
	}
	storage := ref.Group == "" && (ref.Kind == "PersistentVolumeClaim" || ref.Kind == "PersistentVolume" || ref.Kind == "Namespace")
	if teardown := p.ControllerTeardown; teardown != nil {
		coverage := teardown.Coverage
		storage = storage || teardown.Destination != "local" || coverage.Membership != ownership.MembersRecorded.String() ||
			coverage.Missing > 0 || coverage.Withheld > 0 || coverage.Unknown > 0 || coverage.UnavailableReason != ""
	}
	managedCleanup := false
	for _, objects := range [][]ownership.Observation{a.Dependents, a.Possible, members} {
		for _, o := range objects {
			manager := ownership.IsRecordingManager(o.Ref.GroupKind())
			managedCleanup = managedCleanup || manager
			storage = storage || manager || o.Ref.Group == "apps" && o.Ref.Kind == "StatefulSet" || o.Ref.Group == "" && (o.Ref.Kind == "PersistentVolumeClaim" || o.Ref.Kind == "PersistentVolume" || o.Ref.Kind == "Namespace")
		}
	}
	if managedCleanup {
		p.NotEnumerated = append(p.NotEnumerated, "controller cleanup triggered by dependent GitOps managers")
	}
	if storage {
		p.MayReclaimStorage = true
		p.NotEnumerated = append(p.NotEnumerated, "backing storage/data removal governed by the PV reclaim policy")
	}
	return p
}

func previewControllerTeardown(v *ownership.View, ref resourceid.Ref, obj *unstructured.Unstructured) (*ControllerTeardown, []ownership.Observation) {
	controller, action := "", "prune"
	switch ref.GroupKind() {
	case ownership.ArgoApplication:
		controller = "Argo CD"
	case ownership.FluxKustomization:
		controller = "Flux"
	case ownership.FluxHelmRelease:
		controller, action = "Flux", "uninstall"
	default:
		return nil, nil
	}
	if !tearsDown(obj, controller, action) {
		return nil, nil
	}
	destination := "local"
	if controller == "Argo CD" {
		if !gitops.HasArgoDestination(obj) {
			destination = "unknown"
		} else if !gitops.IsInClusterDestination(obj) {
			destination = "remote"
		}
	} else if !gitops.FluxTargetsLocalCluster(obj) {
		destination = "remote"
	}
	t := &ControllerTeardown{Controller: controller, Action: action, Destination: destination, Terminating: obj.GetDeletionTimestamp() != nil}
	members := v.Members(ref)
	if members.Membership == ownership.MembersRemote && t.Destination == "local" {
		t.Destination = "unknown"
	}
	t.Coverage = &MembersCoverage{Membership: members.Membership.String(), Missing: members.Missing, Withheld: members.Withheld, Unknown: members.Unknown}
	if len(members.Gaps) > 0 {
		t.Coverage.UnavailableReason = string(members.Gaps[0].Reason)
	}
	// Membership can describe a previous destination. Never project those
	// coordinates into a different cluster's current teardown.
	if t.Destination != "local" || members.Membership != ownership.MembersRecorded {
		return t, members.Members
	}
	for _, m := range members.Members {
		if len(t.Resources) == maxOwnershipRefs {
			t.Coverage.Truncated = true
			break
		}
		t.Resources = append(t.Resources, refOf(m.Ref))
	}
	return t, members.Members
}
