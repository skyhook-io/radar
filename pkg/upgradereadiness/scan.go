package upgradereadiness

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	"github.com/skyhook-io/radar/pkg/topology"
)

var (
	ErrInvalidCurrentVersion = errors.New("invalid current Kubernetes version")
	ErrInvalidTargetVersion  = errors.New("invalid target Kubernetes version")
	ErrNonForwardTarget      = errors.New("target Kubernetes version must be newer than the current version")
)

func Scan(input *Input, currentVersion, targetVersion string) (*ScanResults, error) {
	current, err := parseMinor(currentVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCurrentVersion, currentVersion)
	}
	if targetVersion == "" {
		targetVersion = current.AddMinor(1).String()
	}
	target, err := parseTargetMinor(targetVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTargetVersion, targetVersion)
	}
	if !target.GreaterThan(current) {
		return nil, ErrNonForwardTarget
	}
	reviewed := utilversion.MustParseGeneric(ReviewedThrough)

	if input == nil {
		input = &Input{}
	}
	index := newWorkloadIndex(input)
	result := &ScanResults{
		CurrentVersion:  minorString(current),
		TargetVersion:   minorString(target),
		ReviewedThrough: ReviewedThrough,
		Findings:        []Finding{},
		Coverage: Coverage{
			Source: "live",
			State:  "complete",
		},
		RulesEvaluated: []RuleSummary{},
	}

	result.Coverage.UnavailableKinds = unavailableKinds(input)
	if len(result.Coverage.UnavailableKinds) > 0 {
		result.Coverage.State = "partial"
	}

	for _, r := range catalog {
		deprecatedIn := utilversion.MustParseGeneric(r.deprecatedIn)
		appliesFrom := utilversion.MustParseGeneric(r.appliesFrom)
		if target.LessThan(deprecatedIn) {
			continue
		}
		result.RulesEvaluated = append(result.RulesEvaluated, RuleSummary{ID: r.id, Title: r.title, AppliesFrom: r.appliesFrom})
		level := LevelWarning
		if target.AtLeast(appliesFrom) {
			level = LevelBlocker
		}
		result.Findings = append(result.Findings, r.scan(input, index, r, level)...)
	}

	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].Level != result.Findings[j].Level {
			return result.Findings[i].Level == LevelBlocker
		}
		a, b := result.Findings[i].Resource, result.Findings[j].Resource
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return result.Findings[i].Evidence.Path < result.Findings[j].Evidence.Path
	})

	result.Summary.Scanned = countScanned(input, index)
	for _, finding := range result.Findings {
		if finding.Level == LevelBlocker {
			result.Summary.Blockers++
		} else {
			result.Summary.Warnings++
		}
	}

	switch {
	case result.Summary.Blockers > 0:
		result.Verdict = VerdictBlocked
	case target.GreaterThan(reviewed):
		result.Verdict = VerdictUnknown
	case result.Coverage.State != "complete":
		result.Verdict = VerdictUnknown
	case result.Summary.Warnings > 0:
		result.Verdict = VerdictReview
	default:
		result.Verdict = VerdictNoKnownBlockers
	}

	return result, nil
}

var targetMinorPattern = regexp.MustCompile(`^v?\d+\.\d+$`)

func parseTargetMinor(raw string) (*utilversion.Version, error) {
	raw = strings.TrimSpace(raw)
	if !targetMinorPattern.MatchString(raw) {
		return nil, fmt.Errorf("invalid version")
	}
	return parseMinor(raw)
}

func parseMinor(raw string) (*utilversion.Version, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	v, err := utilversion.ParseGeneric(raw)
	if err != nil || v.Major() == 0 {
		return nil, fmt.Errorf("invalid version")
	}
	return utilversion.MajorMinor(v.Major(), v.Minor()), nil
}

func minorString(v *utilversion.Version) string {
	return fmt.Sprintf("%d.%d", v.Major(), v.Minor())
}

func unavailableKinds(input *Input) []string {
	var unavailable []string
	if input.Pods == nil {
		unavailable = append(unavailable, "pods")
	}
	if input.Deployments == nil {
		unavailable = append(unavailable, "deployments")
	}
	if input.ReplicaSets == nil {
		unavailable = append(unavailable, "replicasets")
	}
	if input.StatefulSets == nil {
		unavailable = append(unavailable, "statefulsets")
	}
	if input.DaemonSets == nil {
		unavailable = append(unavailable, "daemonsets")
	}
	if input.Jobs == nil {
		unavailable = append(unavailable, "jobs")
	}
	if input.CronJobs == nil {
		unavailable = append(unavailable, "cronjobs")
	}
	return unavailable
}

