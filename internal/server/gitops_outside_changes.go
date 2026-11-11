package server

// The outside-changes endpoint lists, for a Flux Kustomization or
// HelmRelease, which fields of the objects it manages were last written by a
// person's tool (kubectl, Lens, Radar…) rather than by Flux. The evidence is
// metadata.managedFields, which Radar's caches strip, so the members are
// listed live, as the caller, by the root's Flux labels. Only manager names,
// times, field paths and an allowlist of scalar values leave the server, and
// Secrets are never listed: a LIST would return their data.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/gitops/fieldwriters"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const maxOutsideListGroups = 40

type outsideRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type outsideChange struct {
	Manager   string            `json:"manager"`
	Tool      string            `json:"tool"`
	Operation string            `json:"operation"`
	Time      string            `json:"time,omitempty"`
	Paths     []string          `json:"paths"`
	MorePaths int               `json:"morePaths"`
	Values    map[string]string `json:"values,omitempty"`
	Outcome   string            `json:"outcome"`
	Harmless  bool              `json:"harmless"`
}

type outsideObject struct {
	Ref     outsideRef      `json:"ref"`
	Changes []outsideChange `json:"changes"`
}

type outsideToolCount struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

type outsideOtherWriter struct {
	Manager string `json:"manager"`
	Objects int    `json:"objects"`
}

type outsideDenied struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
}

