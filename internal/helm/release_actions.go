package helm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
)

type UninstallOptions struct {
	NoHooks     bool
	KeepHistory bool
	DryRun      bool
}

type ReleaseActionOptions struct {
	Action      string `json:"action"`
	Revision    int    `json:"revision,omitempty"`
	NoHooks     bool   `json:"no_hooks"`
	KeepHistory bool   `json:"keep_history"`
}

func (o ReleaseActionOptions) Validate() error {
	switch o.Action {
	case "uninstall":
		if o.Revision != 0 {
			return fmt.Errorf("revision is only supported for rollback")
		}
	case "rollback":
		if o.Revision <= 0 {
			return fmt.Errorf("rollback requires an explicit positive revision; use get_helm_release include=history")
		}
		if o.KeepHistory {
			return fmt.Errorf("keep_history is only supported for uninstall")
		}
	default:
		return fmt.Errorf("action must be uninstall or rollback")
	}
	return nil
}

type ReleaseActionResource struct {
	ResourceRef
	Effect string `json:"effect"`
	Scope  string `json:"scope,omitempty"`
}

type ReleaseActionHook struct {
	HelmHook
	Effect      string                  `json:"effect"`
	Enumeration string                  `json:"enumeration"`
	Resources   []ReleaseActionResource `json:"resources,omitempty"`
}

type ReleaseActionPreview struct {
	Summary                  string                  `json:"summary"`
	Name                     string                  `json:"name"`
	Namespace                string                  `json:"namespace"`
	Status                   string                  `json:"status"`
	Revision                 int                     `json:"revision"`
	Options                  ReleaseActionOptions    `json:"options"`
	Resources                []ReleaseActionResource `json:"resources"`
	Hooks                    []ReleaseActionHook     `json:"hooks"`
	Warnings                 []string                `json:"warnings"`
	Limits                   []string                `json:"limits"`
	StatusMeaning            string                  `json:"statusMeaning,omitempty"`
	Deleted                  *time.Time              `json:"deleted,omitempty"`
	LastDeployed             *time.Time              `json:"lastDeployed,omitempty"`
	ManagedByFluxHelmRelease string                  `json:"managedByFluxHelmRelease,omitempty"`
	HookDiagnostics          []HookDiagnostic        `json:"hookDiagnostics,omitempty"`
	Fingerprint              string                  `json:"-"`
}

