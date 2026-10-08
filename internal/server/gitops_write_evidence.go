package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/topology"
)

// The write-evidence endpoint answers, before a direct write, what Radar can
// learn about whether the object's GitOps (or Helm) owner will put the old
// value back: per written field, is it in the last client-side apply payload,
// is it owned (managedFields) by the owner's controller, does an ignore rule
// cover it — plus the owner's sync policy. Both managedFields and last-applied
// are stripped from Radar's caches, so this reads the object directly, as the
// caller. Only derived facts leave the server, never the annotation or the
// managedFields themselves.

const (
	maxWriteEvidenceRequestBytes = 16 << 10
	maxWriteEvidencePaths        = 64
	lastAppliedAnnotationKey     = "kubectl.kubernetes.io/last-applied-configuration"
)

type writeEvidenceRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type gitOpsWriteEvidenceRequest struct {
	writeEvidenceRef
	Paths []string `json:"paths"`
	// Owner is the GitOps owner the client resolved (it may be inherited from
	// a parent workload). When absent, the object's own tracking metadata is
	// used.
	Owner *writeEvidenceRef `json:"owner,omitempty"`
}

type fieldOwnerEvidence struct {
	Manager     string `json:"manager"`
	Operation   string `json:"operation"`
	Subresource string `json:"subresource,omitempty"`
	// Tool is the GitOps/Helm tool the manager belongs to ("argocd",
	// "fluxcd", "helm"), empty for any other manager.
	Tool string `json:"tool,omitempty"`
	// Approximate: the manager owns an ancestor of the path (an atomic list
	// or struct), or only part of the path's subtree.
	Approximate bool `json:"approximate,omitempty"`
}

type gitOpsPathEvidence struct {
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
	// LastApplied: "present" | "absent" | "no-annotation".
	LastApplied string               `json:"lastApplied"`
	OwnedBy     []fieldOwnerEvidence `json:"ownedBy"`
	// OwnedByGitOps: a manager of the resolved owner's tool owns the path.
	OwnedByGitOps bool `json:"ownedByGitOps"`
	Approximate   bool `json:"approximate,omitempty"`
	// Partial: the path has a wildcard, and the GitOps source declares the
	// field on some of the elements it matches but not all of them.
	Partial bool `json:"partial,omitempty"`
	// Empty: the path's wildcard matches nothing in the live object (a
	// Deployment without init containers), so a write can't touch it.
	Empty bool `json:"empty,omitempty"`
	// Ignored: "" | "effective" (the owner won't revert it) |
	// "comparison-only" (Argo ignoreDifferences without
	// RespectIgnoreDifferences: no self-heal trigger, but a sync overwrites) |
	// "unevaluated" (a matching rule uses jqPathExpressions, which Radar
	// doesn't evaluate, or covers only some of the elements a wildcard path
	// matches, so it may or may not cover the field being written).
	Ignored   string `json:"ignored,omitempty"`
	IgnoredBy string `json:"ignoredBy,omitempty"`
}

type gitOpsWritePolicy struct {
	Tool      string `json:"tool"`
	Auto      *bool  `json:"auto"`
	SelfHeal  *bool  `json:"selfHeal"`
	Prune     *bool  `json:"prune"`
	Suspended *bool  `json:"suspended"`
	Interval  string `json:"interval,omitempty"`
	// Argo sync options (Application spec merged with the object's
	// argocd.argoproj.io/sync-options annotation).
	RespectIgnoreDifferences bool `json:"respectIgnoreDifferences,omitempty"`
	ServerSideApply          bool `json:"serverSideApply,omitempty"`
	Replace                  bool `json:"replace,omitempty"`
	// HelmRelease spec.driftDetection.mode: "disabled" | "warn" | "enabled".
	DriftDetection string `json:"driftDetection,omitempty"`
	// ObjectReconcile is a per-object opt-out read from the target's
	// annotations: "ignore" (Flux ssa: Ignore, reconcile: disabled, Helm
	// driftDetection: disabled) or "if-not-present" (Flux ssa: IfNotPresent —
	// recreated when deleted, never overwritten).
	ObjectReconcile string `json:"objectReconcile,omitempty"`
}

type controllerOwnerEvidence struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

