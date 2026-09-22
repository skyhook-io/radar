package connections

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
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
	ConnectionID   string                        `json:"connectionId,omitempty"`
	Binding        string                        `json:"binding,omitempty"`
	Name           string                        `json:"name,omitempty"`
	URL            *string                       `json:"url,omitempty"`
	Headers        []prom.HeaderOperation        `json:"headers,omitempty"`
	Secret         *SecretEdit                   `json:"secret,omitempty"`
	InsecureTLS    *bool                         `json:"insecureTls,omitempty"`
	Mode           *string                       `json:"mode,omitempty"`
	ClusterID      *string                       `json:"clusterId,omitempty"`
	KeepUnused     bool                          `json:"keepUnused"`
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
	Shared         bool
	Affected       int
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
	if req.Revision != p.integrationRevisions[req.Kind] {
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
	pending = Pending{Target: target, Kind: req.Kind, file: next, revision: revision, Affected: 1}
	metadata := req.Action == "rename" || req.Action == "delete" || req.Action == "forget"
	if !metadata {
		if !target.Same(req.Target) {
			return pending, config.ErrProfileConflict
		}
		if _, exists := p.launch[req.Kind]; exists && target.Binding == p.launchTarget.Binding && target.Fingerprint == p.launchTarget.Fingerprint {
			return pending, errors.New("this integration is set for this launch; restart without its startup override to edit saved settings")
		}
	}
	profile := next.Profiles[target.Binding]
	a := next.Assignment(target.Binding, req.Kind)
	previousID := a.ConnectionID
	connection := emptyConnection(req.Kind)
	if previousID != "" {
		connection = next.Connections[previousID].Clone()
	} else {
		if a.ArgoCD != nil {
			c := *a.ArgoCD
			connection.ArgoCD = &c
		}
		if a.Kubecost != nil {
			c := *a.Kubecost
			connection.Kubecost = &c
		}
	}
	if a.NeedsTarget() && a.Target != target.Fingerprint && !metadata && req.Action != "reconfirm" && req.Action != "replace" && req.Action != "use" && req.Action != "auto" && req.Action != "dismiss_legacy" {
		return pending, errors.New("review the changed cluster target before editing its connection")
	}
	switch req.Action {
	case "mapping":
		if req.Kind != config.IntegrationCost || req.ClusterID == nil {
			return pending, errors.New("cluster mapping requires a Kubecost cluster ID")
		}
		a.ClusterID = strings.TrimSpace(*req.ClusterID)
		a.Target = target.Fingerprint
		identity := target.Identity
		a.Identity = &identity
		putAssignment(&next, target, req.Kind, profile, a)
		pending.Probe = true
		pending.Candidate = Bundle{Connection: connection.Clone(), Assignment: a}
	case "rename":
		c, ok := next.Connections[req.ConnectionID]
		if !ok || c.Type != req.Kind {
			return pending, errors.New("saved connection not found")
		}
		c.Name = strings.TrimSpace(req.Name)
		next.Connections[req.ConnectionID] = c
	case "delete":
		if c, ok := next.Connections[req.ConnectionID]; !ok || c.Type != req.Kind {
			return pending, errors.New("saved connection not found")
		}
		if !req.ConfirmRemoval {
			return pending, errors.New("confirm deletion of the saved connection and its credentials")
		}
		if len(next.Uses(req.ConnectionID)) != 0 {
			return pending, errors.New("remove the connection's assignments before deleting it")
		}
		delete(next.Connections, req.ConnectionID)
	case "forget":
		old := next.Assignment(req.Binding, req.Kind)
		if old.ConnectionID != "" && len(next.Uses(old.ConnectionID)) == 1 && !req.KeepUnused && !req.ConfirmRemoval {
			return pending, errors.New("confirm removal of this connection's last assignment and saved credentials")
		}
		if old.ConnectionID == "" && old.NeedsTarget() && !req.ConfirmRemoval {
			return pending, errors.New("confirm removal of this context's saved settings")
		}
		if err := next.RemoveAssignment(req.Binding, req.Kind, req.KeepUnused); err != nil {
			return pending, err
		}
		dismiss(&next, req.Binding, req.Kind)
	case "dismiss_legacy":
		next.Imported[req.Kind] = true
	case "reconfirm":
		if len(req.Kinds) == 0 {
			return pending, errors.New("select the integrations to confirm")
		}
		for _, kind := range req.Kinds {
			if !slices.Contains(config.IntegrationKinds, kind) {
				return pending, errors.New("unknown integration type")
			}
			if req.Revisions[kind] != p.integrationRevisions[kind] {
				return pending, config.ErrProfileConflict
			}
			selected := next.Assignment(target.Binding, kind)
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
			if previousID != "" && len(next.Uses(previousID)) == 1 && !req.KeepUnused && !req.ConfirmRemoval {
				return pending, errors.New("confirm removal of this connection's last assignment and saved credentials")
			}
			if previousID == "" && a.NeedsTarget() && !req.ConfirmRemoval {
				return pending, errors.New("confirm removal of this context's saved credentials and mapping")
			}
			if err := next.RemoveAssignment(target.Binding, req.Kind, req.KeepUnused); err != nil {
				return pending, err
			}
		}
		profile = next.Profiles[target.Binding]
		a = config.IntegrationAssignment{Mode: mode}
		putAssignment(&next, target, req.Kind, profile, a)
		dismiss(&next, target.Binding, req.Kind)
	case "use", "adopt", "save", "replace", "update_shared", "fork":
		if req.Action == "use" {
			if a.NeedsTarget() && a.Target != target.Fingerprint {
				a.ClusterID = ""
			}
			var exists bool
			connection, exists = next.Connections[req.ConnectionID]
			if !exists || connection.Type != req.Kind {
				return pending, errors.New("saved connection not found for this integration")
			}
			connection = connection.Clone()
			a.ConnectionID = req.ConnectionID
			a.ArgoCD = nil
			a.Kubecost = nil
			if req.Kind == config.IntegrationCost {
				a.Mode = "kubecost"
			} else {
				a.Mode = "connection"
			}
		} else {
			if req.Action == "adopt" {
				if _, exists := profile.Integrations[req.Kind]; exists {
					return pending, errors.New("this context already has settings; use a saved connection or replace them explicitly")
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
				if err := validateLegacyBinding(req.Kind, target, legacy); err != nil {
					return pending, err
				}
				connection = legacy.Connection.Clone()
				if req.Kind == config.IntegrationCost {
					a.Mode = legacy.Assignment.Mode
					a.ClusterID = legacy.Assignment.ClusterID
				}
				if connection.URL() != "" {
					next.Imported[req.Kind] = true
				}
			} else {
				if req.Action == "replace" {
					connection = emptyConnection(req.Kind)
					a = config.IntegrationAssignment{Mode: "auto"}
				}
				if err := editConnection(&connection, req); err != nil {
					return pending, err
				}
			}
			if req.Action == "update_shared" {
				if req.Mode != nil && *req.Mode != a.Mode || req.ClusterID != nil && strings.TrimSpace(*req.ClusterID) != a.ClusterID {
					return pending, errors.New("edit this context's source or cluster mapping separately from the shared backend")
				}
				if previousID == "" || a.ConnectionID == "" {
					return pending, errors.New("no saved connection to update")
				}
				pending.Shared = true
				pending.Affected = len(next.Uses(previousID))
			} else if req.Action == "save" && previousID != "" && len(next.Uses(previousID)) > 1 {
				return pending, errors.New("choose Edit shared connection or Customize for this cluster")
			}
			if connection.URL() == "" {
				if req.Action == "update_shared" || req.Action == "fork" {
					return pending, errors.New("shared connections require an explicit URL; use auto-discovery only for this context")
				}
				a.ConnectionID = ""
				if req.Kind != config.IntegrationCost {
					a.Mode = "auto"
				}
				if req.Kind == config.IntegrationMetrics {
					if err := connection.Prometheus.Validate(); err != nil {
						return pending, err
					}
				} else if req.Kind == config.IntegrationArgoCD {
					a.ArgoCD = nil
					if connection.ArgoCD.Token != "" || connection.ArgoCD.InsecureTLS {
						a.ArgoCD = connection.ArgoCD
					}
				} else {
					a.Kubecost = nil
					if connection.Kubecost.APIKey != "" {
						a.Kubecost = connection.Kubecost
					}
				}
			} else {
				if err := connection.Validate(); err != nil {
					return pending, err
				}
				id := previousID
				if id == "" || req.Action == "fork" || req.Action == "replace" || req.Action == "adopt" {
					id = "conn_" + rand.Text()
				}
				next.Connections[id] = connection.Clone()
				a.ConnectionID = id
				a.ArgoCD = nil
				a.Kubecost = nil
				if req.Kind == config.IntegrationCost {
					if a.Mode == "auto" {
						a.Mode = "kubecost"
					}
				} else {
					a.Mode = "connection"
				}
			}
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
		putAssignment(&next, target, req.Kind, profile, a)
		if previousID != "" && previousID != a.ConnectionID && len(next.Uses(previousID)) == 0 && !req.KeepUnused {
			if !req.ConfirmRemoval {
				return pending, errors.New("confirm removal of the previous connection and its saved credentials")
			}
			delete(next.Connections, previousID)
		}
		dismiss(&next, target.Binding, req.Kind)
		pending.Probe = true
		pending.Candidate = Bundle{Connection: connection.Clone(), Assignment: a}
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

func putAssignment(file *config.ClusterProfiles, target k8s.ProfileTarget, kind config.Integration, profile config.ClusterProfile, a config.IntegrationAssignment) {
	profile.Context = target.Context
	profile.Source = target.Source
	profile.InFileName = target.InFileName
	profile.CAPI = target.CAPI
	if profile.Integrations == nil {
		profile.Integrations = map[config.Integration]config.IntegrationAssignment{}
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

func editConnection(c *config.SavedConnection, req Update) error {
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

func validateLegacyBinding(kind config.Integration, target k8s.ProfileTarget, bundle Bundle) error {
	if kind == config.IntegrationArgoCD && bundle.Connection.ArgoCD.URL == "" && bundle.Connection.ArgoCD.Token != "" && bundle.legacyArgoBinding != target.Binding {
		return errors.New("re-enter the Argo CD discovery token to bind it to this kubeconfig source")
	}
	if kind == config.IntegrationCost {
		if bundle.Connection.Kubecost.URL == "" && bundle.Connection.Kubecost.APIKey != "" && bundle.legacyCostBinding != target.Context {
			return errors.New("re-enter the Kubecost discovery key for this context")
		}
		if bundle.Assignment.ClusterID != "" && bundle.legacyClusterIDBinding != target.Context {
			return errors.New("set the Kubecost cluster ID for this context before adopting")
		}
	}
	return nil
}
