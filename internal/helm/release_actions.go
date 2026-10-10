package helm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"time"

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
	Effect string `json:"effect"`
}

type ReleaseActionPreview struct {
	Name             string                  `json:"name"`
	Namespace        string                  `json:"namespace"`
	Status           string                  `json:"status"`
	Revision         int                     `json:"revision"`
	Options          ReleaseActionOptions    `json:"options"`
	Resources        []ReleaseActionResource `json:"resources"`
	CurrentResources []OwnedResource         `json:"currentResources,omitempty"`
	Hooks            []ReleaseActionHook     `json:"hooks"`
	Warnings         []string                `json:"warnings"`
	Fingerprint      string                  `json:"-"`
}

// PreviewReleaseAction reads Helm storage only. It does not simulate hooks,
// authorize eventual writes, or inspect controller cleanup and live finalizers.
func PreviewReleaseAction(cfg *action.Configuration, name string, options ReleaseActionOptions) (*ReleaseActionPreview, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	current, err := cfg.Releases.Last(name)
	if err != nil {
		return nil, fmt.Errorf("read current Helm release: %w", err)
	}
	if current.Info == nil {
		return nil, fmt.Errorf("current Helm release has no status; no action attempted")
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
		return nil, fmt.Errorf("target Helm release has no status; no action attempted")
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
			"If a GitOps controller manages this release, change its source of truth; direct Helm changes can be reconciled back.",
		},
	}
	if options.Action == "rollback" {
		currentRendered, currentErrors := parseManifestResourceObjects(current.Manifest, current.Namespace)
		if currentErrors > 0 || len(currentRendered) != len(releaseutil.SplitManifests(current.Manifest)) {
			return nil, fmt.Errorf("current release manifest cannot be fully enumerated; no confirmation issued")
		}
		preview.CurrentResources = parseManifestResources(current.Manifest, current.Namespace)
		preview.Warnings = append(preview.Warnings, "Rollback creates a new revision using the target manifest and values. Current resources absent from the target may be deleted; resources retained by helm.sh/resource-policy=keep can remain.")
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
				return nil, fmt.Errorf("release is already uninstalled; keep_history=true has no action to perform")
			}
			preview.Warnings = append(preview.Warnings, "Release is already uninstalled: only its stored Helm history will be purged; resources and hooks will not be processed again.")
		}
	}
	rendered, parseErrors := parseManifestResourceObjects(target.Manifest, target.Namespace)
	if parseErrors > 0 || len(rendered) != len(releaseutil.SplitManifests(target.Manifest)) {
		return nil, fmt.Errorf("release manifest cannot be fully enumerated; no confirmation issued")
	}
	for _, resource := range rendered {
		effect := "delete"
		if options.Action == "rollback" {
			effect = "restore target declaration"
		} else if current.Info.Status == release.StatusUninstalled {
			effect = "not processed (history purge only)"
		} else if resource.Object.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			effect = "keep (helm.sh/resource-policy)"
		}
		preview.Resources = append(preview.Resources, ReleaseActionResource{ResourceRef: resource.Ref, Effect: effect})
	}
	for _, hook := range extractHooks(target) {
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
		preview.Hooks = append(preview.Hooks, ReleaseActionHook{HelmHook: hook, Effect: effect})
	}
	return preview, nil
}

func RunReleaseAction(cfg *action.Configuration, name string, options ReleaseActionOptions, fingerprint string) error {
	preview, err := PreviewReleaseAction(cfg, name, options)
	if err != nil {
		return err
	}
	if fingerprint == "" || preview.Fingerprint != fingerprint {
		return fmt.Errorf("Helm release or options changed; run dry_run=true again and review the new preview")
	}
	if options.Action == "uninstall" {
		return uninstallWithOptions(cfg, name, UninstallOptions{NoHooks: options.NoHooks, KeepHistory: options.KeepHistory})
	}
	rollback := action.NewRollback(cfg)
	rollback.Version = options.Revision
	rollback.DisableHooks = options.NoHooks
	rollback.Timeout = 120 * time.Second
	if err := rollback.Run(name); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	return nil
}

func (c *Client) UninstallWithOptionsAsUser(namespace, name, username string, groups []string, options UninstallOptions) error {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return uninstallWithOptions(cfg, name, options)
}

func uninstallWithOptions(cfg *action.Configuration, name string, options UninstallOptions) error {
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