type gitOpsWriteEvidenceResponse struct {
	UID             string                   `json:"uid"`
	ResourceVersion string                   `json:"resourceVersion"`
	Owner           *topology.ResourceRef    `json:"owner"`
	Policy          *gitOpsWritePolicy       `json:"policy"`
	PolicyError     string                   `json:"policyError,omitempty"`
	ControllerOwner *controllerOwnerEvidence `json:"controllerOwner,omitempty"`
	// OwnerTracksTarget: the Argo CD Application's status lists this object
	// among its resources, which confirms an owner the client matched by name.
	OwnerTracksTarget bool                 `json:"ownerTracksTarget,omitempty"`
	Paths             []gitOpsPathEvidence `json:"paths"`
}

func (s *Server) handleGitOpsWriteEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	var req gitOpsWriteEvidenceRequest
	if err := decodeBoundedJSONBody(w, r, maxWriteEvidenceRequestBytes, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "write-evidence request is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid write-evidence request: "+err.Error())
		return
	}
	if req.Kind == "" || req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name are required")
		return
	}
	if len(req.Paths) > maxWriteEvidencePaths {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d paths are allowed", maxWriteEvidencePaths))
		return
	}

	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource discovery not available")
		return
	}
	gvr, ok := discovery.GetGVRWithGroup(req.Kind, req.Group)
	if !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown kind %q in group %q", req.Kind, req.Group))
		return
	}
	dyn := s.getDynamicClientForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kubernetes client not available")
		return
	}

	target, err := dyn.Resource(gvr).Namespace(req.Namespace).Get(r.Context(), req.Name, metav1.GetOptions{})
	if err != nil {
		switch {
		case apierrors.IsForbidden(err):
			s.writeError(w, http.StatusForbidden, err.Error())
		case apierrors.IsNotFound(err):
			s.writeError(w, http.StatusNotFound, err.Error())
		default:
			log.Printf("[gitops] Failed to read %s for write evidence %s/%s: %v", sanitizeForLog(req.Kind), sanitizeForLog(req.Namespace), sanitizeForLog(req.Name), err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	req.writeEvidenceRef = canonicalWriteEvidenceRef(req.writeEvidenceRef, target)
	owner := resolveWriteEvidenceOwner(req, target)
	if owner != nil && owner.Namespace == "" && owner.Group == "argoproj.io" && strings.EqualFold(owner.Kind, "Application") {
		owner.Namespace = findTrackingArgoApplication(r.Context(), dyn, owner.Name, req.writeEvidenceRef)
	}
	var ownerObj *unstructured.Unstructured
	var ownerErr error
	if owner != nil {
		ownerGVR, gitOpsOwner := gitOpsOwnerGVR(owner)
		switch {
		case !gitOpsOwner:
			// Native Helm: no controller policy to read.
		case owner.Namespace == "":
			ownerErr = fmt.Errorf("the owner's namespace is unknown")
		default:
			ownerObj, ownerErr = dyn.Resource(ownerGVR).Namespace(owner.Namespace).Get(r.Context(), owner.Name, metav1.GetOptions{})
		}
	}

	s.writeJSON(w, buildGitOpsWriteEvidence(target, req.writeEvidenceRef, req.Paths, owner, ownerObj, ownerErr))
}

// canonicalWriteEvidenceRef takes the kind and group from the object itself:
// ignore rules name the singular Kind, and a caller may send a resource name
// ("deployments") that discovery accepted.
func canonicalWriteEvidenceRef(ref writeEvidenceRef, target *unstructured.Unstructured) writeEvidenceRef {
	if gvk := target.GroupVersionKind(); gvk.Kind != "" {
		ref.Kind, ref.Group = gvk.Kind, gvk.Group
	}
	return ref
}

// findTrackingArgoApplication finds the namespace of the Application named
// name whose status lists the object. Argo CD 3 leaves the namespace out of
// tracking ids for Applications in its own namespace, and the browser can't
// always list Applications (its namespace view may exclude Argo's namespace).
// Candidates come from the cache; each is read as the caller, so an
// Application the caller can't read is never named. Returns "" unless exactly
// one readable Application lists the object.
func findTrackingArgoApplication(ctx context.Context, dyn dynamic.Interface, name string, ref writeEvidenceRef) string {
	cache := k8s.GetResourceCache()
	if cache == nil || name == "" {
		return ""
	}
	apps, err := cache.ListDynamicWithGroup(ctx, "applications", "", "argoproj.io")
	if err != nil {
		return ""
	}
	return pickTrackingArgoApplication(ctx, dyn, apps, name, ref)
}

func pickTrackingArgoApplication(ctx context.Context, dyn dynamic.Interface, candidates []*unstructured.Unstructured, name string, ref writeEvidenceRef) string {
	found := ""
	for _, candidate := range candidates {
		if candidate.GetName() != name {
			continue
		}
		app, err := dyn.Resource(argoApplicationGVR).Namespace(candidate.GetNamespace()).Get(ctx, name, metav1.GetOptions{})
		if err != nil || !argoAppListsResource(app, ref) {
			continue
		}
		if found != "" {
			return ""
		}
		found = app.GetNamespace()
	}
	return found
}

func resolveWriteEvidenceOwner(req gitOpsWriteEvidenceRequest, target *unstructured.Unstructured) *topology.ResourceRef {
	if req.Owner != nil && req.Owner.Name != "" {
		return &topology.ResourceRef{Kind: req.Owner.Kind, Group: req.Owner.Group, Namespace: req.Owner.Namespace, Name: req.Owner.Name}
	}
	refs := topology.SynthesizeManagedBy(target, req.Kind, req.Namespace, req.Name, nil, nil, nil)
	if len(refs) == 0 {
		return nil
	}
	ref := refs[0]
	if _, gitOps := gitOpsOwnerGVR(&ref); !gitOps && !isNativeHelmOwner(&ref) {
		return nil
	}
	return &ref
}

func isNativeHelmOwner(ref *topology.ResourceRef) bool {
	return ref != nil && ref.Kind == "HelmRelease" && ref.Group == ""
}

func gitOpsOwnerGVR(ref *topology.ResourceRef) (schema.GroupVersionResource, bool) {
	if ref == nil {
		return schema.GroupVersionResource{}, false
	}
	if ref.Group == "argoproj.io" && strings.EqualFold(ref.Kind, "Application") {
		return argoApplicationGVR, true
	}
	if ref.Group == "kustomize.toolkit.fluxcd.io" || ref.Group == "helm.toolkit.fluxcd.io" {
		entry, err := gitops.ResolveFluxKind(ref.Kind)
		if err == nil && entry.GVR.Group == ref.Group {
			return entry.GVR, true
		}
	}
	return schema.GroupVersionResource{}, false
}

func ownerTool(ref *topology.ResourceRef) string {
	switch {
	case ref == nil:
		return ""
	case ref.Group == "argoproj.io":
		return "argocd"
	case ref.Group == "kustomize.toolkit.fluxcd.io" || ref.Group == "helm.toolkit.fluxcd.io":
		return "fluxcd"
	case isNativeHelmOwner(ref):
		return "helm"
	}
	return ""
}

// Field managers GitOps and Helm controllers write with. Argo CD records
// argocd-controller for server-side apply and the controller binary name for
// client-side apply.
var gitOpsFieldManagers = map[string]string{
	"argocd-controller":             "argocd",
	"argocd-application-controller": "argocd",
	"kustomize-controller":          "fluxcd",
	"helm-controller":               "fluxcd",
	"helm":                          "helm",
}

// Which manager, for a given owner, is the one that re-applies the source.
func ownerManagers(ref *topology.ResourceRef) map[string]bool {
	switch ownerTool(ref) {
	case "argocd":
		return map[string]bool{"argocd-controller": true, "argocd-application-controller": true}
	case "fluxcd":
		if ref.Group == "helm.toolkit.fluxcd.io" {
			return map[string]bool{"helm-controller": true}
		}
		return map[string]bool{"kustomize-controller": true}
	case "helm":
		return map[string]bool{"helm": true}
	}
	return nil
}

type parsedManagedFields struct {
	entry  metav1.ManagedFieldsEntry
	fields map[string]any
}

func buildGitOpsWriteEvidence(
	target *unstructured.Unstructured,
	ref writeEvidenceRef,
	paths []string,
	owner *topology.ResourceRef,
	ownerObj *unstructured.Unstructured,
	ownerErr error,
) gitOpsWriteEvidenceResponse {
	resp := gitOpsWriteEvidenceResponse{
		UID:             string(target.GetUID()),
		ResourceVersion: target.GetResourceVersion(),
		Owner:           owner,
		Paths:           []gitOpsPathEvidence{},
	}
	for _, or := range target.GetOwnerReferences() {
		if or.Controller != nil && *or.Controller {
			resp.ControllerOwner = &controllerOwnerEvidence{APIVersion: or.APIVersion, Kind: or.Kind, Name: or.Name}
			break
		}
	}

	var policy *gitOpsWritePolicy
	if owner != nil && ownerTool(owner) != "helm" {
		switch {
		case ownerErr != nil:
			resp.PolicyError = describeOwnerReadError(ownerErr)
		case ownerObj != nil:
			policy = readGitOpsWritePolicy(owner, ownerObj, target)
		}
	}
	resp.Policy = policy
	if ownerObj != nil && ownerTool(owner) == "argocd" {
		resp.OwnerTracksTarget = argoAppListsResource(ownerObj, ref)
	}

	var lastApplied any
	hasLastApplied := false
	if raw := target.GetAnnotations()[lastAppliedAnnotationKey]; strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &lastApplied); err == nil {
			hasLastApplied = true
		}
	}
	managers := ownerManagers(owner)
	var managedFields []parsedManagedFields
	for _, mf := range target.GetManagedFields() {
		if mf.FieldsV1 == nil || len(mf.FieldsV1.Raw) == 0 {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(mf.FieldsV1.Raw, &fields); err != nil {
			continue
		}
		managedFields = append(managedFields, parsedManagedFields{entry: mf, fields: fields})
	}

	for _, path := range paths {
		ev := gitOpsPathEvidence{Path: path, LastApplied: "no-annotation", OwnedBy: []fieldOwnerEvidence{}}
		segs, err := parseWriteFieldPath(path)
		if err != nil {
			ev.Error = err.Error()
			resp.Paths = append(resp.Paths, ev)
			continue
		}
		// A wildcard is judged per element of the live object: the source may
		// declare one container's image and not another's.
		elements := expandWildcards(target.Object, segs)
		if !anyAddressable(elements) {
			ev.Empty = true
			resp.Paths = append(resp.Paths, ev)
			continue
		}
		present, declared := 0, 0
		owners := map[string]*fieldOwnerEvidence{}
		var order []string
		for _, el := range elements {
			elPresent := hasLastApplied && valuePresent(lastApplied, el)
			if elPresent {
				present++
			}
			elGitOps := false
			for _, parsed := range managedFields {
				mf := parsed.entry
				owned, approximate := fieldsV1Owns(parsed.fields, el)
				if !owned {
					continue
				}
				key := mf.Manager + "\x00" + string(mf.Operation) + "\x00" + mf.Subresource
				o, seen := owners[key]
				if !seen {
					o = &fieldOwnerEvidence{Manager: mf.Manager, Operation: string(mf.Operation), Subresource: mf.Subresource, Tool: gitOpsFieldManagers[mf.Manager]}
					owners[key] = o
					order = append(order, key)
				}
				o.Approximate = o.Approximate || approximate
				if managers[mf.Manager] && mf.Subresource == "" {
					elGitOps = true
					ev.OwnedByGitOps = true
					ev.Approximate = ev.Approximate || approximate
				}
			}
			if elPresent || elGitOps {
				declared++
			}
		}
		// A manager that owns the field on only some elements owns part of the path.
		for _, key := range order {
			o := owners[key]
			if !ownsEveryElement(managedFields, o, elements) {
				o.Approximate = true
				if managers[o.Manager] && o.Subresource == "" {
					ev.Approximate = true
				}
			}
			ev.OwnedBy = append(ev.OwnedBy, *o)
		}
		if hasLastApplied {
			if present > 0 {
				ev.LastApplied = "present"
			} else {
				ev.LastApplied = "absent"
			}
		}
		ev.Partial = declared > 0 && declared < len(elements)
		ev.Ignored, ev.IgnoredBy = ignoreRuleFor(owner, ownerObj, policy, target, ref, segs, ev.OwnedBy)
		resp.Paths = append(resp.Paths, ev)
	}
	return resp
}

func describeOwnerReadError(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "you can't read the owner, so its sync policy is unknown"
	case apierrors.IsNotFound(err):
		return "the owner no longer exists"
	default:
		return err.Error()
	}
}

