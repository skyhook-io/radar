package helm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
)

// maxRollbackLiveChecks bounds the caller-scoped GETs one rollback preview makes.
const maxRollbackLiveChecks = 100

// MCPRollbackRequest previews or executes a rollback to a stored revision.
type MCPRollbackRequest struct {
	Namespace, Name            string
	Revision, ExpectedRevision int
	DryRun                     bool
}

// MCPRollbackResult reuses the upgrade comparison: rollback re-applies the
// target revision's stored manifest and values, so the stored comparison is
// exact. Live is set only for previews.
type MCPRollbackResult struct {
	MCPUpgradeResult
	TargetRevision int
	Live           *RollbackLiveCheck
	// Hooks are the target revision's pre/post-rollback hooks, which Helm runs.
	Hooks []MCPHookPreview
}

// RollbackLiveCheck reports what a stored comparison can't show. Helm's
// rollback updates any live object the target revision declares and stamps
// this release's ownership on it without the ownership check an upgrade makes,
// and decides whether to delete a removed object from the live object's
// resource policy.
type RollbackLiveCheck struct {
	// Takeovers exist live but aren't recorded as this release's.
	Takeovers []RollbackTakeover
	// Kept are removed objects Helm will leave because the live object says keep.
	Kept []ResourceRef
	// Unknown couldn't be read (denied, kind not served, or beyond the check cap).
	Unknown []ResourceRef
	Checked int
}

// RollbackTakeover names who the live object is recorded as belonging to.
// Owner is "<namespace>/<release>" of another release, or empty when the live
// object doesn't record both coordinates of a Helm owner.
type RollbackTakeover struct {
	ResourceRef
	Owner string `json:"owner"`
}

// liveRead is one caller-scoped GET. Found false with no error means absent.
type liveRead struct {
	Annotations map[string]string
	Found       bool
	Err         error
}

type liveAnnotationReader func(renderedResource) liveRead

// RollbackForMCPAsUser previews (DryRun) or executes a rollback as the caller.
// ExpectedRevision refuses a stale latest revision; it is not a lock.
func (c *Client) RollbackForMCPAsUser(req MCPRollbackRequest, user *pkgauth.User) (*MCPRollbackResult, error) {
	cfg, err := c.getActionConfigForUser(req.Namespace, user)
	if err != nil {
		return nil, err
	}
	return c.rollbackForMCPWith(cfg, req, nil)
}

func (c *Client) rollbackForMCPWith(cfg *action.Configuration, req MCPRollbackRequest, read liveAnnotationReader) (*MCPRollbackResult, error) {
	current, err := cfg.Releases.Last(req.Name)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != 0 && current.Version != req.ExpectedRevision {
		return nil, &OperationPreflightError{Err: fmt.Errorf("release is now revision %d, expected %d; inspect it and preview again", current.Version, req.ExpectedRevision)}
	}
	if req.Revision > current.Version {
		return nil, &OperationPreflightError{Err: fmt.Errorf("revision %d is newer than the latest revision %d", req.Revision, current.Version)}
	}
	target, err := cfg.Releases.Get(req.Name, req.Revision)
	if err != nil {
		return nil, &OperationPreflightError{Err: fmt.Errorf("revision %d is not in the stored history: %w", req.Revision, err)}
	}
	if target.Chart == nil || target.Chart.Metadata == nil {
		return nil, &OperationPreflightError{Err: fmt.Errorf("revision %d has no usable stored chart", req.Revision)}
	}
	if !req.DryRun {
		if err := c.rollbackWith(cfg, req.Name, req.Revision); err != nil {
			return nil, err
		}
		latest, err := cfg.Releases.Last(req.Name)
		if err != nil {
			return nil, fmt.Errorf("rollback ran but the new revision couldn't be read: %w", err)
		}
		return &MCPRollbackResult{MCPUpgradeResult: MCPUpgradeResult{ChartVersion: target.Chart.Metadata.Version, Revision: latest.Version, BaselineRevision: current.Version}, TargetRevision: req.Revision}, nil
	}
	// Helm records a rollback as a new revision carrying the target's chart,
	// manifest and values.
	next := *target
	next.Version = current.Version + 1
	compared, err := mcpUpgradeResult(current, &next, true)
	if err != nil {
		return nil, err
	}
	if read == nil {
		if err := useReleaseTargetNamespace(cfg, req.Name); err != nil {
			return nil, err
		}
		read = kubeLiveAnnotationReader(cfg.KubeClient)
	}
	live := checkRollbackLive(current, target, read)
	return &MCPRollbackResult{MCPUpgradeResult: *compared, TargetRevision: req.Revision, Live: &live,
		Hooks: hookPreviews(target, release.HookPreRollback, release.HookPostRollback)}, nil
}

