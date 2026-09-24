package connections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

type SecretEdit struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

type Update struct {
	Target         k8s.ProfileTarget             `json:"target"`
	Revision       string                        `json:"revision"`
	Revisions      map[config.Integration]string `json:"revisions,omitempty"`
	Kind           config.Integration            `json:"kind"`
	Action         string                        `json:"action"`
	Binding        string                        `json:"binding,omitempty"`
	SourceRevision string                        `json:"sourceRevision,omitempty"`
	URL            *string                       `json:"url,omitempty"`
	Headers        []prom.HeaderOperation        `json:"headers,omitempty"`
	Secret         *SecretEdit                   `json:"secret,omitempty"`
	InsecureTLS    *bool                         `json:"insecureTls,omitempty"`
	Mode           *string                       `json:"mode,omitempty"`
	ClusterID      *string                       `json:"clusterId,omitempty"`
	ConfirmRemoval bool                          `json:"confirmRemoval"`
	LegacyRevision string                        `json:"legacyRevision,omitempty"`
	UseCLIToken    bool                          `json:"useCliToken,omitempty"`
	Kinds          []config.Integration          `json:"kinds,omitempty"`
}

type Pending struct {
	Target         k8s.ProfileTarget
	Kind           config.Integration
	Candidate      Bundle
	Probe          bool
	file           config.ClusterProfiles
	revision       string
	legacyRevision string
}

func (p *Resolver) Commit(ctx context.Context, pending Pending) error {
	_, err := p.Store.Update(ctx, pending.revision, func(file *config.ClusterProfiles) error {
		if pending.legacyRevision != "" {
			_, digest := Legacy(pending.Kind)
			if digest != pending.legacyRevision {
				return config.ErrProfileConflict
			}
		}
		*file = pending.file
		return nil
	})
	return err
}