func evidenceBool(b bool) *bool { return &b }

func readGitOpsWritePolicy(owner *topology.ResourceRef, ownerObj, target *unstructured.Unstructured) *gitOpsWritePolicy {
	annotations := target.GetAnnotations()
	switch ownerTool(owner) {
	case "argocd":
		p := &gitOpsWritePolicy{Tool: "argocd"}
		automated, hasAutomated, _ := unstructured.NestedMap(ownerObj.Object, "spec", "syncPolicy", "automated")
		auto := hasAutomated && automated != nil
		if enabled, ok := automated["enabled"].(bool); ok && !enabled {
			auto = false
		}
		selfHeal, _ := automated["selfHeal"].(bool)
		prune, _ := automated["prune"].(bool)
		p.Auto = evidenceBool(auto)
		p.SelfHeal = evidenceBool(auto && selfHeal)
		p.Prune = evidenceBool(auto && prune)
		options, _, _ := unstructured.NestedStringSlice(ownerObj.Object, "spec", "syncPolicy", "syncOptions")
		for _, opt := range strings.Split(annotations["argocd.argoproj.io/sync-options"], ",") {
			if opt = strings.TrimSpace(opt); opt != "" {
				options = append(options, opt)
			}
		}
		for _, opt := range options {
			switch strings.TrimSpace(opt) {
			case "RespectIgnoreDifferences=true":
				p.RespectIgnoreDifferences = true
			case "ServerSideApply=true":
				p.ServerSideApply = true
			case "Replace=true":
				p.Replace = true
			}
		}
		return p
	case "fluxcd":
		suspended, _, _ := unstructured.NestedBool(ownerObj.Object, "spec", "suspend")
		interval, _, _ := unstructured.NestedString(ownerObj.Object, "spec", "interval")
		p := &gitOpsWritePolicy{Tool: "fluxcd", Auto: evidenceBool(!suspended), Suspended: evidenceBool(suspended), Interval: interval}
		if owner.Group == "helm.toolkit.fluxcd.io" {
			mode, _, _ := unstructured.NestedString(ownerObj.Object, "spec", "driftDetection", "mode")
			if mode == "" {
				mode = "disabled"
			}
			p.DriftDetection = mode
			p.SelfHeal = evidenceBool(!suspended && mode == "enabled")
			if strings.EqualFold(annotations["helm.toolkit.fluxcd.io/driftDetection"], "disabled") {
				p.ObjectReconcile = "ignore"
			}
			return p
		}
		prune, _, _ := unstructured.NestedBool(ownerObj.Object, "spec", "prune")
		p.SelfHeal = evidenceBool(!suspended)
		p.Prune = evidenceBool(prune)
		switch {
		case strings.EqualFold(annotations["kustomize.toolkit.fluxcd.io/reconcile"], "disabled"),
			strings.EqualFold(annotations["kustomize.toolkit.fluxcd.io/ssa"], "Ignore"):
			p.ObjectReconcile = "ignore"
		case strings.EqualFold(annotations["kustomize.toolkit.fluxcd.io/ssa"], "IfNotPresent"):
			p.ObjectReconcile = "if-not-present"
		}
		return p
	}
	return nil
}