// checkRollbackLive reads, as the caller, every object the rollback will apply
// or remove (up to maxRollbackLiveChecks), mirroring Helm's Update: target
// objects that exist are overwritten and claimed; removed objects are deleted
// unless the live object carries helm.sh/resource-policy: keep.
func checkRollbackLive(current, target *release.Release, read liveAnnotationReader) RollbackLiveCheck {
	out := RollbackLiveCheck{Takeovers: []RollbackTakeover{}, Kept: []ResourceRef{}, Unknown: []ResourceRef{}}
	identity := func(r renderedResource) string {
		return resourceid.ResourceKey(r.Object.GroupVersionKind().Group, r.Ref.Kind, r.Ref.Namespace, r.Ref.Name)
	}
	targetObjects, _ := parseManifestResourceObjects(target.Manifest, target.Namespace)
	currentObjects, _ := parseManifestResourceObjects(current.Manifest, current.Namespace)
	inTarget := make(map[string]bool, len(targetObjects))
	type check struct {
		r       renderedResource
		removed bool
	}
	checks := make([]check, 0, len(targetObjects)+len(currentObjects))
	for _, r := range targetObjects {
		inTarget[identity(r)] = true
		checks = append(checks, check{r: r})
	}
	for _, r := range currentObjects {
		if !inTarget[identity(r)] {
			checks = append(checks, check{r: r, removed: true})
		}
	}
	for i, c := range checks {
		if i >= maxRollbackLiveChecks {
			out.Unknown = append(out.Unknown, c.r.Ref)
			continue
		}
		got := read(c.r)
		if got.Err != nil {
			out.Unknown = append(out.Unknown, c.r.Ref)
			continue
		}
		out.Checked++
		if !got.Found {
			continue
		}
		if c.removed {
			// Exact match: rollback's Update doesn't normalize the value the way
			// uninstall's manifest filter does (kube/client.go Update).
			if got.Annotations[kube.ResourcePolicyAnno] == kube.KeepPolicy {
				out.Kept = append(out.Kept, c.r.Ref)
			}
			continue
		}
		name, namespace := got.Annotations["meta.helm.sh/release-name"], got.Annotations["meta.helm.sh/release-namespace"]
		if name == target.Name && namespace == target.Namespace {
			continue
		}
		owner := "" // a partial record doesn't name a release
		if name != "" && namespace != "" {
			owner = namespace + "/" + name
		}
		out.Takeovers = append(out.Takeovers, RollbackTakeover{ResourceRef: c.r.Ref, Owner: owner})
	}
	return out
}

// kubeLiveAnnotationReader GETs one stored object through Helm's kube client,
// which carries the caller's impersonated identity and the release namespace.
func kubeLiveAnnotationReader(kc kube.Interface) liveAnnotationReader {
	return func(r renderedResource) liveRead {
		metadata := map[string]any{"name": r.Object.GetName()}
		if r.Object.GetNamespace() != "" {
			metadata["namespace"] = r.Object.GetNamespace()
		}
		raw, err := json.Marshal(map[string]any{"apiVersion": r.Object.GetAPIVersion(), "kind": r.Object.GetKind(), "metadata": metadata})
		if err != nil {
			return liveRead{Err: err}
		}
		infos, err := kc.Build(bytes.NewReader(raw), false)
		if err != nil {
			return liveRead{Err: err}
		}
		if len(infos) != 1 {
			return liveRead{Err: errors.New("object did not resolve to one resource")}
		}
		if err := infos[0].Get(); err != nil {
			if apierrors.IsNotFound(err) {
				return liveRead{}
			}
			return liveRead{Err: err}
		}
		accessor, err := meta.Accessor(infos[0].Object)
		if err != nil {
			return liveRead{Err: err}
		}
		return liveRead{Annotations: accessor.GetAnnotations(), Found: true}
	}
}

