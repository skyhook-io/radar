package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

type Integration string

const (
	IntegrationMetrics Integration = "metrics"
	IntegrationArgoCD  Integration = "argocd"
	IntegrationCost    Integration = "cost"
)

var IntegrationKinds = []Integration{IntegrationMetrics, IntegrationArgoCD, IntegrationCost}

type TargetIdentity struct {
	Server      string `json:"server"`
	User        string `json:"user,omitempty"`
	TLSName     string `json:"tlsName,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
	Trust       string `json:"trust,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
}

type IntegrationSettings struct {
	Mode       string               `json:"mode,omitempty"`
	Target     string               `json:"target,omitempty"`
	Identity   *TargetIdentity      `json:"identity,omitempty"`
	Prometheus *prom.Connection     `json:"prometheus,omitempty"`
	ArgoCD     *argoapi.Connection  `json:"argocd,omitempty"`
	Kubecost   *opencost.Connection `json:"kubecost,omitempty"`
	ClusterID  string               `json:"clusterId,omitempty"`
}

func (s IntegrationSettings) URL() string {
	switch {
	case s.Prometheus != nil:
		return s.Prometheus.URL
	case s.ArgoCD != nil:
		return s.ArgoCD.URL
	case s.Kubecost != nil:
		return s.Kubecost.URL
	}
	return ""
}

func (s IntegrationSettings) EffectiveMode(kind Integration) string {
	if kind == IntegrationCost {
		if s.Mode != "" {
			return s.Mode
		}
	} else if s.URL() != "" {
		return "connection"
	}
	return "auto"
}

func (s IntegrationSettings) Clone() IntegrationSettings {
	if s.Prometheus != nil {
		p := *s.Prometheus
		p.Headers = maps.Clone(p.Headers)
		p.HeadersFromEnv = maps.Clone(p.HeadersFromEnv)
		s.Prometheus = &p
	}
	if s.ArgoCD != nil {
		a := *s.ArgoCD
		s.ArgoCD = &a
	}
	if s.Kubecost != nil {
		k := *s.Kubecost
		s.Kubecost = &k
	}
	if s.Identity != nil {
		identity := *s.Identity
		s.Identity = &identity
	}
	return s
}

func (s IntegrationSettings) NeedsTarget() bool {
	return s.URL() != "" || s.ClusterID != "" || s.ArgoCD != nil && s.ArgoCD.Token != "" || s.Kubecost != nil && s.Kubecost.APIKey != ""
}

func (s IntegrationSettings) Validate(kind Integration) error {
	if s.NeedsTarget() && s.Target == "" {
		return errors.New("saved settings require an accepted cluster target")
	}
	switch kind {
	case IntegrationMetrics:
		if s.ArgoCD != nil || s.Kubecost != nil || s.ClusterID != "" || s.Mode != "" {
			return errors.New("metrics settings use only a URL and optional headers")
		}
		if s.Prometheus != nil {
			return s.Prometheus.Validate()
		}
	case IntegrationArgoCD:
		if s.Prometheus != nil || s.Kubecost != nil || s.ClusterID != "" || s.Mode != "" {
			return errors.New("Argo CD settings use only a URL, token and TLS option")
		}
		if s.ArgoCD != nil {
			return s.ArgoCD.Validate()
		}
	case IntegrationCost:
		if s.Prometheus != nil || s.ArgoCD != nil || (s.Mode != "" && s.Mode != "auto" && s.Mode != "prometheus" && s.Mode != "kubecost") {
			return errors.New("cost source must be auto, prometheus, or kubecost")
		}
		if s.Mode == "prometheus" && (s.Kubecost != nil || s.ClusterID != "") {
			return errors.New("Prometheus costs use this context's metrics connection, without Kubecost settings")
		}
		if strings.TrimSpace(s.ClusterID) != s.ClusterID || len(s.ClusterID) > 1024 || strings.ContainsAny(s.ClusterID, "\r\n\x00") {
			return errors.New("invalid Kubecost cluster ID")
		}
		if s.Kubecost != nil {
			return s.Kubecost.Validate()
		}
	default:
		return errors.New("unknown integration type")
	}
	return nil
}

func (p ClusterProfiles) Settings(binding string, kind Integration) IntegrationSettings {
	s, ok := p.Profiles[binding].Integrations[kind]
	if !ok {
		return IntegrationSettings{}
	}
	return s
}

func (p ClusterProfiles) ValidateChanges(before ClusterProfiles) error {
	for binding, profile := range p.Profiles {
		for kind, settings := range profile.Integrations {
			previous, exists := before.Profiles[binding].Integrations[kind]
			if exists {
				// JSON cloning normalizes empty maps to nil; compare persisted values.
				previousJSON, err := json.Marshal(previous)
				if err != nil {
					return err
				}
				settingsJSON, err := json.Marshal(settings)
				if err != nil {
					return err
				}
				if bytes.Equal(previousJSON, settingsJSON) {
					continue
				}
			}
			if err := settings.Validate(kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *ClusterProfiles) RemoveSettings(binding string, kind Integration) error {
	profile, ok := p.Profiles[binding]
	if !ok {
		return errors.New("context settings no longer exist")
	}
	if _, ok := profile.Integrations[kind]; !ok {
		return errors.New("integration settings no longer exist")
	}
	delete(profile.Integrations, kind)
	if len(profile.Integrations) == 0 {
		delete(p.Profiles, binding)
	} else {
		p.Profiles[binding] = profile
	}
	return nil
}

func (p ClusterProfiles) ValidateStructure() error {
	if p.Version != 1 {
		return errors.New("unsupported clusters.json version (expected 1)")
	}
	if p.Profiles == nil {
		return errors.New("clusters.json requires a profiles object")
	}
	for kind := range p.Imported {
		if !slices.Contains(IntegrationKinds, kind) {
			return errors.New("unsupported integration in imported settings")
		}
	}
	for binding, kinds := range p.Dismissed {
		if binding == "" {
			return errors.New("dismissed settings require a context binding")
		}
		for kind := range kinds {
			if !slices.Contains(IntegrationKinds, kind) {
				return errors.New("unsupported integration in dismissed settings")
			}
		}
	}
	for binding, profile := range p.Profiles {
		if binding == "" || profile.Context == "" || profile.Integrations == nil {
			return errors.New("cluster profile requires binding, context and integrations")
		}
		for kind := range profile.Integrations {
			if !slices.Contains(IntegrationKinds, kind) {
				return errors.New("unsupported integration type in cluster settings")
			}
		}
	}
	return nil
}