// ignoreRuleFor reports whether an owner-level rule covers the path.
func ignoreRuleFor(
	owner *topology.ResourceRef,
	ownerObj *unstructured.Unstructured,
	policy *gitOpsWritePolicy,
	target *unstructured.Unstructured,
	ref writeEvidenceRef,
	segs []fieldPathSegment,
	ownedBy []fieldOwnerEvidence,
) (string, string) {
	if policy == nil || ownerObj == nil {
		return "", ""
	}
	switch policy.ObjectReconcile {
	case "ignore", "if-not-present":
		return "effective", "the object's reconcile annotation"
	}
	sets := pointerTokenSets(target.Object, segs)
	switch ownerTool(owner) {
	case "argocd":
		entries, _, _ := unstructured.NestedSlice(ownerObj.Object, "spec", "ignoreDifferences")
		covered := make([]bool, len(sets))
		managerCovers, jqRule, partialManagerMatch := false, false, false
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok || !argoIgnoreEntryMatches(entry, ref) {
				continue
			}
			markCovered(stringSlice(entry["jsonPointers"]), sets, covered)
			// A manager that owns only part of the path (one container's image
			// under containers[*]) doesn't prove the rule covers the rest.
			for _, m := range stringSlice(entry["managedFieldsManagers"]) {
				for _, o := range ownedBy {
					if o.Manager != m {
						continue
					}
					if o.Approximate {
						partialManagerMatch = true
					} else {
						managerCovers = true
					}
				}
			}
			if len(stringSlice(entry["jqPathExpressions"])) > 0 {
				jqRule = true
			}
		}
		switch {
		case managerCovers || allCovered(covered):
			if policy.RespectIgnoreDifferences {
				return "effective", "spec.ignoreDifferences"
			}
			return "comparison-only", "spec.ignoreDifferences"
		case jqRule:
			return "unevaluated", "spec.ignoreDifferences jqPathExpressions"
		case partialManagerMatch:
			return "unevaluated", "spec.ignoreDifferences managedFieldsManagers"
		case anyCovered(covered):
			return "unevaluated", "spec.ignoreDifferences jsonPointers"
		}
	case "fluxcd":
		if owner.Group != "helm.toolkit.fluxcd.io" {
			return "", ""
		}
		rules, _, _ := unstructured.NestedSlice(ownerObj.Object, "spec", "driftDetection", "ignore")
		covered := make([]bool, len(sets))
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := rule["target"].(map[string]any); ok && !fluxIgnoreTargetMatches(t, ref, target.GroupVersionKind().Version) {
				continue
			}
			markCovered(stringSlice(rule["paths"]), sets, covered)
		}
		switch {
		case allCovered(covered):
			return "effective", "spec.driftDetection.ignore"
		case anyCovered(covered):
			return "unevaluated", "spec.driftDetection.ignore"
		}
	}
	return "", ""
}