// PreviewReleaseAction plans stored declarations. Live rollback effects and
// hook evidence are attached by EnrichReleaseActionPreview as the caller.
func PreviewReleaseAction(cfg *action.Configuration, name string, options ReleaseActionOptions) (*ReleaseActionPreview, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	current, err := cfg.Releases.Last(name)
	if err != nil {
		return nil, fmt.Errorf("read current Helm release: %w", err)
	}
	if current.Info == nil {
		return nil, fmt.Errorf("%w: current Helm release has no status", ErrReleaseActionInvalidManifest)
	}
	target := current
	if options.Action == "rollback" {
		if options.Revision > current.Version {
			return nil, fmt.Errorf("rollback revision must not exceed current revision %d", current.Version)
		}
		target, err = cfg.Releases.Get(name, options.Revision)
		if err != nil {
			return nil, fmt.Errorf("read rollback revision %d: %w", options.Revision, err)
		}
	}
	if target.Info == nil {
		return nil, fmt.Errorf("%w: target Helm release has no status", ErrReleaseActionInvalidManifest)
	}
	raw, err := json.Marshal(struct {
		Current *release.Release
		Target  *release.Release
		Options ReleaseActionOptions
	}{current, target, options})
	if err != nil {
		return nil, err
	}
	preview := &ReleaseActionPreview{
		Name: name, Namespace: current.Namespace, Status: current.Info.Status.String(), Revision: current.Version,
		Options: options, Fingerprint: fmt.Sprintf("%x", sha256.Sum256(raw)),
		Resources: []ReleaseActionResource{}, Hooks: []ReleaseActionHook{},
		Warnings: []string{},
		Limits: []string{
			"This is a stored-manifest plan, not a Kubernetes server-side dry run. Write permissions, hooks, live finalizers, and controller cleanup are not simulated. Resources created by controllers and chart CRDs outside the release manifest are not enumerated.",
			"Helm does not provide atomic release preconditions. Release state is rechecked before execution; concurrent Helm operations can still race. Avoid concurrent management of this release.",
		},
	}
	if !current.Info.Deleted.IsZero() {
		t := current.Info.Deleted.Time
		preview.Deleted = &t
	}
	if !current.Info.LastDeployed.IsZero() {
		t := current.Info.LastDeployed.Time
		preview.LastDeployed = &t
	}
	if options.Action == "rollback" {
		if current.Info.Status.IsPending() {
			preview.Warnings = append(preview.Warnings, "Rolling back a pending release can race an active install, upgrade, or rollback. Helm v3.22 does not lock pending releases for rollback; verify the other operation has stopped.")
		}
		preview.Limits = append(preview.Limits, "Resource effects are checked against live objects before confirmation; live state can change before execution.")
	} else {
		if !options.KeepHistory {
			preview.Warnings = append(preview.Warnings, "Uninstall purges Helm release history by default, including earlier revisions.")
		}
		if current.Info.Status == release.StatusUninstalling {
			preview.Warnings = append(preview.Warnings, "This retries the uninstalling release. Inspect pre-delete hook Jobs and surviving resources/finalizers first; no_hooks skips cleanup hooks and can leave external resources behind.")
		}
		if current.Info.Status == release.StatusUninstalled {
			if options.KeepHistory {
				return nil, fmt.Errorf("%w: release is already uninstalled; keep_history=true has no action to perform", ErrReleaseActionRefused)
			}
		}
	}
	enumerationAction := options.Action
	if options.Action == "uninstall" && current.Info.Status == release.StatusUninstalled {
		enumerationAction = "purge"
	}
	rendered, err := enumerableReleaseResources(cfg, target.Manifest, current.Namespace, enumerationAction)
	if err != nil {
		return nil, err
	}
	currentRendered := rendered
	if options.Action == "rollback" {
		currentRendered, err = enumerableReleaseResources(cfg, current.Manifest, current.Namespace, "rollback")
		if err != nil {
			return nil, err
		}
	}
	currentMap := make(map[resourceid.Ref]bool, len(currentRendered))
	targetMap := make(map[resourceid.Ref]bool, len(rendered))
	for _, r := range currentRendered {
		currentMap[releaseActionResourceIdentity(r.Ref)] = true
	}
	for _, r := range rendered {
		targetMap[releaseActionResourceIdentity(r.Ref)] = true
	}
	for _, resource := range rendered {
		effect := "delete"
		if options.Action == "rollback" {
			effect = "create"
			if currentMap[releaseActionResourceIdentity(resource.Ref)] {
				effect = "update"
			}
		} else if current.Info.Status == release.StatusUninstalled {
			effect = "not processed (history-only purge)"
		} else if policy, present := resource.Object.GetAnnotations()[kube.ResourcePolicyAnno]; present {
			effect = "not deleted (helm.sh/resource-policy)"
			if strings.ToLower(strings.TrimSpace(policy)) == kube.KeepPolicy {
				effect = "keep (helm.sh/resource-policy)"
			}
		}
		preview.Resources = append(preview.Resources, ReleaseActionResource{ResourceRef: resource.Ref, Effect: effect, Scope: resource.Scope})
	}
	if options.Action == "rollback" {
		for _, r := range currentRendered {
			if !targetMap[releaseActionResourceIdentity(r.Ref)] {
				preview.Resources = append(preview.Resources, ReleaseActionResource{ResourceRef: r.Ref, Effect: "delete"})
			}
		}
	}
	slices.SortFunc(preview.Resources, func(a, b ReleaseActionResource) int {
		return strings.Compare(resourceRefKey(a.ResourceRef), resourceRefKey(b.ResourceRef))
	})
	hookTarget := *target
	hookTarget.Namespace = current.Namespace
	for i, hook := range extractHooks(&hookTarget) {
		relevant := slices.Contains(hook.Events, "pre-delete") || slices.Contains(hook.Events, "post-delete")
		if options.Action == "rollback" {
			relevant = slices.Contains(hook.Events, "pre-rollback") || slices.Contains(hook.Events, "post-rollback")
		}
		if !relevant {
			continue
		}
		effect := "run"
		if options.NoHooks {
			effect = "skip (no_hooks)"
		} else if options.Action == "uninstall" && current.Info.Status == release.StatusUninstalled {
			effect = "skip (already uninstalled)"
		}
		plannedHook := ReleaseActionHook{HelmHook: hook, Effect: effect, Enumeration: "one named object"}
		manifest := target.Hooks[i].Manifest
		resources, err := enumerableReleaseResources(cfg, manifest, current.Namespace, "hook")
		if err != nil {
			plannedHook.Enumeration = "not enumerated: " + strings.TrimPrefix(strings.TrimSuffix(err.Error(), "; no confirmation issued"), ErrReleaseActionInvalidManifest.Error()+": ")
		} else {
			direct, _ := parseManifestResourceObjects(manifest, "")
			if len(resources) == 0 {
				plannedHook.Enumeration = "not enumerated: manifest contains no named objects"
			} else if len(resources) != 1 || len(direct) != 1 {
				plannedHook.Enumeration = "multiple objects or List manifest"
			}
			for _, r := range resources {
				plannedHook.Resources = append(plannedHook.Resources, ReleaseActionResource{ResourceRef: r.Ref, Effect: effect, Scope: r.Scope})
			}
		}
		preview.Hooks = append(preview.Hooks, plannedHook)
	}
	if len(preview.Hooks) > 0 {
		preview.StatusMeaning = "Hook status is the last recorded phase; Running can persist after hook creation or waiting failed"
	}
	for _, hook := range preview.Hooks {
		if hook.Effect == "run" && len(hook.DeletePolicies) > 0 && !slices.Contains(hook.DeletePolicies, "before-hook-creation") {
			preview.Warnings = append(preview.Warnings, fmt.Sprintf("Hook %s/%s has no before-hook-creation delete policy. A surviving Job or Pod can make execution fail with AlreadyExists. Inspect get_helm_release hook diagnostics; explicitly approve deleting that hook object after inspecting it, or use no_hooks after reviewing skipped cleanup.", hook.Namespace, hook.Name))
		}
	}
	preview.Summary = releaseActionSummary(preview)
	return preview, nil
}