func countScanned(input *Input, index *workloadIndex) int {
	count := countNonNil(input.Deployments) + countNonNil(input.StatefulSets) + countNonNil(input.DaemonSets) + countNonNil(input.CronJobs)
	for _, replicaSet := range input.ReplicaSets {
		if replicaSet != nil && !index.replicaSetCoveredByDeployment(replicaSet) {
			count++
		}
	}
	for _, pod := range input.Pods {
		if pod != nil && !index.podCoveredByScannedWorkload(pod) {
			count++
		}
	}
	for _, job := range input.Jobs {
		if job != nil && !index.jobCoveredByCronJob(job) {
			count++
		}
	}
	return count
}

func countNonNil[T any](items []*T) int {
	count := 0
	for _, item := range items {
		if item != nil {
			count++
		}
	}
	return count
}

func scanGitRepo(input *Input, index *workloadIndex, r rule, level Level) []Finding {
	var findings []Finding
	for _, pod := range input.Pods {
		if pod == nil || index.podCoveredByScannedWorkload(pod) {
			continue
		}
		findings = append(findings, gitRepoFindings(pod, "Pod", "", pod.Namespace, pod.Name, pod.Spec, "spec", r, level)...)
	}
	for _, deployment := range input.Deployments {
		if deployment == nil {
			continue
		}
		findings = append(findings, gitRepoFindings(deployment, "Deployment", "apps", deployment.Namespace, deployment.Name, deployment.Spec.Template.Spec, "spec.template.spec", r, level)...)
	}
	for _, replicaSet := range input.ReplicaSets {
		if replicaSet == nil || index.replicaSetCoveredByDeployment(replicaSet) {
			continue
		}
		findings = append(findings, gitRepoFindings(replicaSet, "ReplicaSet", "apps", replicaSet.Namespace, replicaSet.Name, replicaSet.Spec.Template.Spec, "spec.template.spec", r, level)...)
	}
	for _, statefulSet := range input.StatefulSets {
		if statefulSet == nil {
			continue
		}
		findings = append(findings, gitRepoFindings(statefulSet, "StatefulSet", "apps", statefulSet.Namespace, statefulSet.Name, statefulSet.Spec.Template.Spec, "spec.template.spec", r, level)...)
	}
	for _, daemonSet := range input.DaemonSets {
		if daemonSet == nil {
			continue
		}
		findings = append(findings, gitRepoFindings(daemonSet, "DaemonSet", "apps", daemonSet.Namespace, daemonSet.Name, daemonSet.Spec.Template.Spec, "spec.template.spec", r, level)...)
	}
	for _, job := range input.Jobs {
		if job == nil || index.jobCoveredByCronJob(job) {
			continue
		}
		findings = append(findings, gitRepoFindings(job, "Job", "batch", job.Namespace, job.Name, job.Spec.Template.Spec, "spec.template.spec", r, level)...)
	}
	for _, cronJob := range input.CronJobs {
		if cronJob == nil {
			continue
		}
		findings = append(findings, gitRepoFindings(cronJob, "CronJob", "batch", cronJob.Namespace, cronJob.Name, cronJob.Spec.JobTemplate.Spec.Template.Spec, "spec.jobTemplate.spec.template.spec", r, level)...)
	}
	return findings
}

func gitRepoFindings(obj metav1.Object, kind, group, namespace, name string, spec corev1.PodSpec, pathPrefix string, r rule, level Level) []Finding {
	var findings []Finding
	resource := ResourceRef{Group: group, Kind: kind, Namespace: namespace, Name: name}
	managedBy := managedByRef(obj, resource)
	for i, volume := range spec.Volumes {
		if volume.GitRepo == nil {
			continue
		}
		impact := r.impact
		if level == LevelWarning {
			impact = r.warningImpact
		}
		findings = append(findings, Finding{
			RuleID:      r.id,
			Title:       r.title,
			Level:       level,
			Resource:    resource,
			ManagedBy:   managedBy,
			Evidence:    Evidence{Source: "live", Path: fmt.Sprintf("%s.volumes[%d].gitRepo", pathPrefix, i)},
			AppliesFrom: r.appliesFrom,
			Impact:      impact,
			Remediation: r.remediation,
			References:  append([]Reference(nil), r.references...),
		})
	}
	return findings
}