// argoIgnoreEntryMatches: kind is required, an omitted group is the core
// group, and "*" matches any group or kind.
func argoIgnoreEntryMatches(entry map[string]any, ref writeEvidenceRef) bool {
	str := func(k string) string { v, _ := entry[k].(string); return v }
	if kind := str("kind"); kind != "*" && kind != ref.Kind {
		return false
	}
	if group := str("group"); group != "*" && group != ref.Group {
		return false
	}
	if name := str("name"); name != "" && name != ref.Name {
		return false
	}
	if ns := str("namespace"); ns != "" && ns != ref.Namespace {
		return false
	}
	return true
}

// fluxIgnoreTargetMatches: a Kustomize selector, whose fields are anchored
// regular expressions and match anything when omitted. Label/annotation
// selectors aren't evaluated, so a rule carrying one (or a pattern that
// doesn't compile) never counts as covering the object.
func fluxIgnoreTargetMatches(target map[string]any, ref writeEvidenceRef, version string) bool {
	str := func(k string) string { v, _ := target[k].(string); return v }
	if str("labelSelector") != "" || str("annotationSelector") != "" {
		return false
	}
	for key, want := range map[string]string{"kind": ref.Kind, "group": ref.Group, "version": version, "name": ref.Name, "namespace": ref.Namespace} {
		pattern := str(key)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil || !re.MatchString(want) {
			return false
		}
	}
	return true
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Field paths: `a.b["x/y"][name=app][0][*].c`
// ---------------------------------------------------------------------------

type fieldPathSegmentType int

const (
	segKey fieldPathSegmentType = iota
	segIndex
	segMatch
	segAny
)

type fieldPathSegment struct {
	typ   fieldPathSegmentType
	key   string
	value string
	index int
}

func unquoteFieldPath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func parseWriteFieldPath(path string) ([]fieldPathSegment, error) {
	var segs []fieldPathSegment
	var key strings.Builder
	flush := func() {
		if key.Len() > 0 {
			segs = append(segs, fieldPathSegment{typ: segKey, key: key.String()})
			key.Reset()
		}
	}
	for i := 0; i < len(path); {
		c := path[i]
		switch c {
		case '.':
			flush()
			i++
		case '[':
			flush()
			j := i + 1
			var quote byte
			for ; j < len(path); j++ {
				d := path[j]
				if quote != 0 {
					if d == quote {
						quote = 0
					}
				} else if d == '"' || d == '\'' {
					quote = d
				} else if d == ']' {
					break
				}
			}
			if j >= len(path) {
				return nil, fmt.Errorf("unterminated bracket in %q", path)
			}
			body := strings.TrimSpace(path[i+1 : j])
			switch {
			case body == "*":
				segs = append(segs, fieldPathSegment{typ: segAny})
			case body != "" && body[0] != '"' && body[0] != '\'' && strings.Contains(body, "="):
				eq := strings.Index(body, "=")
				segs = append(segs, fieldPathSegment{typ: segMatch, key: strings.TrimSpace(body[:eq]), value: unquoteFieldPath(body[eq+1:])})
			default:
				if n, err := strconv.Atoi(body); err == nil && n >= 0 {
					segs = append(segs, fieldPathSegment{typ: segIndex, index: n})
				} else {
					segs = append(segs, fieldPathSegment{typ: segKey, key: unquoteFieldPath(body)})
				}
			}
			i = j + 1
		default:
			key.WriteByte(c)
			i++
		}
	}
	flush()
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty field path")
	}
	return segs, nil
}