// MCPUninstallPreview mirrors Helm's uninstall: what it deletes, what
// helm.sh/resource-policy leaves, and which delete hooks run.
type MCPUninstallPreview struct {
	Revision int
	// HistoryOnly: the release is already uninstalled with history kept, so
	// uninstall only purges the stored history. No hooks run and nothing is deleted.
	HistoryOnly bool
	Delete      []ResourceRef
	Kept        []ResourceRef
	Hooks       []MCPHookPreview
	ParseErrors int
}

// MCPHookPreview is a hook an action runs, with its effective delete policies
// (Helm applies before-hook-creation when none are declared).
type MCPHookPreview struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Events         []string `json:"events"`
	DeletePolicies []string `json:"deletePolicies"`
	LastPhase      string   `json:"lastPhase,omitempty"`
}

// UninstallPreviewAsUser reads the latest stored release as the caller.
func (c *Client) UninstallPreviewAsUser(namespace, name string, user *pkgauth.User) (*MCPUninstallPreview, error) {
	cfg, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	rel, err := cfg.Releases.Last(name)
	if err != nil {
		return nil, err
	}
	return uninstallPreviewFor(rel)
}

func uninstallPreviewFor(rel *release.Release) (*MCPUninstallPreview, error) {
	out := &MCPUninstallPreview{Revision: rel.Version, Delete: []ResourceRef{}, Kept: []ResourceRef{}, Hooks: []MCPHookPreview{}}
	if rel.Info != nil && rel.Info.Status == release.StatusUninstalled {
		out.HistoryOnly = true
		return out, nil
	}
	_, files, err := releaseutil.SortManifests(releaseutil.SplitManifests(rel.Manifest), nil, releaseutil.UninstallOrder)
	if err != nil {
		return nil, fmt.Errorf("the stored release manifest can't be split, so Helm would refuse to uninstall it: %w", err)
	}
	// Helm evaluates resource-policy per manifest document before flattening
	// Lists, and leaves a document whose policy is present but not "keep" out of
	// both its keep and delete sets (action/resource_policy.go).
	for _, file := range files {
		refs := []ResourceRef{}
		objects, parseErrors := parseManifestResourceObjects(file.Content, rel.Namespace)
		out.ParseErrors += parseErrors
		for _, o := range objects {
			refs = append(refs, o.Ref)
		}
		hasPolicy := false
		if file.Head != nil && file.Head.Metadata != nil {
			_, hasPolicy = file.Head.Metadata.Annotations[kube.ResourcePolicyAnno]
		}
		if hasPolicy {
			out.Kept = append(out.Kept, refs...)
			continue
		}
		out.Delete = append(out.Delete, refs...)
	}
	out.Hooks = hookPreviews(rel, release.HookPreDelete, release.HookPostDelete)
	return out, nil
}

// hookPreviews lists rel's hooks bound to any of events, keeping only those events.
func hookPreviews(rel *release.Release, events ...release.HookEvent) []MCPHookPreview {
	out := []MCPHookPreview{}
	for _, h := range rel.Hooks {
		if h == nil {
			continue
		}
		hook := MCPHookPreview{Name: h.Name, Kind: h.Kind, Events: []string{}, DeletePolicies: []string{}, LastPhase: h.LastRun.Phase.String()}
		for _, event := range h.Events {
			for _, wanted := range events {
				if event == wanted {
					hook.Events = append(hook.Events, event.String())
				}
			}
		}
		if len(hook.Events) == 0 {
			continue
		}
		for _, policy := range h.DeletePolicies {
			hook.DeletePolicies = append(hook.DeletePolicies, string(policy))
		}
		if len(hook.DeletePolicies) == 0 {
			hook.DeletePolicies = append(hook.DeletePolicies, string(release.HookBeforeHookCreation))
		}
		out = append(out, hook)
	}
	return out
}