type outsideCoverage struct {
	Listed     int             `json:"listed"`
	Denied     []outsideDenied `json:"denied,omitempty"`
	Skipped    []string        `json:"skipped,omitempty"`
	NotChecked []string        `json:"notChecked,omitempty"`
	// LabelScan: the release records no inventory, so a fixed set of common
	// kinds was listed by Flux's labels; other kinds weren't checked.
	LabelScan bool `json:"labelScan,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

type outsidePolicy struct {
	Tool           string `json:"tool"`
	DriftDetection string `json:"driftDetection,omitempty"`
	Suspended      bool   `json:"suspended"`
	Interval       string `json:"interval,omitempty"`
}

type outsideChangesResponse struct {
	Root         outsideRef                    `json:"root"`
	Policy       *outsidePolicy                `json:"policy,omitempty"`
	Count        int                           `json:"count"`
	Objects      []outsideObject               `json:"objects"`
	Harmless     []outsideToolCount            `json:"harmless"`
	OtherWriters []outsideOtherWriter          `json:"otherWriters"`
	DriftEvents  []gitopsinsights.EventSummary `json:"driftEvents"`
	Coverage     outsideCoverage               `json:"coverage"`
}

// classifyOutsideObject reads one member's managedFields. Subresource writes
// (status, scale) are controllers', never a hand edit.
func classifyOutsideObject(obj *unstructured.Unstructured, owner *topology.ResourceRef, ownerObj *unstructured.Unstructured) (out outsideObject, harmless []string, otherManagers []string) {
	group := resourceid.GroupFromAPIVersion(obj.GetAPIVersion())
	out.Ref = outsideRef{Group: group, Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	ref := writeEvidenceRef{Kind: obj.GetKind(), Group: group, Namespace: obj.GetNamespace(), Name: obj.GetName()}
	owners := ownerManagers(owner)
	var policy *gitOpsWritePolicy
	if ownerObj != nil {
		policy = readGitOpsWritePolicy(owner, ownerObj, obj)
	}
	for _, mf := range obj.GetManagedFields() {
		if mf.Subresource != "" {
			continue
		}
		switch fieldwriters.Classify(mf.Manager, owners, radarWriteManagers) {
		case fieldwriters.ClassOwner:
			continue
		case fieldwriters.ClassOther:
			otherManagers = append(otherManagers, mf.Manager)
			continue
		}
		var raw []byte
		if mf.FieldsV1 != nil {
			raw = mf.FieldsV1.Raw
		}
		paths, err := fieldwriters.LeafPaths(raw)
		if err != nil {
			continue
		}
		kept, label := fieldwriters.SplitHarmless(paths)
		if label != "" {
			harmless = append(harmless, label)
		}
		if len(kept) == 0 {
			continue
		}
		change := outsideChange{Manager: mf.Manager, Tool: fieldwriters.ToolName(mf.Manager, radarWriteManagers), Operation: string(mf.Operation)}
		if mf.Time != nil {
			change.Time = mf.Time.UTC().Format(time.RFC3339)
		}
		if len(kept) > fieldwriters.MaxPaths {
			change.MorePaths = len(kept) - fieldwriters.MaxPaths
			kept = kept[:fieldwriters.MaxPaths]
		}
		change.Paths = kept
		ignore := "effective"
		for _, p := range kept {
			if v, ok := outsideValue(obj.Object, p); ok {
				if change.Values == nil {
					change.Values = map[string]string{}
				}
				change.Values[p] = v
			}
			segs, err := parseWriteFieldPath(p)
			if err != nil || policy == nil {
				ignore = ""
				continue
			}
			if covered, _ := ignoreRuleFor(owner, ownerObj, policy, obj, ref, segs); covered != "effective" {
				ignore = ""
			}
		}
		change.Outcome = outsideOutcome(policy, ignore)
		out.Changes = append(out.Changes, change)
	}
	return out, harmless, otherManagers
}

// outsideOutcome is what the root's policy says Flux does about a hand edit.
// The UI owns the sentence for each code.
func outsideOutcome(policy *gitOpsWritePolicy, ignore string) string {
	switch {
	case policy == nil:
		return "unknown"
	case policy.Suspended != nil && *policy.Suspended:
		return "suspended"
	case policy.ObjectReconcile != "" || ignore == "effective":
		return "ignored"
	case policy.DriftDetection == "":
		return "may-overwrite"
	case policy.DriftDetection == "enabled":
		return "reverted"
	case policy.DriftDetection == "warn":
		return "reported"
	}
	return "stays"
}

// outsideValue returns the live value of a path only when it is a scalar
// on the allowlist: replicas, container images and resource requests and
// limits. Env values, labels, annotations and data never leave the server.
func outsideValue(obj map[string]any, path string) (string, bool) {
	allowed := path == "spec.replicas" ||
		(strings.HasSuffix(path, ".image") && (strings.Contains(path, "containers[") || strings.Contains(path, "initContainers["))) ||
		strings.Contains(path, ".resources.requests.") || strings.Contains(path, ".resources.limits.")
	if !allowed {
		return "", false
	}
	segs, err := parseWriteFieldPath(path)
	if err != nil {
		return "", false
	}
	var node any = obj
	for _, seg := range segs {
		switch seg.typ {
		case segKey:
			m, ok := node.(map[string]any)
			if !ok {
				return "", false
			}
			node = m[seg.key]
		case segMatch:
			list, ok := node.([]any)
			if !ok {
				return "", false
			}
			node = nil
			for _, item := range list {
				if matchesSelector(item, seg) {
					node = item
					break
				}
			}
		default:
			return "", false
		}
	}
	switch v := node.(type) {
	case string:
		return v, true
	case int64, int32, int, float64, bool:
		return fmt.Sprint(v), true
	}
	return "", false
}

type outsideListGroup struct {
	GVR       schema.GroupVersionResource
	Kind      string
	Namespace string
}

// Kinds a HelmRelease without inventory is checked for, by Flux's labels.
var outsideLabelScanKinds = []struct{ Group, Kind string }{
	{"apps", "Deployment"}, {"apps", "StatefulSet"}, {"apps", "DaemonSet"}, {"", "Service"},
	{"", "ConfigMap"}, {"networking.k8s.io", "Ingress"}, {"batch", "CronJob"}, {"batch", "Job"},
	{"", "ServiceAccount"}, {"rbac.authorization.k8s.io", "Role"}, {"rbac.authorization.k8s.io", "RoleBinding"},
	{"", "PersistentVolumeClaim"},
}

// outsideListGroups turns the tree's managed members into one LIST per
// (resource, namespace). Secrets are never listed.
func outsideListGroups(tree *gitopstree.ResourceTree, root *unstructured.Unstructured,
	resolve func(group, kind string) (schema.GroupVersionResource, bool)) (groups []outsideListGroup, cov outsideCoverage) {
	seen := map[string]bool{}
	skipped := map[string]bool{}
	notChecked := map[string]bool{}
	add := func(group, kind, namespace string) {
		if group == "" && kind == "Secret" {
			skipped["secrets"] = true
			return
		}
		gvr, ok := resolve(group, kind)
		if !ok {
			notChecked[kind] = true
			return
		}
		key := gvr.String() + "\x00" + namespace
		if seen[key] {
			return
		}
		seen[key] = true
		groups = append(groups, outsideListGroup{GVR: gvr, Kind: kind, Namespace: namespace})
	}
	if tree != nil {
		for _, n := range tree.Nodes {
			if n.Role != gitopstree.RoleDeclared || n.Dependency || n.Remote || n.Data["relationship"] != nil {
				continue
			}
			add(n.Ref.Group, n.Ref.Kind, n.Ref.Namespace)
		}
	}
	if root.GetKind() == "HelmRelease" {
		if _, present, _ := unstructured.NestedFieldNoCopy(root.Object, "status", "inventory"); !present {
			namespace, _, _ := unstructured.NestedString(root.Object, "spec", "targetNamespace")
			if namespace == "" {
				namespace = root.GetNamespace()
			}
			for _, k := range outsideLabelScanKinds {
				add(k.Group, k.Kind, namespace)
			}
			cov.LabelScan = true
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.GVR.Resource != b.GVR.Resource {
			return a.GVR.Resource < b.GVR.Resource
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.GVR.Group < b.GVR.Group
	})
	if len(groups) > maxOutsideListGroups {
		groups = groups[:maxOutsideListGroups]
		cov.Truncated = true
	}
	cov.Skipped = sortedKeys(skipped)
	cov.NotChecked = sortedKeys(notChecked)
	return groups, cov
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fluxMemberSelector selects the objects Flux labels as the root's.
func fluxMemberSelector(root *unstructured.Unstructured) string {
	prefix := "kustomize.toolkit.fluxcd.io"
	if root.GetKind() == "HelmRelease" {
		prefix = "helm.toolkit.fluxcd.io"
	}
	return fmt.Sprintf("%s/name=%s,%s/namespace=%s", prefix, root.GetName(), prefix, root.GetNamespace())
}