func matchesSelector(item any, seg fieldPathSegment) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	v, ok := m[seg.key]
	return ok && fmt.Sprint(v) == seg.value
}

// expandWildcards replaces each [*] with one path per element of the live
// list: a [name=…] selector for elements that carry a name (the merge key
// managedFields and last-applied are matched by), an index otherwise. A
// wildcard over a missing or empty list is kept as is.
func expandWildcards(obj any, segs []fieldPathSegment) [][]fieldPathSegment {
	var out [][]fieldPathSegment
	var walk func(node any, i int, prefix []fieldPathSegment)
	walk = func(node any, i int, prefix []fieldPathSegment) {
		if i == len(segs) {
			out = append(out, prefix)
			return
		}
		seg := segs[i]
		with := func(sg fieldPathSegment) []fieldPathSegment {
			return append(append(make([]fieldPathSegment, 0, len(prefix)+1), prefix...), sg)
		}
		switch seg.typ {
		case segKey:
			var next any
			if m, ok := node.(map[string]any); ok {
				next = m[seg.key]
			}
			walk(next, i+1, with(seg))
		case segIndex:
			var next any
			if list, ok := node.([]any); ok && seg.index < len(list) {
				next = list[seg.index]
			}
			walk(next, i+1, with(seg))
		case segMatch:
			var next any
			if list, ok := node.([]any); ok {
				for _, item := range list {
					if matchesSelector(item, seg) {
						next = item
						break
					}
				}
			}
			walk(next, i+1, with(seg))
		default:
			list, _ := node.([]any)
			if len(list) == 0 {
				out = append(out, append(with(seg), segs[i+1:]...))
				return
			}
			for idx, item := range list {
				el := fieldPathSegment{typ: segIndex, index: idx}
				if m, ok := item.(map[string]any); ok {
					if name, ok := m["name"].(string); ok && name != "" {
						el = fieldPathSegment{typ: segMatch, key: "name", value: name}
					}
				}
				walk(item, i+1, with(el))
			}
		}
	}
	walk(obj, 0, nil)
	return out
}