func releaseActionSummary(preview *ReleaseActionPreview) string {
	counts := make(map[string]int)
	for _, resource := range preview.Resources {
		counts[strings.SplitN(resource.Effect, " (", 2)[0]]++
	}
	parts := []string{}
	for _, effect := range []struct{ key, verb string }{
		{"create", "creates"}, {"adopt", "adopts"}, {"update", "updates"}, {"delete", "deletes"}, {"keep", "keeps"},
		{"not deleted", "does not delete"}, {"not processed", "does not process"},
	} {
		if count := counts[effect.key]; count > 0 {
			noun := "resources"
			if count == 1 {
				noun = "resource"
			}
			parts = append(parts, fmt.Sprintf("%s %d %s", effect.verb, count, noun))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "affects 0 manifest resources")
	}
	events := []string{"pre-delete", "post-delete"}
	if preview.Options.Action == "rollback" {
		events = []string{"pre-rollback", "post-rollback"}
	}
	for _, event := range events {
		run, skip := 0, 0
		for _, hook := range preview.Hooks {
			if !slices.Contains(hook.Events, event) {
				continue
			}
			if hook.Effect == "run" {
				run++
			} else {
				skip++
			}
		}
		for _, effect := range []struct {
			verb  string
			count int
		}{{"runs", run}, {"skips", skip}} {
			if effect.count > 0 {
				noun := "hooks"
				if effect.count == 1 {
					noun = "hook"
				}
				parts = append(parts, fmt.Sprintf("%s %d %s %s", effect.verb, effect.count, event, noun))
			}
		}
	}
	if preview.Options.Action == "rollback" {
		parts = append(parts, fmt.Sprintf("creates a new revision from revision %d", preview.Options.Revision))
	} else if preview.Options.KeepHistory {
		parts = append(parts, "retains history")
	} else {
		parts = append(parts, "purges history")
	}
	summary := preview.Options.Action + " " + strings.Join(parts, ", ")
	if preview.ManagedByFluxHelmRelease != "" {
		summary += "; owned by Flux HelmRelease " + preview.ManagedByFluxHelmRelease
	}
	return summary
}

