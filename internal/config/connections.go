package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode"

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

type SavedConnection struct {
	Type       Integration          `json:"type"`
	Name       string               `json:"name,omitempty"`
	Prometheus *prom.Connection     `json:"prometheus,omitempty"`
	ArgoCD     *argoapi.Connection  `json:"argocd,omitempty"`
	Kubecost   *opencost.Connection `json:"kubecost,omitempty"`
}

func (c SavedConnection) URL() string {
	switch c.Type {
	case IntegrationMetrics:
		if c.Prometheus != nil {
			return c.Prometheus.URL
		}
	case IntegrationArgoCD:
		if c.ArgoCD != nil {
			return c.ArgoCD.URL
		}
	case IntegrationCost:
		if c.Kubecost != nil {
			return c.Kubecost.URL
		}
	}
	return ""
}

func (c SavedConnection) Validate() error {
	if strings.TrimSpace(c.Name) != c.Name || len(c.Name) > 160 || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 {
		return errors.New("connection name must be at most 160 bytes with no surrounding whitespace or control characters")
	}
	count := 0
	if c.Prometheus != nil {
		count++
	}
	if c.ArgoCD != nil {
		count++
	}
	if c.Kubecost != nil {
		count++
	}
	if count != 1 || c.URL() == "" {
		return errors.New("saved connection requires one matching configuration with an explicit URL")
	}
	switch c.Type {
	case IntegrationMetrics:
		return c.Prometheus.Validate()
	case IntegrationArgoCD:
		return c.ArgoCD.Validate()
	case IntegrationCost:
		return c.Kubecost.Validate()
	default:
		return errors.New("unknown integration type")
	}
}

func (c SavedConnection) Clone() SavedConnection {
	if c.Prometheus != nil {
		p := *c.Prometheus
		p.Headers = maps.Clone(p.Headers)
		p.HeadersFromEnv = maps.Clone(p.HeadersFromEnv)
		c.Prometheus = &p
	}
	if c.ArgoCD != nil {
		a := *c.ArgoCD
		c.ArgoCD = &a
	}
	if c.Kubecost != nil {
		k := *c.Kubecost
		c.Kubecost = &k
	}
	return c
}

func (c SavedConnection) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	label := map[Integration]string{IntegrationMetrics: "Metrics", IntegrationArgoCD: "Argo CD", IntegrationCost: "Kubecost"}[c.Type]
	if label == "" {
		label = "Connection"
	}
	u, err := url.Parse(c.URL())
	if err != nil || u.Host == "" {
		return label
	}
	return label + " · " + u.Host
}

type TargetIdentity struct {
	Server      string `json:"server"`
	User        string `json:"user,omitempty"`
	TLSName     string `json:"tlsName,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
	Trust       string `json:"trust,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
}

type IntegrationAssignment struct {
	Mode         string               `json:"mode"`
	ConnectionID string               `json:"connectionId,omitempty"`
	Target       string               `json:"target,omitempty"`
	Identity     *TargetIdentity      `json:"identity,omitempty"`
	ArgoCD       *argoapi.Connection  `json:"argocd,omitempty"`
	Kubecost     *opencost.Connection `json:"kubecost,omitempty"`
	ClusterID    string               `json:"clusterId,omitempty"`
}

func (a IntegrationAssignment) NeedsTarget() bool {
	return a.ConnectionID != "" || a.ClusterID != "" || a.ArgoCD != nil && a.ArgoCD.Token != "" || a.Kubecost != nil && a.Kubecost.APIKey != ""
}