// anyAddressable: some expansion names a concrete entry (a wildcard over an
// empty or missing list stays a wildcard).
func anyAddressable(elements [][]fieldPathSegment) bool {
	for _, el := range elements {
		concrete := true
		for _, seg := range el {
			if seg.typ == segAny {
				concrete = false
				break
			}
		}
		if concrete {
			return true
		}
	}
	return false
}

func ownsEveryElement(managedFields []parsedManagedFields, o *fieldOwnerEvidence, elements [][]fieldPathSegment) bool {
	for _, el := range elements {
		owned := false
		for _, parsed := range managedFields {
			mf := parsed.entry
			if mf.Manager != o.Manager || string(mf.Operation) != o.Operation || mf.Subresource != o.Subresource {
				continue
			}
			if ok, _ := fieldsV1Owns(parsed.fields, el); ok {
				owned = true
				break
			}
		}
		if !owned {
			return false
		}
	}
	return true
}

// valuePresent reports whether the path exists in a decoded JSON document.
// A wildcard matches when any element has the rest of the path.
func valuePresent(node any, segs []fieldPathSegment) bool {
	if len(segs) == 0 {
		return true
	}
	seg := segs[0]
	switch seg.typ {
	case segKey:
		m, ok := node.(map[string]any)
		if !ok {
			return false
		}
		child, ok := m[seg.key]
		return ok && valuePresent(child, segs[1:])
	case segIndex:
		list, ok := node.([]any)
		return ok && seg.index < len(list) && valuePresent(list[seg.index], segs[1:])
	default:
		list, ok := node.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if (seg.typ == segAny || matchesSelector(item, seg)) && valuePresent(item, segs[1:]) {
				return true
			}
		}
		return false
	}
}

// fieldsV1Owns reports whether a managedFields fieldsV1 set covers the path.
// Exact when the set names the path itself as a leaf; approximate when it
// only names an ancestor as a leaf (an atomic value such as an atomic list),
// names the path with a partially-owned subtree, or matched via a wildcard.
func fieldsV1Owns(node map[string]any, segs []fieldPathSegment) (owned, approximate bool) {
	return fieldsV1OwnsAt(node, segs, 0)
}