func (p *Resolver) Prepare(target k8s.ProfileTarget, req Update) (Pending, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var pending Pending
	file, revision, err := p.read()
	if err != nil {
		return pending, err
	}
	if req.Action != "forget" && req.Revision != p.integrationRevision(file, req.Kind, target.Binding) {
		return pending, config.ErrProfileConflict
	}
	if (req.Action == "copy" || req.Action == "forget") && req.SourceRevision != p.integrationRevision(file, req.Kind, req.Binding) {
		return pending, config.ErrProfileConflict
	}
	if !slices.Contains(config.IntegrationKinds, req.Kind) {
		return pending, errors.New("unknown integration type")
	}
	data, err := json.Marshal(file)
	if err != nil {
		return pending, err
	}
	var next config.ClusterProfiles
	if err := json.Unmarshal(data, &next); err != nil {
		return pending, err
	}
	if next.Imported == nil {
		next.Imported = map[config.Integration]bool{}
	}
	if next.Dismissed == nil {
		next.Dismissed = map[string]map[config.Integration]bool{}
	}
	pending = Pending{Target: target, Kind: req.Kind, file: next, revision: revision}
	metadata := req.Action == "forget"
	if !metadata {
		if !target.Same(req.Target) {
			return pending, config.ErrProfileConflict
		}
		if _, exists := p.launch[req.Kind]; exists && target.Binding == p.launchTarget.Binding && target.Fingerprint == p.launchTarget.Fingerprint {
			return pending, errors.New("this integration is set for this launch; restart without its startup override to edit saved settings")
		}
	}
	profile := next.Profiles[target.Binding]
	a := next.Settings(target.Binding, req.Kind)
	a = a.Clone()
	if a.NeedsTarget() && a.Target != target.Fingerprint && !metadata && req.Action != "reconfirm" && req.Action != "replace" && req.Action != "copy" && req.Action != "auto" && req.Action != "dismiss_legacy" {
		return pending, errors.New("review the changed cluster target before editing its connection")
	}
	switch req.Action {
	case "forget":
		old := next.Settings(req.Binding, req.Kind)
		if old.NeedsTarget() && !req.ConfirmRemoval {
			return pending, errors.New("confirm removal of this context's saved connection and credentials")
		}
		if err := next.RemoveSettings(req.Binding, req.Kind); err != nil {
			return pending, err
		}
		dismiss(&next, req.Binding, req.Kind)
	case "dismiss_legacy":
		dismiss(&next, target.Binding, req.Kind)
	case "reconfirm":
		if len(req.Kinds) == 0 {
			return pending, errors.New("select the integrations to confirm")
		}
		for _, kind := range req.Kinds {
			if !slices.Contains(config.IntegrationKinds, kind) {
				return pending, errors.New("unknown integration type")
			}
			if req.Revisions[kind] != p.integrationRevision(file, kind, target.Binding) {
				return pending, config.ErrProfileConflict
			}
			selected := next.Settings(target.Binding, kind)
			if !selected.NeedsTarget() || selected.Target == target.Fingerprint {
				return pending, errors.New("selected integration has no cluster change to confirm")
			}
			selected.Target = target.Fingerprint
			identity := target.Identity
			selected.Identity = &identity
			profile.Integrations[kind] = selected
		}
		next.Profiles[target.Binding] = profile
	case "auto":
		if req.Mode != nil && req.Kind != config.IntegrationCost {
			return pending, errors.New("source selection is only available for cost")
		}
		mode := "auto"
		if req.Mode != nil {
			mode = *req.Mode
		}
		if mode != "auto" && mode != "prometheus" {
			return pending, errors.New("choose auto-discovery or this context's Prometheus connection")
		}
		if _, exists := profile.Integrations[req.Kind]; exists {
			if a.NeedsTarget() && !req.ConfirmRemoval {
				return pending, errors.New("confirm removal of this context's saved connection and credentials")
			}
			if err := next.RemoveSettings(target.Binding, req.Kind); err != nil {
				return pending, err
			}
		}
		profile = next.Profiles[target.Binding]
		a = config.IntegrationSettings{}
		if req.Kind == config.IntegrationCost {
			a.Mode = mode
		}
		putSettings(&next, target, req.Kind, profile, a)
		dismiss(&next, target.Binding, req.Kind)
	case "copy", "adopt", "save", "replace":
		removing := req.Action == "replace" && a.NeedsTarget() || req.Action == "save" && a.URL() != "" && req.URL != nil && strings.TrimSpace(*req.URL) == ""
		if removing && !req.ConfirmRemoval {
			return pending, errors.New("confirm removal of the previous connection and its saved credentials")
		}
		if req.Action == "copy" {
			if a.NeedsTarget() && !req.ConfirmRemoval {
				return pending, errors.New("confirm replacement of this context's saved settings and credentials")
			}
			if req.Binding == "" || req.Binding == target.Binding {
				return pending, errors.New("choose another cluster to copy from")
			}
			source, exists := next.Profiles[req.Binding].Integrations[req.Kind]
			if !exists || source.URL() == "" {
				return pending, errors.New("source cluster has no explicit connection to copy")
			}
			if err := source.Validate(req.Kind); err != nil {
				return pending, err
			}
			clusterID := a.ClusterID
			if a.NeedsTarget() && a.Target != target.Fingerprint {
				clusterID = ""
			}
			a = source.Clone()
			a.ClusterID = clusterID
			if err := editSettings(&a, req); err != nil {
				return pending, err
			}
			if a.Prometheus != nil {
				if _, err := prom.ResolveHeaders(a.Prometheus.Headers, a.Prometheus.HeadersFromEnv); err != nil {
					return pending, err
				}
			}
			if req.Kind == config.IntegrationCost {
				a.Mode = "kubecost"
			}
		} else {
			if req.Action == "adopt" {
				if _, exists := profile.Integrations[req.Kind]; exists {
					return pending, errors.New("this context already has settings; copy from another cluster or replace them explicitly")
				}
				if next.Imported[req.Kind] || next.Dismissed[target.Binding][req.Kind] {
					return pending, errors.New("legacy settings have already been imported or dismissed")
				}
				legacy, digest := Legacy(req.Kind)
				pending.legacyRevision = digest
				if req.LegacyRevision == "" || req.LegacyRevision != p.Revision(digest) {
					return pending, config.ErrProfileConflict
				}
				if legacy.Err != nil {
					return pending, legacy.Err
				}
				if !hasLegacy(legacy) {
					return pending, errors.New("no legacy connection to adopt")
				}
				a = legacySettingsForTarget(req.Kind, target, legacy)
				if err := editSettings(&a, req); err != nil {
					return pending, err
				}
				if a.URL() != "" {
					next.Imported[req.Kind] = true
				}
			} else {
				if req.Action == "replace" {
					a = config.IntegrationSettings{}
				}
				if err := editSettings(&a, req); err != nil {
					return pending, err
				}
			}
		}
		if req.Kind == config.IntegrationCost && a.URL() != "" && a.EffectiveMode(req.Kind) == "auto" {
			a.Mode = "kubecost"
		}
		if req.Kind == config.IntegrationCost {
			if req.Mode != nil {
				a.Mode = *req.Mode
			}
			if req.ClusterID != nil {
				a.ClusterID = strings.TrimSpace(*req.ClusterID)
			}
		}
		a.Target = target.Fingerprint
		identity := target.Identity
		a.Identity = &identity
		putSettings(&next, target, req.Kind, profile, a)
		dismiss(&next, target.Binding, req.Kind)
		pending.Probe = true
		pending.Candidate = Bundle{Settings: a.Clone()}
	default:
		return pending, errors.New("unknown connection action")
	}
	if err := next.ValidateStructure(); err != nil {
		return pending, err
	}
	if err := next.ValidateChanges(file); err != nil {
		return pending, err
	}
	pending.file = next
	return pending, nil
}

