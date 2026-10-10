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
}

type ReleaseActionHook struct {
	HelmHook
	Effect        string `json:"effect"`
	StatusMeaning string `json:"statusMeaning"`
}

type ReleaseActionPreview struct {
	Name                     string                  `json:"name"`
	Namespace                string                  `json:"namespace"`
	Status                   string                  `json:"status"`
	Revision                 int                     `json:"revision"`
	Options                  ReleaseActionOptions    `json:"options"`
	Resources                []ReleaseActionResource `json:"resources"`
	Hooks                    []ReleaseActionHook     `json:"hooks"`
	Warnings                 []string                `json:"warnings"`
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
		return nil, fmt.Errorf("%w: current Helm release has no status", ErrReleaseActionRefused)
	}
	target := current
	if options.Action == "rollback" {
		if options.Revision >= current.Version {
			return nil, fmt.Errorf("rollback revision must be older than current revision %d", current.Version)
		}
		target, err = cfg.Releases.Get(name, options.Revision)
		if err != nil {
			return nil, fmt.Errorf("read rollback revision %d: %w", options.Revision, err)
		}
	}
	if target.Info == nil {
		return nil, fmt.Errorf("%w: target Helm release has no status", ErrReleaseActionRefused)
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
		Warnings: []string{
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
		preview.Warnings = append(preview.Warnings, "Rollback creates a new revision. Resource effects are checked against live objects before confirmation; live state can change before execution.")
	} else {
		if options.KeepHistory {
			preview.Warnings = append(preview.Warnings, "Release history will be retained; this does not retain the release's Kubernetes resources.")
		} else {
			preview.Warnings = append(preview.Warnings, "Uninstall purges Helm release history by default, including earlier revisions.")
		}
		if current.Info.Status == release.StatusUninstalling {
			preview.Warnings = append(preview.Warnings, "This retries the uninstalling release. Inspect pre-delete hook Jobs and surviving resources/finalizers first; no_hooks skips cleanup hooks and can leave external resources behind.")
		}
		if current.Info.Status == release.StatusUninstalled {
			if options.KeepHistory {
				return nil, fmt.Errorf("%w: release is already uninstalled; keep_history=true has no action to perform", ErrReleaseActionRefused)
			}
			preview.Warnings = append(preview.Warnings, "Release is already uninstalled: only its stored Helm history will be purged; resources and hooks will not be processed again.")
		}
	}
	rendered, err := enumerableReleaseResources(target.Manifest, current.Namespace)
	if err != nil {
		return nil, err
	}
	currentRendered := rendered
	if options.Action == "rollback" {
		currentRendered, err = enumerableReleaseResources(current.Manifest, current.Namespace)
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
			effect = "not processed (history purge only)"
		} else if policy, present := resource.Object.GetAnnotations()[kube.ResourcePolicyAnno]; present {
			effect = "not deleted (helm.sh/resource-policy)"
			if strings.ToLower(strings.TrimSpace(policy)) == kube.KeepPolicy {
				effect = "keep (helm.sh/resource-policy)"
			}
		}
		preview.Resources = append(preview.Resources, ReleaseActionResource{ResourceRef: resource.Ref, Effect: effect})
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
	for _, hook := range extractHooks(&hookTarget) {
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
		preview.Hooks = append(preview.Hooks, ReleaseActionHook{HelmHook: hook, Effect: effect, StatusMeaning: "last recorded phase; Running can persist after hook creation or waiting failed"})
	}
	if current.Info.Status == release.StatusUninstalling && !options.NoHooks {
		for _, hook := range preview.Hooks {
			if len(hook.DeletePolicies) > 0 && !slices.Contains(hook.DeletePolicies, "before-hook-creation") {
				preview.Warnings = append(preview.Warnings, fmt.Sprintf("Hook %s/%s has no before-hook-creation delete policy. A surviving failed Job or Pod can make retry fail with AlreadyExists. Inspect get_helm_release hook diagnostics; explicitly approve deleting that hook object after inspecting it, or use no_hooks after reviewing skipped cleanup.", hook.Namespace, hook.Name))
			}
		}
	}
	return preview, nil
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
		return fmt.Errorf("uninstall failed: %w", err)
	}
	return nil
}