func fieldsV1OwnsAt(node map[string]any, segs []fieldPathSegment, depth int) (bool, bool) {
	if len(segs) == 0 {
		return true, !isFieldsLeaf(node)
	}
	seg := segs[0]
	var children []map[string]any
	switch seg.typ {
	case segKey:
		if child, ok := node["f:"+seg.key].(map[string]any); ok {
			children = append(children, child)
		}
	case segMatch, segAny:
		for k, v := range node {
			child, ok := v.(map[string]any)
			if !ok || !strings.HasPrefix(k, "k:") {
				continue
			}
			var keyFields map[string]any
			if seg.typ == segAny ||
				(json.Unmarshal([]byte(strings.TrimPrefix(k, "k:")), &keyFields) == nil && matchesSelector(keyFields, seg)) {
				children = append(children, child)
			}
		}
	case segIndex:
		// Index-addressed lists are atomic in managedFields; only an
		// ancestor leaf can own them.
	}
	if len(children) == 0 {
		if depth > 0 && isFieldsLeaf(node) {
			return true, true
		}
		return false, false
	}
	owned, approximate := false, seg.typ == segAny
	for _, child := range children {
		if o, a := fieldsV1OwnsAt(child, segs[1:], depth+1); o {
			owned = true
			approximate = approximate || a
		}
	}
	if !owned {
		return false, false
	}
	return true, approximate
}

func isFieldsLeaf(node map[string]any) bool {
	for k := range node {
		if k != "." {
			return false
		}
	}
	return true
}

// pointerTokenSets turns the path into JSON-pointer tokens against the live
// object, resolving selectors to indices, with one token list per element a
// wildcard expands to. A list stops at a selector that matches nothing (or a
// wildcard over no list), so only an ancestor pointer can cover it.
func pointerTokenSets(obj any, segs []fieldPathSegment) [][]string {
	var sets [][]string
	var walk func(node any, rest []fieldPathSegment, tokens []string)
	walk = func(node any, rest []fieldPathSegment, tokens []string) {
		if len(rest) == 0 {
			sets = append(sets, tokens)
			return
		}
		seg := rest[0]
		with := func(t string) []string {
			return append(append(make([]string, 0, len(tokens)+1), tokens...), t)
		}
		switch seg.typ {
		case segKey:
			var next any
			if m, ok := node.(map[string]any); ok {
				next = m[seg.key]
			}
			walk(next, rest[1:], with(seg.key))
		case segIndex:
			var next any
			if list, ok := node.([]any); ok && seg.index < len(list) {
				next = list[seg.index]
			}
			walk(next, rest[1:], with(strconv.Itoa(seg.index)))
		case segMatch:
			list, _ := node.([]any)
			for i, item := range list {
				if matchesSelector(item, seg) {
					walk(item, rest[1:], with(strconv.Itoa(i)))
					return
				}
			}
			sets = append(sets, tokens)
		default:
			list, _ := node.([]any)
			if len(list) == 0 {
				sets = append(sets, tokens)
				return
			}
			for i, item := range list {
				walk(item, rest[1:], with(strconv.Itoa(i)))
			}
		}
	}
	walk(obj, segs, nil)
	return sets
}

func markCovered(pointers []string, sets [][]string, covered []bool) {
	for i, tokens := range sets {
		for _, p := range pointers {
			if pointerCovers(p, tokens) {
				covered[i] = true
			}
		}
	}
}

func allCovered(covered []bool) bool {
	for _, c := range covered {
		if !c {
			return false
		}
	}
	return len(covered) > 0
}

func anyCovered(covered []bool) bool {
	for _, c := range covered {
		if c {
			return true
		}
	}
	return false
}

// argoAppListsResource reports whether the Application's status lists the
// object among the resources it manages. The core group is recorded as an
// omitted group there.
func argoAppListsResource(app *unstructured.Unstructured, ref writeEvidenceRef) bool {
	resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
	for _, raw := range resources {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { v, _ := r[k].(string); return v }
		if str("kind") == ref.Kind && str("group") == ref.Group && str("namespace") == ref.Namespace && str("name") == ref.Name {
			return true
		}
	}
	return false
}

func pointerCovers(pointer string, tokens []string) bool {
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > len(tokens) {
		return false
	}
	for i, p := range parts {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		if p != tokens[i] {
			return false
		}
	}
	return true
}
