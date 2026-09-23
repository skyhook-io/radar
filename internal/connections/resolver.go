package connections

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

type LegacyOffer struct {
	URL        string   `json:"url"`
	HeaderKeys []string `json:"headerKeys"`
	SecretSet  bool     `json:"secretSet"`
	Revision   string   `json:"revision"`
	Error      string   `json:"error,omitempty"`
}

type Usage struct {
	config.ConnectionUse
	Availability string `json:"availability"`
	Revision     string `json:"revision"`
}

type ConnectionView struct {
	ID            string             `json:"id"`
	Type          config.Integration `json:"type"`
	Name          string             `json:"name"`
	CustomName    string             `json:"customName"`
	URL           string             `json:"url"`
	HeaderKeys    []string           `json:"headerKeys"`
	EnvHeaderKeys []string           `json:"envHeaderKeys"`
	SecretSet     bool               `json:"secretSet"`
	InsecureTLS   bool               `json:"insecureTls"`
	Uses          []Usage            `json:"uses"`
	Error         string             `json:"error,omitempty"`
}

type ProfileView struct {
	Target           k8s.ProfileTarget      `json:"target"`
	Revision         string                 `json:"revision"`
	State            string                 `json:"state"`
	Mode             string                 `json:"mode"`
	URL              string                 `json:"url"`
	HeaderKeys       []string               `json:"headerKeys"`
	EnvHeaderKeys    []string               `json:"envHeaderKeys"`
	HeadersManaged   bool                   `json:"headersManaged"`
	SecretSet        bool                   `json:"secretSet"`
	InsecureTLS      bool                   `json:"insecureTls"`
	ClusterID        string                 `json:"clusterId"`
	Connection       *ConnectionView        `json:"connection,omitempty"`
	PreviousIdentity *config.TargetIdentity `json:"previousIdentity,omitempty"`
	Error            string                 `json:"error,omitempty"`
	Legacy           *LegacyOffer           `json:"legacy,omitempty"`
}

type Bundle struct {
	Connection             config.SavedConnection
	Assignment             config.IntegrationAssignment
	Err                    error
	legacyArgoBinding      string
	legacyCostBinding      string
	legacyClusterIDBinding string
}

type Selection struct {
	View ProfileView
	Bundle
	fileRevision string
}

type Resolver struct {
	Store        *config.ProfileStore
	mu           sync.Mutex
	file         config.ClusterProfiles
	fileRevision string
	key          [32]byte
	launchTarget k8s.ProfileTarget
	launch       map[config.Integration]Bundle
}

func NewResolver(store *config.ProfileStore, target k8s.ProfileTarget, launch map[config.Integration]Bundle) *Resolver {
	p := &Resolver{Store: store, launchTarget: target, launch: maps.Clone(launch)}
	rand.Read(p.key[:])
	for kind, bundle := range p.launch {
		bundle.Connection = bundle.Connection.Clone()
		p.launch[kind] = bundle
	}
	return p
}

func (p *Resolver) Revision(digest string) string {
	mac := hmac.New(sha256.New, p.key[:])
	mac.Write([]byte(digest))
	return hex.EncodeToString(mac.Sum(nil))
}

// Redacted credentials participate in conflicts without being returned to the browser.
func (p *Resolver) integrationRevision(file config.ClusterProfiles, kind config.Integration, binding string) string {
	scope := config.ClusterProfiles{
		Version:     file.Version,
		Profiles:    map[string]config.ClusterProfile{},
		Connections: map[string]config.SavedConnection{},
		Imported:    map[config.Integration]bool{kind: file.Imported[kind]},
		Dismissed:   map[string]map[config.Integration]bool{},
	}
	if profile, ok := file.Profiles[binding]; ok {
		if assignment, ok := profile.Integrations[kind]; ok {
			profile.Integrations = map[config.Integration]config.IntegrationAssignment{kind: assignment}
			scope.Profiles[binding] = profile
			if assignment.ConnectionID != "" {
				scope.Connections[assignment.ConnectionID] = file.Connections[assignment.ConnectionID]
			}
		}
	}
	if file.Dismissed[binding][kind] {
		scope.Dismissed[binding] = map[config.Integration]bool{kind: true}
	}
	data, _ := json.Marshal(scope)
	return p.Revision(string(data))
}

func (p *Resolver) read() (config.ClusterProfiles, string, error) {
	file, revision, changed, err := p.Store.ReadSince(p.fileRevision)
	if err != nil {
		p.fileRevision = ""
		p.file = config.ClusterProfiles{}
		return file, "", err
	}
	if changed || p.file.Profiles == nil {
		p.file = file
		p.fileRevision = revision
	}
	return p.file, revision, nil
}