var ErrReleaseActionRefused = errors.New("Helm action refused")
var ErrReleaseActionInProgress = errors.New("a Helm action is already running for this release; wait for it to finish")
var releaseActionsInFlight sync.Map

func BeginReleaseAction(storageNamespace, name string) (func(), error) {
	key := struct{ Context, Namespace, Name string }{k8s.GetContextName(), storageNamespace, name}
	if _, loaded := releaseActionsInFlight.LoadOrStore(key, struct{}{}); loaded {
		return nil, ErrReleaseActionInProgress
	}
	return func() { releaseActionsInFlight.Delete(key) }, nil
}

func useReleaseTargetNamespace(cfg *action.Configuration, name string) error {
	rel, err := cfg.Releases.Last(name)
	if err != nil {
		return err
	}
	// Storage retains its namespace; only the Kubernetes object builder changes.
	if client, ok := cfg.KubeClient.(*kube.Client); ok {
		client.Namespace = rel.Namespace
	}
	return nil
}

func enumerableReleaseResources(manifest, namespace string) ([]renderedResource, error) {
	resources, parseErrors := parseManifestResourceObjects(manifest, namespace)
	documents := 0
	for _, doc := range releaseutil.SplitManifests(manifest) {
		raw, err := yaml.YAMLToJSON([]byte(doc))
		if err != nil || string(raw) != "null" {
			documents++
		}
	}
	if parseErrors > 0 || len(resources) != documents {
		return nil, fmt.Errorf("%w: release manifest cannot be fully enumerated; no confirmation issued", ErrReleaseActionRefused)
	}
	return resources, nil
}

func releaseActionResourceIdentity(ref ResourceRef) resourceid.Ref {
	return resourceid.Ref{Group: resourceid.GroupFromAPIVersion(ref.APIVersion), Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

func EnrichReleaseActionPreview(ctx context.Context, cfg *action.Configuration, storageNamespace string, preview *ReleaseActionPreview) error {
	preview.ManagedByFluxHelmRelease = applyFluxOwnership(preview.Name, storageNamespace, fluxHelmReleaseMap(ctx))
	if preview.ManagedByFluxHelmRelease != "" {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("Flux HelmRelease %s owns this release and may reconcile it back. Change its source of truth before managing it directly.", preview.ManagedByFluxHelmRelease))
	} else {
		preview.Warnings = append(preview.Warnings, "A GitOps controller may reconcile this release back. Check its source of truth before managing it directly.")
	}
	hooks := make([]HelmHook, 0, len(preview.Hooks))
	for _, h := range preview.Hooks {
		hooks = append(hooks, h.HelmHook)
	}
	detail := &HelmReleaseDetail{Namespace: preview.Namespace, Hooks: hooks, HookDiagnostics: extractHookDiagnostics(hooks)}
	EnrichHookDiagnosticsWithClusterEvidence(ctx, detail, k8s.ClientFromContext(ctx))
	preview.HookDiagnostics = detail.HookDiagnostics
	if preview.Options.Action != "rollback" {
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
			return fmt.Errorf("%w: rollback resource %s/%s did not resolve to one object", ErrReleaseActionRefused, r.Namespace, r.Name)
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
		if r.Effect != "delete" {
			r.Effect = "update"
			continue
		}
		accessor, err := meta.Accessor(infos[0].Object)
		if err != nil {
			return err
		}
		if accessor.GetAnnotations()[kube.ResourcePolicyAnno] == kube.KeepPolicy {
			r.Effect = "keep (live helm.sh/resource-policy)"
		}
	}
	return nil
}

func writeReleaseActionError(w http.ResponseWriter, err error) {
	switch {
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