func putSettings(file *config.ClusterProfiles, target k8s.ProfileTarget, kind config.Integration, profile config.ClusterProfile, a config.IntegrationSettings) {
	if a.Prometheus != nil && a.Prometheus.URL == "" && len(a.Prometheus.Headers) == 0 && len(a.Prometheus.HeadersFromEnv) == 0 {
		a.Prometheus = nil
	}
	if a.ArgoCD != nil && *a.ArgoCD == (argoapi.Connection{}) {
		a.ArgoCD = nil
	}
	if a.Kubecost != nil && *a.Kubecost == (opencost.Connection{}) {
		a.Kubecost = nil
	}
	if !a.NeedsTarget() {
		a.Target, a.Identity = "", nil
	}
	profile.Context = target.Context
	profile.Source = target.Source
	profile.InFileName = target.InFileName
	profile.CAPI = target.CAPI
	if profile.Integrations == nil {
		profile.Integrations = map[config.Integration]config.IntegrationSettings{}
	}
	profile.Integrations[kind] = a
	file.Profiles[target.Binding] = profile
}

func dismiss(file *config.ClusterProfiles, binding string, kind config.Integration) {
	if file.Dismissed[binding] == nil {
		file.Dismissed[binding] = map[config.Integration]bool{}
	}
	file.Dismissed[binding][kind] = true
}

func editSettings(c *config.IntegrationSettings, req Update) error {
	*c = settingsWithDefaults(req.Kind, *c)
	previousURL := c.URL()
	newURL := previousURL
	if req.URL != nil {
		newURL = strings.TrimSpace(*req.URL)
	}
	if c.Prometheus != nil {
		if req.Secret != nil || req.InsecureTLS != nil || req.UseCLIToken {
			return errors.New("metrics uses header operations, not a token or TLS override")
		}
		updated, err := prom.ApplyHeaderOperations(*c.Prometheus, newURL, req.Headers)
		if err != nil {
			return err
		}
		c.Prometheus = &updated
	} else {
		if len(req.Headers) > 0 {
			return errors.New("header operations are only available for metrics")
		}
		token := ""
		if c.ArgoCD != nil {
			token = c.ArgoCD.Token
		} else if c.Kubecost != nil {
			token = c.Kubecost.APIKey
		} else {
			return errors.New("invalid saved connection")
		}
		edit := req.Secret
		if req.UseCLIToken {
			if req.Kind != config.IntegrationArgoCD || newURL == "" || edit != nil {
				return errors.New("CLI token adoption requires an explicit Argo CD URL and no token edit")
			}
			cliToken, err := argoapi.TokenFromCLIConfig("", newURL)
			if err != nil {
				return errors.New("could not read an Argo CD CLI token for this server")
			}
			edit = &SecretEdit{Action: "set", Value: cliToken}
		}
		if edit == nil {
			edit = &SecretEdit{Action: "keep"}
		}
		switch edit.Action {
		case "keep":
			if edit.Value != "" {
				return errors.New("keep must not include a secret value")
			}
			if token != "" && previousURL != newURL {
				before, ok := prom.NormalizeOrigin(previousURL)
				after, valid := prom.NormalizeOrigin(newURL)
				if !ok || !valid || before != after {
					return errors.New("changing servers requires replacing or clearing the saved credential")
				}
			}
		case "set":
			token = edit.Value
		case "clear":
			if edit.Value != "" {
				return errors.New("clear must not include a secret value")
			}
			token = ""
		default:
			return errors.New("secret action must be keep, set, or clear")
		}
		if c.ArgoCD != nil {
			c.ArgoCD.URL = newURL
			c.ArgoCD.Token = token
			if req.InsecureTLS != nil {
				c.ArgoCD.InsecureTLS = *req.InsecureTLS
			}
			return c.ArgoCD.Validate()
		}
		if req.InsecureTLS != nil {
			return errors.New("Kubecost does not support a TLS verification override")
		}
		c.Kubecost.URL = newURL
		c.Kubecost.APIKey = token
		return c.Kubecost.Validate()
	}
	return nil
}