func HeaderKeys(headers map[string]string) []string {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func connectionView(id string, c config.SavedConnection) ConnectionView {
	v := ConnectionView{ID: id, Type: c.Type, Name: c.DisplayName(), CustomName: c.Name, URL: prom.SafeAddress(c.URL()), HeaderKeys: []string{}, EnvHeaderKeys: []string{}, Uses: []Usage{}}
	if c.Prometheus != nil {
		v.HeaderKeys = HeaderKeys(c.Prometheus.Headers)
		v.EnvHeaderKeys = HeaderKeys(c.Prometheus.HeadersFromEnv)
		v.HeaderKeys = append(v.HeaderKeys, v.EnvHeaderKeys...)
		slices.Sort(v.HeaderKeys)
		v.SecretSet = len(v.HeaderKeys) > 0
	}
	if c.ArgoCD != nil {
		v.SecretSet = c.ArgoCD.Token != ""
		v.InsecureTLS = c.ArgoCD.InsecureTLS
	}
	if c.Kubecost != nil {
		v.SecretSet = c.Kubecost.APIKey != ""
	}
	if err := c.Validate(); err != nil {
		v.Error = err.Error()
	}
	return v
}

func (p *Resolver) uses(file config.ClusterProfiles, id string) []Usage {
	result := []Usage{}
	for _, use := range file.Uses(id) {
		availability := "unavailable"
		ref := k8s.ContextForSafetyBinding(use.Binding)
		if !ref.Empty() {
			availability = "available"
			use.Context = ref.Name
		} else if k8s.ContextReferenceKnownMissing(k8s.ContextRef{SourceFile: use.Source, InFileName: use.InFileName}) {
			availability = "removed"
		}
		result = append(result, Usage{ConnectionUse: use, Availability: availability, Revision: p.integrationRevision(file, use.Integration, use.Binding)})
	}
	return result
}

func (p *Resolver) UnlinkedAssignments() ([]Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	file, _, err := p.read()
	if err != nil {
		return nil, err
	}
	return p.uses(file, ""), nil
}

func (p *Resolver) Catalog() ([]ConnectionView, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	file, revision, err := p.read()
	if err != nil {
		return nil, "", err
	}
	items := make([]ConnectionView, 0, len(file.Connections))
	for id, c := range file.Connections {
		v := connectionView(id, c)
		v.Uses = p.uses(file, id)
		items = append(items, v)
	}
	slices.SortFunc(items, func(a, b ConnectionView) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return items, p.Revision(revision), nil
}

func (p *Resolver) Resolve(target k8s.ProfileTarget, kind config.Integration, details bool) Selection {
	p.mu.Lock()
	defer p.mu.Unlock()
	file, revision, err := p.read()
	return p.resolve(file, revision, err, target, kind, details)
}

func (p *Resolver) ResolveAll(target k8s.ProfileTarget, details bool) map[config.Integration]Selection {
	p.mu.Lock()
	defer p.mu.Unlock()
	file, revision, err := p.read()
	selected := make(map[config.Integration]Selection, len(config.IntegrationKinds))
	for _, kind := range config.IntegrationKinds {
		selected[kind] = p.resolve(file, revision, err, target, kind, details)
	}
	return selected
}

func (p *Resolver) resolve(file config.ClusterProfiles, revision string, err error, target k8s.ProfileTarget, kind config.Integration, details bool) Selection {
	s := Selection{View: ProfileView{Target: target, Revision: p.integrationRevision(file, kind, target.Binding), State: "auto", Mode: "auto", HeaderKeys: []string{}, EnvHeaderKeys: []string{}}, fileRevision: revision}
	s.Assignment = config.IntegrationAssignment{Mode: "auto"}
	s.Connection = emptyConnection(kind)
	if err != nil {
		s.Err = err
	} else {
		a := file.Assignment(target.Binding, kind)
		s.Assignment = a
		s.View.Mode = a.Mode
		s.View.ClusterID = a.ClusterID
		if _, exists := file.Profiles[target.Binding].Integrations[kind]; exists {
			s.View.State = "saved"
		}
		if err := a.Validate(kind, file.Connections); err != nil {
			s.Err = err
		}
		if a.ConnectionID != "" {
			s.Connection = file.Connections[a.ConnectionID].Clone()
			view := connectionView(a.ConnectionID, s.Connection)
			if details {
				view.Uses = p.uses(file, a.ConnectionID)
			}
			s.View.Connection = &view
		} else {
			if a.ArgoCD != nil {
				aCopy := *a.ArgoCD
				s.Connection.ArgoCD = &aCopy
			}
			if a.Kubecost != nil {
				kCopy := *a.Kubecost
				s.Connection.Kubecost = &kCopy
			}
		}
		if a.NeedsTarget() && a.Target != target.Fingerprint {
			s.View.State = "target_changed"
			s.View.PreviousIdentity = a.Identity
			s.Err = errors.New("cluster connection changed; review its saved connection in Settings")
		}
		if details && !file.Imported[kind] && !file.Dismissed[target.Binding][kind] && s.View.State == "auto" {
			legacy, digest := Legacy(kind)
			if hasLegacy(legacy) {
				v := connectionView("", legacy.Connection)
				s.View.Legacy = &LegacyOffer{URL: v.URL, HeaderKeys: v.HeaderKeys, SecretSet: v.SecretSet, Revision: p.Revision(digest)}
				if legacy.Err != nil {
					s.View.Legacy.Error = legacy.Err.Error()
				}
			}
		}
	}
	if launch, ok := p.launch[kind]; ok && target.Binding == p.launchTarget.Binding && target.Fingerprint == p.launchTarget.Fingerprint {
		s.Bundle = launch
		s.Connection = launch.Connection.Clone()
		s.View.State = "launch"
		s.View.Mode = launch.Assignment.Mode
		s.View.ClusterID = launch.Assignment.ClusterID
		s.View.Connection = nil
		s.View.Legacy = nil
	}
	v := connectionView("", s.Connection)
	s.View.URL = v.URL
	s.View.HeaderKeys = v.HeaderKeys
	s.View.EnvHeaderKeys = v.EnvHeaderKeys
	s.View.HeadersManaged = len(v.EnvHeaderKeys) > 0
	s.View.SecretSet = v.SecretSet
	s.View.InsecureTLS = v.InsecureTLS
	if s.Err == nil && s.Connection.Prometheus != nil {
		headers, err := prom.ResolveHeaders(s.Connection.Prometheus.Headers, s.Connection.Prometheus.HeadersFromEnv)
		if err != nil {
			s.Err = err
		} else {
			s.Connection.Prometheus.Headers = headers
			s.Connection.Prometheus.HeadersFromEnv = nil
		}
	}
	if s.Err != nil {
		if s.View.State != "target_changed" && s.View.State != "launch" {
			s.View.State = "error"
		}
		s.View.Error = s.Err.Error()
	}
	return s
}

func emptyConnection(kind config.Integration) config.SavedConnection {
	c := config.SavedConnection{Type: kind}
	switch kind {
	case config.IntegrationMetrics:
		c.Prometheus = &prom.Connection{}
	case config.IntegrationArgoCD:
		c.ArgoCD = &argoapi.Connection{}
	case config.IntegrationCost:
		c.Kubecost = &opencost.Connection{}
	}
	return c
}

func (p *Resolver) IsCurrent(selection Selection) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, revision, err := p.read()
	return err == nil && revision == selection.fileRevision
}

func Legacy(kind config.Integration) (Bundle, string) {
	c := config.Load()
	b := Bundle{Connection: emptyConnection(kind), Assignment: config.IntegrationAssignment{Mode: "auto"}}
	b.legacyArgoBinding, b.legacyCostBinding, b.legacyClusterIDBinding = c.ArgoCDTokenBinding, c.KubecostAPIKeyContext, c.KubecostClusterIDContext
	switch kind {
	case config.IntegrationMetrics:
		b.Connection.Prometheus = &prom.Connection{URL: c.PrometheusURL, Headers: c.PrometheusHeaders, HeadersFromEnv: c.PrometheusHeadersFromEnv}
		b.Err = b.Connection.Prometheus.Validate()
	case config.IntegrationArgoCD:
		b.Connection.ArgoCD = &argoapi.Connection{URL: c.ArgoCDURL, Token: c.ArgoCDToken, InsecureTLS: c.ArgoCDInsecureTLS}
		b.Assignment.Target = c.ArgoCDTokenBinding
		b.Err = b.Connection.ArgoCD.Validate()
	case config.IntegrationCost:
		b.Connection.Kubecost = &opencost.Connection{URL: c.KubecostURL, APIKey: c.KubecostAPIKey}
		b.Assignment.Mode = c.CostSource
		if b.Assignment.Mode == "" {
			b.Assignment.Mode = "auto"
		}
		b.Assignment.ClusterID = c.KubecostClusterID
		if b.Assignment.Mode == "prometheus" {
			b.Connection.Kubecost = &opencost.Connection{}
			b.Assignment.ClusterID = ""
		}
		b.Err = b.Connection.Kubecost.Validate()
	}
	data, _ := json.Marshal(struct {
		Connection                                 config.SavedConnection
		Assignment                                 config.IntegrationAssignment
		ArgoBinding, CostBinding, ClusterIDBinding string
	}{b.Connection, b.Assignment, c.ArgoCDTokenBinding, c.KubecostAPIKeyContext, c.KubecostClusterIDContext})
	digest := sha256.Sum256(data)
	return b, hex.EncodeToString(digest[:])
}

func hasLegacy(b Bundle) bool {
	c := b.Connection
	return c.URL() != "" || c.Prometheus != nil && len(c.Prometheus.Headers)+len(c.Prometheus.HeadersFromEnv) > 0 || c.ArgoCD != nil && (c.ArgoCD.Token != "" || c.ArgoCD.InsecureTLS) || c.Kubecost != nil && (c.Kubecost.APIKey != "" || b.Assignment.ClusterID != "" || b.Assignment.Mode != "auto")
}