func RunReleaseAction(cfg *action.Configuration, name string, options ReleaseActionOptions, fingerprint string) error {
	preview, err := PreviewReleaseAction(cfg, name, options)
	if err != nil {
		return err
	}
	if fingerprint == "" || preview.Fingerprint != fingerprint {
		return fmt.Errorf("the release changed since the preview — preview again")
	}
	if options.Action == "uninstall" {
		return uninstallWithOptions(cfg, name, UninstallOptions{NoHooks: options.NoHooks, KeepHistory: options.KeepHistory})
	}
	return rollbackWith(cfg, name, options.Revision, options.NoHooks)
}

func uninstallWithOptions(cfg *action.Configuration, name string, options UninstallOptions) error {
	if err := useReleaseTargetNamespace(cfg, name); err != nil {
		return err
	}
	uninstall := action.NewUninstall(cfg)
	uninstall.Timeout = 120 * time.Second
	uninstall.DisableHooks = options.NoHooks
	uninstall.KeepHistory = options.KeepHistory
	uninstall.DryRun = options.DryRun
	if _, err := uninstall.Run(name); err != nil {
		if err.Error() == fmt.Sprintf("the release named %q is already deleted", name) {
			return fmt.Errorf("%w: %v", ErrReleaseActionRefused, err)
		}
		return fmt.Errorf("uninstall failed: %w", err)
	}
	return nil
}

var ErrReleaseActionRefused = errors.New("Helm action refused")
var ErrReleaseActionInvalidManifest = errors.New("Helm action refused: stored release cannot be fully enumerated")
var ErrReleaseActionInProgress = errors.New("a Helm action is already running for this release; wait for it to finish")
var releaseActionsInFlight sync.Map

func BeginReleaseAction(storageNamespace, name string) (func(), error) {
	key := struct{ Context, Namespace, Name string }{k8s.GetContextName(), storageNamespace, name}
	if _, loaded := releaseActionsInFlight.LoadOrStore(key, struct{}{}); loaded {
		return nil, ErrReleaseActionInProgress
	}
	return func() { releaseActionsInFlight.Delete(key) }, nil
}

type releaseActionManifestResource struct {
	renderedResource
	Scope string
}