func managedByRef(obj metav1.Object, resource ResourceRef) *ResourceRef {
	refs := topology.SynthesizeManagedBy(obj, resource.Kind, resource.Namespace, resource.Name, nil, nil, nil)
	if len(refs) == 0 {
		return nil
	}
	ref := refs[0]
	return &ResourceRef{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

type workloadKey struct {
	group     string
	kind      string
	namespace string
	name      string
}

type workloadIndex struct {
	coveredPodOwners map[workloadKey]struct{}
	deployments      map[workloadKey]*appsv1.Deployment
	cronJobs         map[workloadKey]*batchv1.CronJob
}

func newWorkloadIndex(input *Input) *workloadIndex {
	index := &workloadIndex{
		coveredPodOwners: make(map[workloadKey]struct{}),
		deployments:      make(map[workloadKey]*appsv1.Deployment),
		cronJobs:         make(map[workloadKey]*batchv1.CronJob),
	}
	for _, deployment := range input.Deployments {
		if deployment == nil {
			continue
		}
		key := workloadKey{group: "apps", kind: "Deployment", namespace: deployment.Namespace, name: deployment.Name}
		index.deployments[key] = deployment
		index.coveredPodOwners[key] = struct{}{}
	}
	for _, cronJob := range input.CronJobs {
		if cronJob == nil {
			continue
		}
		key := workloadKey{group: "batch", kind: "CronJob", namespace: cronJob.Namespace, name: cronJob.Name}
		index.cronJobs[key] = cronJob
		index.coveredPodOwners[key] = struct{}{}
	}
	addWorkloads(index.coveredPodOwners, "apps", "ReplicaSet", input.ReplicaSets, func(item *appsv1.ReplicaSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "apps", "StatefulSet", input.StatefulSets, func(item *appsv1.StatefulSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "apps", "DaemonSet", input.DaemonSets, func(item *appsv1.DaemonSet) metav1.Object { return item })
	addWorkloads(index.coveredPodOwners, "batch", "Job", input.Jobs, func(item *batchv1.Job) metav1.Object { return item })
	return index
}

func addWorkloads[T any](dest map[workloadKey]struct{}, group, kind string, items []*T, object func(*T) metav1.Object) {
	for _, item := range items {
		if item == nil {
			continue
		}
		obj := object(item)
		dest[workloadKey{group: group, kind: kind, namespace: obj.GetNamespace(), name: obj.GetName()}] = struct{}{}
	}
}

func (index *workloadIndex) podCoveredByScannedWorkload(pod *corev1.Pod) bool {
	owner := controllerOwner(pod.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, pod.Namespace)
	if !ok {
		return false
	}
	_, covered := index.coveredPodOwners[key]
	return covered
}

func (index *workloadIndex) replicaSetCoveredByDeployment(replicaSet *appsv1.ReplicaSet) bool {
	owner := controllerOwner(replicaSet.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, replicaSet.Namespace)
	if !ok {
		return false
	}
	deployment, covered := index.deployments[key]
	return covered && reflect.DeepEqual(replicaSet.Spec.Template.Spec, deployment.Spec.Template.Spec)
}

func (index *workloadIndex) jobCoveredByCronJob(job *batchv1.Job) bool {
	owner := controllerOwner(job.OwnerReferences)
	if owner == nil {
		return false
	}
	key, ok := ownerKey(owner, job.Namespace)
	if !ok {
		return false
	}
	cronJob, covered := index.cronJobs[key]
	return covered && reflect.DeepEqual(job.Spec.Template.Spec, cronJob.Spec.JobTemplate.Spec.Template.Spec)
}

func ownerKey(owner *metav1.OwnerReference, namespace string) (workloadKey, bool) {
	groupVersion, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		return workloadKey{}, false
	}
	return workloadKey{group: groupVersion.Group, kind: owner.Kind, namespace: namespace, name: owner.Name}, true
}

func controllerOwner(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return &ref
		}
	}
	return nil
}
