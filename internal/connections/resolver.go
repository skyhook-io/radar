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
	"strings"
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

type StoredSettingsView struct {
	Binding       string             `json:"binding"`
	Integration   config.Integration `json:"integration"`
	Context       string             `json:"context"`
	Source        string             `json:"source,omitempty"`
	InFileName    string             `json:"inFileName,omitempty"`
	Availability  string             `json:"availability"`
	Revision      string             `json:"revision"`
	URL           string             `json:"url"`
	HeaderKeys    []string           `json:"headerKeys"`
	EnvHeaderKeys []string           `json:"envHeaderKeys"`
	SecretSet     bool               `json:"secretSet"`
	InsecureTLS   bool               `json:"insecureTls"`
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
	PreviousIdentity *config.TargetIdentity `json:"previousIdentity,omitempty"`
	Error            string                 `json:"error,omitempty"`
	Legacy           *LegacyOffer           `json:"legacy,omitempty"`
}

type Bundle struct {
	Settings               config.IntegrationSettings
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
		bundle.Settings = bundle.Settings.Clone()
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
		Version:   file.Version,
		Profiles:  map[string]config.ClusterProfile{},
		Imported:  map[config.Integration]bool{kind: file.Imported[kind]},
		Dismissed: map[string]map[config.Integration]bool{},
	}
	if profile, ok := file.Profiles[binding]; ok {
		if settings, ok := profile.Integrations[kind]; ok {
			profile.Integrations = map[config.Integration]config.IntegrationSettings{kind: settings}
			scope.Profiles[binding] = profile
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

func settingsView(s config.IntegrationSettings) StoredSettingsView {
	v := StoredSettingsView{URL: prom.SafeAddress(s.URL()), HeaderKeys: []string{}, EnvHeaderKeys: []string{}}
	if s.Prometheus != nil {
		v.HeaderKeys = HeaderKeys(s.Prometheus.Headers)
		v.EnvHeaderKeys = HeaderKeys(s.Prometheus.HeadersFromEnv)
		v.HeaderKeys = append(v.HeaderKeys, v.EnvHeaderKeys...)
		slices.Sort(v.HeaderKeys)
		v.SecretSet = len(v.HeaderKeys) > 0
	}
	if s.ArgoCD != nil {
		v.SecretSet = s.ArgoCD.Token != ""
		v.InsecureTLS = s.ArgoCD.InsecureTLS
	}
	if s.Kubecost != nil {
		v.SecretSet = s.Kubecost.APIKey != ""
	}
	return v
}

func (p *Resolver) Catalog() ([]StoredSettingsView, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	file, _, err := p.read()
	if err != nil {
		return nil, err
	}
	items := []StoredSettingsView{}
	for binding, profile := range file.Profiles {
		for kind, settings := range profile.Integrations {
			v := settingsView(settings)
			v.Binding, v.Integration, v.Context = binding, kind, profile.Context
			v.Source, v.InFileName = profile.Source, profile.InFileName
			v.Revision = p.integrationRevision(file, kind, binding)
			v.Availability = "unavailable"
			ref := k8s.ContextForSafetyBinding(binding)
			if !ref.Empty() {
				v.Availability, v.Context = "available", ref.Name
			} else if k8s.ContextReferenceKnownMissing(k8s.ContextRef{SourceFile: profile.Source, InFileName: profile.InFileName}) {
				v.Availability = "removed"
			}
			if err := settings.Validate(kind); err != nil {
				v.Error = err.Error()
			}
			items = append(items, v)
		}
	}
	slices.SortFunc(items, func(a, b StoredSettingsView) int {
		if n := strings.Compare(a.Context, b.Context); n != 0 {
			return n
		}
		return strings.Compare(a.Binding+string(a.Integration), b.Binding+string(b.Integration))
	})
	return items, nil
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
	s.Settings = settingsWithDefaults(kind, config.IntegrationSettings{})
	if err != nil {
		s.Err = err
	} else {
		a := file.Settings(target.Binding, kind)
		s.Settings = a.Clone()
		s.View.Mode = a.EffectiveMode(kind)
		s.View.ClusterID = a.ClusterID
		if _, exists := file.Profiles[target.Binding].Integrations[kind]; exists {
			s.View.State = "saved"
		}
		if err := a.Validate(kind); err != nil {
			s.Err = err
		}
		if a.NeedsTarget() && a.Target != target.Fingerprint {
			s.View.State = "target_changed"
			s.View.PreviousIdentity = a.Identity
			s.Err = errors.New("cluster connection changed; review its saved connection in Settings")
		}
		if details && !file.Imported[kind] && !file.Dismissed[target.Binding][kind] && s.View.State == "auto" {
			legacy, digest := Legacy(kind)
			if hasLegacy(legacy) {
				v := settingsView(legacy.Settings)
				s.View.Legacy = &LegacyOffer{URL: v.URL, HeaderKeys: v.HeaderKeys, SecretSet: v.SecretSet, Revision: p.Revision(digest)}
				if legacy.Err != nil {
					s.View.Legacy.Error = legacy.Err.Error()
				}
			}
		}
	}
	if launch, ok := p.launch[kind]; ok && target.Binding == p.launchTarget.Binding && target.Fingerprint == p.launchTarget.Fingerprint {
		s.Bundle = launch
		s.Settings = launch.Settings.Clone()
		s.View.State = "launch"
		s.View.Mode = launch.Settings.EffectiveMode(kind)
		s.View.ClusterID = launch.Settings.ClusterID
		s.View.Legacy = nil
	}
	s.Settings = settingsWithDefaults(kind, s.Settings)
	v := settingsView(s.Settings)
	s.View.URL = v.URL
	s.View.HeaderKeys = v.HeaderKeys
	s.View.EnvHeaderKeys = v.EnvHeaderKeys
	s.View.HeadersManaged = len(v.EnvHeaderKeys) > 0
	s.View.SecretSet = v.SecretSet
	s.View.InsecureTLS = v.InsecureTLS
	if s.Err == nil && s.Settings.Prometheus != nil {
		headers, err := prom.ResolveHeaders(s.Settings.Prometheus.Headers, s.Settings.Prometheus.HeadersFromEnv)
		if err != nil {
			s.Err = err
		} else {
			s.Settings.Prometheus.Headers = headers
			s.Settings.Prometheus.HeadersFromEnv = nil
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

func settingsWithDefaults(kind config.Integration, s config.IntegrationSettings) config.IntegrationSettings {
	switch kind {
	case config.IntegrationMetrics:
		if s.Prometheus == nil {
			s.Prometheus = &prom.Connection{}
		}
	case config.IntegrationArgoCD:
		if s.ArgoCD == nil {
			s.ArgoCD = &argoapi.Connection{}
		}
	case config.IntegrationCost:
		if s.Kubecost == nil {
			s.Kubecost = &opencost.Connection{}
		}
	}
	return s
}

func (p *Resolver) IsCurrent(selection Selection) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, revision, err := p.read()
	return err == nil && revision == selection.fileRevision
}

func Legacy(kind config.Integration) (Bundle, string) {
	c := config.Load()
	b := Bundle{Settings: settingsWithDefaults(kind, config.IntegrationSettings{})}
	b.legacyArgoBinding, b.legacyCostBinding, b.legacyClusterIDBinding = c.ArgoCDTokenBinding, c.KubecostAPIKeyContext, c.KubecostClusterIDContext
	switch kind {
	case config.IntegrationMetrics:
		b.Settings.Prometheus = &prom.Connection{URL: c.PrometheusURL, Headers: c.PrometheusHeaders, HeadersFromEnv: c.PrometheusHeadersFromEnv}
		b.Err = b.Settings.Prometheus.Validate()
	case config.IntegrationArgoCD:
		b.Settings.ArgoCD = &argoapi.Connection{URL: c.ArgoCDURL, Token: c.ArgoCDToken, InsecureTLS: c.ArgoCDInsecureTLS}
		b.Settings.Target = c.ArgoCDTokenBinding
		b.Err = b.Settings.ArgoCD.Validate()
	case config.IntegrationCost:
		b.Settings.Kubecost = &opencost.Connection{URL: c.KubecostURL, APIKey: c.KubecostAPIKey}
		b.Settings.Mode = c.CostSource
		if b.Settings.Mode == "" {
			b.Settings.Mode = "auto"
		}
		b.Settings.ClusterID = c.KubecostClusterID
		if b.Settings.Mode == "prometheus" {
			b.Settings.Kubecost = &opencost.Connection{}
			b.Settings.ClusterID = ""
		}
		b.Err = b.Settings.Kubecost.Validate()
	}
	data, _ := json.Marshal(struct {
		Settings                                   config.IntegrationSettings
		ArgoBinding, CostBinding, ClusterIDBinding string
	}{b.Settings, c.ArgoCDTokenBinding, c.KubecostAPIKeyContext, c.KubecostClusterIDContext})
	digest := sha256.Sum256(data)
	return b, hex.EncodeToString(digest[:])
}

func hasLegacy(b Bundle) bool {
	c := b.Settings
	return c.URL() != "" || c.Prometheus != nil && len(c.Prometheus.Headers)+len(c.Prometheus.HeadersFromEnv) > 0 || c.ArgoCD != nil && (c.ArgoCD.Token != "" || c.ArgoCD.InsecureTLS) || c.Kubecost != nil && (c.Kubecost.APIKey != "" || b.Settings.ClusterID != "" || b.Settings.Mode != "auto")
}