func enumerableReleaseResources(cfg *action.Configuration, manifest, namespace, action string) ([]releaseActionManifestResource, error) {
	resources := []releaseActionManifestResource{}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		raw, err := yaml.YAMLToJSON([]byte(doc))
		if err != nil {
			return nil, fmt.Errorf("%w: release manifest cannot be fully enumerated; no confirmation issued", ErrReleaseActionInvalidManifest)
		}
		if string(raw) == "null" {
			continue
		}
		var obj unstructured.Unstructured
		if err := json.Unmarshal(raw, &obj.Object); err != nil {
			return nil, fmt.Errorf("%w: release manifest cannot be fully enumerated; no confirmation issued", ErrReleaseActionInvalidManifest)
		}
		if action == "hook" && obj.IsList() {
			items, err := obj.ToList()
			if err != nil {
				return nil, fmt.Errorf("cannot enumerate hook List: %w", err)
			}
			for _, item := range items.Items {
				itemRaw, err := json.Marshal(item.Object)
				if err != nil {
					return nil, err
				}
				nested, err := enumerableReleaseResources(cfg, string(itemRaw), namespace, action)
				if err != nil {
					return nil, err
				}
				resources = append(resources, nested...)
			}
			continue
		}
		parsed, errors := parseManifestResourceObjects(doc, "")
		if errors > 0 || len(parsed) != 1 {
			return nil, fmt.Errorf("%w: release manifest cannot be fully enumerated; no confirmation issued", ErrReleaseActionInvalidManifest)
		}
		resources = append(resources, releaseActionManifestResource{renderedResource: parsed[0]})
	}
	if action == "purge" || len(resources) == 0 {
		return resources, nil
	}
	mapper, mapperErr := cfg.RESTClientGetter.ToRESTMapper()
	for i := range resources {
		r := &resources[i]
		_, policyPresent := r.Object.GetAnnotations()[kube.ResourcePolicyAnno]
		bestEffort := action == "hook" || (action == "uninstall" && policyPresent)
		resolveErr := mapperErr
		if mapperErr == nil {
			gvk := r.Object.GroupVersionKind()
			if gvk.Version == "" {
				resolveErr = fmt.Errorf("resource %s/%s has no apiVersion", r.Ref.Kind, r.Ref.Name)
			} else {
				mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
				resolveErr = err
				if err == nil {
					switch mapping.Scope.Name() {
					case meta.RESTScopeNameRoot:
						r.Object.SetNamespace("")
					case meta.RESTScopeNameNamespace:
						if r.Object.GetNamespace() == "" {
							r.Object.SetNamespace(namespace)
						}
					default:
						resolveErr = fmt.Errorf("unknown resource scope")
					}
				}
			}
		}
		if resolveErr != nil {
			if bestEffort {
				r.Scope = "scope unresolved: " + resolveErr.Error()
				continue
			}
			if mapperErr != nil {
				return nil, fmt.Errorf("%w: cannot resolve resource scopes: %v; no confirmation issued", ErrReleaseActionInvalidManifest, mapperErr)
			}
			return nil, fmt.Errorf("%w: cannot resolve scope of %s %s/%s: %v; no confirmation issued", ErrReleaseActionInvalidManifest, r.Ref.APIVersion, r.Ref.Kind, r.Ref.Name, resolveErr)
		}
		r.Ref.Namespace = r.Object.GetNamespace()
	}
	slices.SortFunc(resources, func(a, b releaseActionManifestResource) int {
		return strings.Compare(resourceRefKey(a.Ref), resourceRefKey(b.Ref))
	})
	return resources, nil
}