func (a IntegrationAssignment) Validate(kind Integration, connections map[string]SavedConnection) error {
	if a.NeedsTarget() && a.Target == "" {
		return errors.New("connection assignment requires an accepted cluster target")
	}
	if a.ConnectionID != "" {
		c, ok := connections[a.ConnectionID]
		if !ok || c.Type != kind {
			return errors.New("saved connection is missing or has the wrong integration type")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if a.ArgoCD != nil || a.Kubecost != nil {
			return errors.New("shared connections cannot include discovery credentials")
		}
	}
	switch kind {
	case IntegrationMetrics:
		if a.ArgoCD != nil || a.Kubecost != nil || a.ClusterID != "" || (a.Mode != "auto" && a.Mode != "connection") || (a.Mode == "connection") != (a.ConnectionID != "") {
			return errors.New("metrics must use auto-discovery or a saved metrics connection")
		}
	case IntegrationArgoCD:
		if a.Kubecost != nil || a.ClusterID != "" || (a.Mode != "auto" && a.Mode != "connection") || (a.Mode == "connection") != (a.ConnectionID != "") {
			return errors.New("Argo CD must use local discovery or a saved Argo CD connection")
		}
		if a.ArgoCD != nil {
			if a.ArgoCD.URL != "" {
				return errors.New("discovery credentials must not contain a URL")
			}
			return a.ArgoCD.Validate()
		}
	case IntegrationCost:
		if a.ArgoCD != nil || (a.Mode != "auto" && a.Mode != "prometheus" && a.Mode != "kubecost") {
			return errors.New("cost source must be auto, prometheus, or kubecost")
		}
		if a.Mode == "prometheus" && (a.ConnectionID != "" || a.Kubecost != nil || a.ClusterID != "") {
			return errors.New("Prometheus costs use this context's metrics connection, without Kubecost settings")
		}
		if strings.TrimSpace(a.ClusterID) != a.ClusterID || len(a.ClusterID) > 1024 || strings.ContainsAny(a.ClusterID, "\r\n\x00") {
			return errors.New("invalid Kubecost cluster ID")
		}
		if a.Kubecost != nil {
			if a.Kubecost.URL != "" {
				return errors.New("discovery credentials must not contain a URL")
			}
			return a.Kubecost.Validate()
		}
	default:
		return errors.New("unknown integration type")
	}
	return nil
}

type ConnectionUse struct {
	Binding     string      `json:"binding"`
	Integration Integration `json:"integration"`
	Context     string      `json:"context"`
	Source      string      `json:"source,omitempty"`
	InFileName  string      `json:"inFileName,omitempty"`
}

func (p ClusterProfiles) Uses(id string) []ConnectionUse {
	uses := []ConnectionUse{}
	for binding, profile := range p.Profiles {
		for kind, assignment := range profile.Integrations {
			if assignment.ConnectionID == id {
				uses = append(uses, ConnectionUse{binding, kind, profile.Context, profile.Source, profile.InFileName})
			}
		}
	}
	slices.SortFunc(uses, func(a, b ConnectionUse) int {
		if n := strings.Compare(a.Context, b.Context); n != 0 {
			return n
		}
		return strings.Compare(a.Binding+string(a.Integration), b.Binding+string(b.Integration))
	})
	return uses
}

func (p ClusterProfiles) Assignment(binding string, kind Integration) IntegrationAssignment {
	a, ok := p.Profiles[binding].Integrations[kind]
	if !ok {
		return IntegrationAssignment{Mode: "auto"}
	}
	return a
}

func (p ClusterProfiles) ValidateChanges(before ClusterProfiles) error {
	for id, connection := range p.Connections {
		if previous, ok := before.Connections[id]; !ok || !reflect.DeepEqual(previous, connection) {
			if ok && previous.Type != connection.Type {
				return errors.New("a saved connection's integration type cannot be changed")
			}
			if err := connection.Validate(); err != nil {
				return err
			}
		}
	}
	for binding, profile := range p.Profiles {
		for kind, assignment := range profile.Integrations {
			previous, exists := before.Profiles[binding].Integrations[kind]
			if exists && reflect.DeepEqual(previous, assignment) {
				continue
			}
			if err := assignment.Validate(kind, p.Connections); err != nil {
				return err
			}
		}
	}
	for id := range before.Connections {
		if _, ok := p.Connections[id]; !ok && len(p.Uses(id)) > 0 {
			return errors.New("cannot delete a connection that is still assigned")
		}
	}
	return nil
}

func (p *ClusterProfiles) RemoveAssignment(binding string, kind Integration) error {
	profile, ok := p.Profiles[binding]
	if !ok {
		return errors.New("context settings no longer exist")
	}
	a, ok := profile.Integrations[kind]
	if !ok {
		return errors.New("connection assignment no longer exists")
	}
	delete(profile.Integrations, kind)
	if len(profile.Integrations) == 0 {
		delete(p.Profiles, binding)
	} else {
		p.Profiles[binding] = profile
	}
	if a.ConnectionID != "" && len(p.Uses(a.ConnectionID)) == 0 {
		delete(p.Connections, a.ConnectionID)
	}
	return nil
}

func (p ClusterProfiles) ValidateStructure() error {
	if p.Version != 1 {
		return errors.New("unsupported clusters.json version (expected 1)")
	}
	if p.Profiles == nil || p.Connections == nil {
		return errors.New("clusters.json requires profiles and connections objects")
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
	for id := range p.Connections {
		if id == "" {
			return errors.New("connection ID must not be empty")
		}
	}
	for binding, profile := range p.Profiles {
		if binding == "" || profile.Context == "" || profile.Integrations == nil {
			return errors.New("cluster profile requires binding, context and integrations")
		}
		for kind := range profile.Integrations {
			if !slices.Contains(IntegrationKinds, kind) {
				return fmt.Errorf("unsupported integration type in cluster settings")
			}
		}
	}
	return nil
}
