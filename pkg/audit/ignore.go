package audit

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// IgnoreChecksAnnotation opts a single resource out of named checks:
//
//	radarhq.io/ignore-checks: singleReplica
//	radarhq.io/ignore-checks: hostNetwork,livenessProbeMissing
//
// The value is a comma-separated list of check IDs (see CheckRegistry).
// Names that match no check are inert. The annotation must sit on the
// resource a finding is ATTRIBUTED to — for the pod-spec family that is the
// workload (Deployment/StatefulSet/...), not the Pod it generates.
//
// Annotations, not Radar-side settings: the opt-out then travels with the
// manifest, so every operator's Radar reports the same posture.
const IgnoreChecksAnnotation = "radarhq.io/ignore-checks"

// ignoreIndex maps ResourceKey → set of checkIDs that resource opts out of.
type ignoreIndex map[string]map[string]bool

func parseIgnoredChecks(value string) map[string]bool {
	if value == "" {
		return nil
	}
	ids := make(map[string]bool)
	for _, part := range strings.Split(value, ",") {
		if id := strings.TrimSpace(part); id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func (idx ignoreIndex) add(kind string, obj metav1.Object) {
	ids := parseIgnoredChecks(obj.GetAnnotations()[IgnoreChecksAnnotation])
	if ids == nil {
		return
	}
	// Emission sites leave Finding.Group empty and buildResults backfills it
	// from the same table, so both sides of the lookup normalize identically.
	// CRD kinds aren't in the table and resolve to "" on both sides — matching
	// the group audit findings actually carry, not the object's real API group.
	key := resourceid.ResourceKey(resourceid.GroupForBuiltinKind(kind), kind, obj.GetNamespace(), obj.GetName())
	existing := idx[key]
	if existing == nil {
		idx[key] = ids
		return
	}
	for id := range ids {
		existing[id] = true
	}
}

func (idx ignoreIndex) addUnstructured(objs []*unstructured.Unstructured) {
	for _, o := range objs {
		if o == nil {
			continue
		}
		idx.add(o.GetKind(), o)
	}
}

// buildIgnoreIndex collects the per-resource check opt-outs from every subject
// inventory in the input. Subjects with no annotation cost one map lookup.
func buildIgnoreIndex(input *CheckInput) ignoreIndex {
	idx := ignoreIndex{}
	if input == nil {
		return idx
	}
	for _, o := range input.Pods {
		idx.add("Pod", o)
	}
	for _, o := range input.Deployments {
		idx.add("Deployment", o)
	}
	for _, o := range input.StatefulSets {
		idx.add("StatefulSet", o)
	}
	for _, o := range input.DaemonSets {
		idx.add("DaemonSet", o)
	}
	for _, o := range input.Jobs {
		idx.add("Job", o)
	}
	for _, o := range input.CronJobs {
		idx.add("CronJob", o)
	}
	for _, o := range input.Services {
		idx.add("Service", o)
	}
	for _, o := range input.Ingresses {
		idx.add("Ingress", o)
	}
	for _, o := range input.HorizontalPodAutoscalers {
		idx.add("HorizontalPodAutoscaler", o)
	}
	for _, o := range input.PodDisruptionBudgets {
		idx.add("PodDisruptionBudget", o)
	}
	for _, o := range input.ConfigMaps {
		idx.add("ConfigMap", o)
	}
	for _, o := range input.Secrets {
		idx.add("Secret", o)
	}
	for _, o := range input.ServiceAccounts {
		idx.add("ServiceAccount", o)
	}
	idx.addUnstructured(input.ManagedResources)
	idx.addUnstructured(input.CompositeResources)
	idx.addUnstructured(input.IngressRoutes)
	idx.addUnstructured(input.MiddlewareSubjects)
	idx.addUnstructured(input.CNPGClusters)
	return idx
}

// applyIgnoreAnnotations drops findings the subject opted out of and takes the
// same subject out of that check's evaluated denominator, so a suppressed
// failure doesn't silently inflate the pass rate.
//
// Only a subject that actually FAILED leaves the denominator — the opt-out is
// applied to findings, and a passing subject produces none. An annotated
// resource that passes therefore still counts as evaluated+passed, and fixing
// an annotated resource moves it back into the denominator.
//
// Findings arrive pre-merge, so one (resource, checkID) pair can appear several
// times (one per container). The denominator is decremented once per distinct
// pair — the same grain the tracker counts in.
func applyIgnoreAnnotations(findings []Finding, tr *evalTracker, idx ignoreIndex) []Finding {
	if len(idx) == 0 {
		return findings
	}
	type checkKey struct{ resource, checkID string }
	dropped := make(map[checkKey]bool)

	kept := findings[:0]
	for _, f := range findings {
		group := f.Group
		if group == "" {
			group = resourceid.GroupForBuiltinKind(f.Kind)
		}
		resource := resourceid.ResourceKey(group, f.Kind, f.Namespace, f.Name)
		if !idx[resource][f.CheckID] {
			kept = append(kept, f)
			continue
		}
		key := checkKey{resource, f.CheckID}
		if !dropped[key] {
			dropped[key] = true
			tr.unrecord(f.CheckID, f.Namespace)
		}
	}
	return kept
}