func releaseActionResourceIdentity(ref ResourceRef) resourceid.Ref {
	return resourceid.Ref{Group: resourceid.GroupFromAPIVersion(ref.APIVersion), Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

func EnrichReleaseActionPreview(ctx context.Context, cfg *action.Configuration, storageNamespace string, preview *ReleaseActionPreview) error {
	if err := EnrichReleaseActionLivePlan(ctx, cfg, storageNamespace, preview); err != nil {
		return err
	}
	enrichReleaseActionHookEvidence(ctx, preview)
	return nil
}

func EnrichReleaseActionLivePlan(ctx context.Context, cfg *action.Configuration, storageNamespace string, preview *ReleaseActionPreview) error {
	return enrichReleaseActionPreview(ctx, cfg, storageNamespace, preview, fluxHelmReleaseMap(ctx))
}

func enrichReleaseActionHookEvidence(ctx context.Context, preview *ReleaseActionPreview) {
	hooks := make([]HelmHook, 0, len(preview.Hooks))
	for _, h := range preview.Hooks {
		hooks = append(hooks, h.HelmHook)
	}
	detail := &HelmReleaseDetail{Namespace: preview.Namespace, Hooks: hooks, HookDiagnostics: extractHookDiagnostics(hooks)}
	EnrichHookDiagnosticsWithClusterEvidence(ctx, detail, k8s.ClientFromContext(ctx))
	preview.HookDiagnostics = detail.HookDiagnostics
}

func enrichReleaseActionPreview(ctx context.Context, cfg *action.Configuration, storageNamespace string, preview *ReleaseActionPreview, fluxMap map[string]string) error {
	preview.ManagedByFluxHelmRelease = applyFluxOwnership(preview.Name, storageNamespace, fluxMap)
	if preview.ManagedByFluxHelmRelease != "" {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("Flux HelmRelease %s owns this release and may reconcile it back. Change its source of truth before managing it directly.", preview.ManagedByFluxHelmRelease))
	}
	if preview.Options.Action != "rollback" {
		preview.Summary = releaseActionSummary(preview)
		return nil
	}
	if err := useReleaseTargetNamespace(cfg, preview.Name); err != nil {
		return err
	}
	for i := range preview.Resources {
		r := &preview.Resources[i]
		raw, _ := json.Marshal(map[string]any{"apiVersion": r.APIVersion, "kind": r.Kind, "metadata": map[string]any{"name": r.Name, "namespace": r.Namespace}})
		infos, err := cfg.KubeClient.Build(strings.NewReader(string(raw)), false)
		if err != nil {
			return fmt.Errorf("read rollback resource %s/%s: %w", r.Namespace, r.Name, err)
		}
		if len(infos) != 1 {
			return fmt.Errorf("%w: rollback resource %s/%s did not resolve to one object", ErrReleaseActionInvalidManifest, r.Namespace, r.Name)
		}
		err = infos[0].Get()
		if apierrors.IsNotFound(err) {
			if r.Effect == "delete" {
				r.Effect = "not deleted (already absent)"
			} else {
				r.Effect = "create"
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("read rollback resource %s/%s: %w", r.Namespace, r.Name, err)
		}
		accessor, err := meta.Accessor(infos[0].Object)
		if err != nil {
			return err
		}
		if r.Effect != "delete" {
			if r.Effect == "create" {
				r.Effect = "adopt (exists live, not in current revision)"
				annotations := accessor.GetAnnotations()
				ownerName, ownerNamespace := annotations["meta.helm.sh/release-name"], annotations["meta.helm.sh/release-namespace"]
				if (ownerName != "" && ownerName != preview.Name) || (ownerNamespace != "" && ownerNamespace != preview.Namespace) {
					preview.Warnings = append(preview.Warnings, fmt.Sprintf("Rollback adopts %s %s/%s from Helm release %s/%s (live ownership annotations).", r.Kind, r.Namespace, r.Name, ownerNamespace, ownerName))
				}
			} else {
				r.Effect = "update"
			}
			continue
		}
		if accessor.GetAnnotations()[kube.ResourcePolicyAnno] == kube.KeepPolicy {
			r.Effect = "keep (live helm.sh/resource-policy)"
		}
	}
	preview.Summary = releaseActionSummary(preview)
	return nil
}

func writeReleaseActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrReleaseActionInvalidManifest):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, driver.ErrReleaseNotFound), apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrReleaseActionRefused), errors.Is(err, ErrReleaseActionInProgress):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeReleaseReadError(w, err)
	}
}

func writeReleaseExecutionError(w http.ResponseWriter, action, namespace, name string, err error) {
	switch {
	case errors.Is(err, ErrReleaseActionInvalidManifest):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, driver.ErrReleaseNotFound), apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrReleaseActionRefused), errors.Is(err, ErrReleaseActionInProgress):
		writeError(w, http.StatusConflict, err.Error())
	case isReleaseReadForbidden(err):
		writeError(w, http.StatusForbidden, "insufficient permissions to "+action+" this Helm release: "+err.Error())
	default:
		log.Printf("[helm] Failed to %s %s/%s: %v", action, namespace, name, err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
