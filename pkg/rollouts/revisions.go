package rollouts

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// Revision is one entry in a Rollout's history, derived from an owned ReplicaSet.
type Revision struct {
	Number    int64     `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	Image     string    `json:"image"`
	Replicas  int64     `json:"replicas"`
	Template  string    `json:"template,omitempty"`
	PodHash   string    `json:"podHash,omitempty"`
	// IsCurrent marks the revision the Rollout is currently rolling out
	// (status.currentPodHash), which during a canary is NOT the one serving
	// stable traffic.
	IsCurrent bool `json:"isCurrent"`
	// IsStable marks the revision serving stable traffic (status.stableRS) — the
	// version an abort reverts to.
	IsStable bool `json:"isStable"`
}

// workloadRefTarget locates the pod template on a spec.workloadRef target.
type workloadRefTarget struct {
	gvr schema.GroupVersionResource
	// PodTemplate keeps its template at the root; the apps kinds nest it under spec.
	templatePath []string
}

// A workloadRef Rollout has no spec.template, so an undo patches the ref instead.
var workloadRefTargets = map[string]workloadRefTarget{
	"Deployment":  {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, templatePath: specTemplatePath},
	"ReplicaSet":  {gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, templatePath: specTemplatePath},
	"PodTemplate": {gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "podtemplates"}, templatePath: []string{"template"}},
}

var specTemplatePath = []string{"spec", "template"}

type TemplateTarget struct {
	GVR          schema.GroupVersionResource
	Name         string
	TemplatePath []string
}

func ResolveTemplateTarget(ro *unstructured.Unstructured) (TemplateTarget, error) {
	target := TemplateTarget{
		GVR:          GVR,
		Name:         ro.GetName(),
		TemplatePath: specTemplatePath,
	}
	refKind, refName, ok := WorkloadRef(ro)
	if !ok {
		return target, nil
	}
	ref, supported := workloadRefTargets[refKind]
	apiVersion, _, _ := unstructured.NestedString(ro.Object, "spec", "workloadRef", "apiVersion")
	refGVK := schema.FromAPIVersionAndKind(apiVersion, refKind)
	if !supported || refGVK.Group != ref.gvr.Group {
		return TemplateTarget{}, fmt.Errorf("Rollout %s/%s references a %s: %w", ro.GetNamespace(), ro.GetName(), refKind, ErrWorkloadRefUnsupported)
	}
	target.GVR = ref.gvr
	target.Name = refName
	target.TemplatePath = ref.templatePath
	return target, nil
}

// ListRevisions returns the Rollout's revision history, newest first.
func ListRevisions(ctx context.Context, client dynamic.Interface, namespace, name string) ([]Revision, error) {
	ro, err := client.Resource(GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Rollout %s/%s: %w", namespace, name, err)
	}

	rsList, err := client.Resource(replicaSetGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list ReplicaSets in %s: %w", namespace, err)
	}

	return BuildRevisions(rsList.Items, ro), nil
}

// ownedRevision pairs an owned ReplicaSet with its parsed revision number.
type ownedRevision struct {
	rs     *unstructured.Unstructured
	number int64
}

// ownedRevisions returns ro's ReplicaSets that carry a revision number, newest first.
func ownedRevisions(replicaSets []unstructured.Unstructured, ro *unstructured.Unstructured) []ownedRevision {
	uid := string(ro.GetUID())

	var owned []ownedRevision
	for i := range replicaSets {
		rs := &replicaSets[i]
		if !ownedBy(rs, uid) {
			continue
		}
		if number, ok := revisionNumber(rs); ok {
			owned = append(owned, ownedRevision{rs: rs, number: number})
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].number > owned[j].number })
	return owned
}

// BuildRevisions derives history from the ReplicaSets owned by ro.
func BuildRevisions(replicaSets []unstructured.Unstructured, ro *unstructured.Unstructured) []Revision {
	currentHash, _, _ := unstructured.NestedString(ro.Object, "status", "currentPodHash")
	stableRS, _, _ := unstructured.NestedString(ro.Object, "status", "stableRS")

	var revisions []Revision
	for _, owned := range ownedRevisions(replicaSets, ro) {
		rs, number := owned.rs, owned.number

		podHash := rs.GetLabels()[PodTemplateHashLabel]
		replicas, _, _ := unstructured.NestedInt64(rs.Object, "spec", "replicas")

		var template string
		if tmpl, found, _ := unstructured.NestedMap(rs.Object, "spec", "template"); found && tmpl != nil {
			if out, err := yaml.Marshal(tmpl); err == nil {
				template = string(out)
			}
		}

		revisions = append(revisions, Revision{
			Number:    number,
			CreatedAt: rs.GetCreationTimestamp().Time,
			Image:     primaryContainerImage(rs.Object),
			Replicas:  replicas,
			Template:  template,
			PodHash:   podHash,
			IsCurrent: podHash != "" && podHash == currentHash,
			IsStable:  podHash != "" && podHash == stableRS,
		})
	}
	return revisions
}

// Undo restores the pod template from a prior revision (<= 0 picks the previous).
// Starts a NEW rollout: steps, pauses, and analysis all re-run.
func Undo(ctx context.Context, client dynamic.Interface, namespace, name string, revision int64) (OperationResult, error) {
	ro, err := get(ctx, client, namespace, name)
	if err != nil {
		return OperationResult{}, err
	}

	rsList, err := client.Resource(replicaSetGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return OperationResult{}, fmt.Errorf("failed to list ReplicaSets in %s: %w", namespace, err)
	}

	target, targetRevision, err := selectRevision(rsList.Items, ro, revision)
	if err != nil {
		return OperationResult{}, fmt.Errorf("%w (Rollout %s/%s)", err, namespace, name)
	}

	template, found, err := unstructured.NestedMap(target.Object, "spec", "template")
	if err != nil || !found {
		return OperationResult{}, fmt.Errorf("revision %d of Rollout %s/%s has no pod template", targetRevision, namespace, name)
	}
	stripPodTemplateHash(template)

	templateTarget, err := ResolveTemplateTarget(ro)
	if err != nil {
		return OperationResult{}, err
	}
	targetGVR, targetName, templatePath := templateTarget.GVR, templateTarget.Name, templateTarget.TemplatePath
	live := ro
	if targetGVR != GVR || targetName != name {
		// The unchanged check has to read the object being patched, not the Rollout.
		if live, err = client.Resource(targetGVR).Namespace(namespace).Get(ctx, targetName, metav1.GetOptions{}); err != nil {
			return OperationResult{}, fmt.Errorf("failed to get workload %s/%s referenced by Rollout %s: %w", namespace, targetName, name, err)
		}
	}

	if templateMatches(live, templatePath, template) {
		return OperationResult{}, fmt.Errorf("revision %d of Rollout %s/%s: %w", targetRevision, namespace, name, ErrTemplateUnchanged)
	}

	patch, err := json.Marshal([]map[string]any{{
		"op":    "replace",
		"path":  "/" + strings.Join(templatePath, "/"),
		"value": template,
	}})
	if err != nil {
		return OperationResult{}, fmt.Errorf("failed to build undo patch: %w", err)
	}

	if _, err := client.Resource(targetGVR).Namespace(namespace).Patch(
		ctx, targetName, types.JSONPatchType, patch, metav1.PatchOptions{},
	); err != nil {
		return OperationResult{}, fmt.Errorf("failed to roll back to revision %d: %w", targetRevision, err)
	}

	return OperationResult{
		Message:   fmt.Sprintf("Rollout %s/%s: rollback to revision %d initiated — the controller starts the new rollout", namespace, name, targetRevision),
		Operation: "undo",
		Namespace: namespace,
		Name:      name,
		Revision:  targetRevision,
	}, nil
}

// selectRevision finds the ReplicaSet for the requested revision, or the
// second-newest when revision <= 0.
func selectRevision(replicaSets []unstructured.Unstructured, ro *unstructured.Unstructured, revision int64) (*unstructured.Unstructured, int64, error) {
	owned := ownedRevisions(replicaSets, ro)

	if revision > 0 {
		for _, c := range owned {
			if c.number == revision {
				return c.rs, c.number, nil
			}
		}
		return nil, 0, fmt.Errorf("revision %d: %w", revision, ErrRevisionNotFound)
	}

	if len(owned) < 2 {
		return nil, 0, fmt.Errorf("no previous revision to roll back to: %w", ErrRevisionNotFound)
	}
	return owned[1].rs, owned[1].number, nil
}

// Both sides are hash-stripped: a differing pod-template-hash is not a real diff.
func templateMatches(obj *unstructured.Unstructured, templatePath []string, candidate map[string]any) bool {
	live, found, err := unstructured.NestedMap(obj.Object, templatePath...)
	if err != nil || !found {
		return false
	}
	stripPodTemplateHash(live)
	return reflect.DeepEqual(live, candidate)
}

func stripPodTemplateHash(template map[string]any) {
	labels, found, err := unstructured.NestedMap(template, "metadata", "labels")
	if err != nil || !found {
		return
	}
	if _, present := labels[PodTemplateHashLabel]; !present {
		return
	}
	delete(labels, PodTemplateHashLabel)
	if len(labels) == 0 {
		unstructured.RemoveNestedField(template, "metadata", "labels")
		return
	}
	_ = unstructured.SetNestedMap(template, labels, "metadata", "labels")
}

// WorkloadRef reports the spec.workloadRef a Rollout defers its pod template to.
func WorkloadRef(ro *unstructured.Unstructured) (kind, name string, ok bool) {
	kind, _, _ = unstructured.NestedString(ro.Object, "spec", "workloadRef", "kind")
	name, _, _ = unstructured.NestedString(ro.Object, "spec", "workloadRef", "name")
	return kind, name, kind != "" && name != ""
}

func ownedBy(obj *unstructured.Unstructured, uid string) bool {
	if uid == "" {
		return false
	}
	for _, ref := range obj.GetOwnerReferences() {
		if string(ref.UID) == uid {
			return true
		}
	}
	return false
}

func revisionNumber(rs *unstructured.Unstructured) (int64, bool) {
	raw, ok := rs.GetAnnotations()[RevisionAnnotation]
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}

func primaryContainerImage(obj map[string]any) string {
	containers, found, _ := unstructured.NestedSlice(obj, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return ""
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return ""
	}
	image, _ := container["image"].(string)
	return image
}
