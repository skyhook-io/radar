// Package capacityapi defines the stable JSON contract for Radar's Capacity API.
// It is data-only so Radar Cloud can consume the same wire model without
// importing Radar's cache, server, or Karpenter implementation packages.
package capacityapi

import "time"

const CurrentSchemaVersion = "v1alpha1"

type IntegrationState string

const (
	IntegrationAvailable   IntegrationState = "available"
	IntegrationNotDetected IntegrationState = "not_detected"
	IntegrationDenied      IntegrationState = "denied"
	IntegrationSyncing     IntegrationState = "syncing"
)

type ClusterContext struct {
	ContextName string `json:"contextName"`
	ClusterName string `json:"clusterName,omitempty"`
}

type ProviderType string

const ProviderKarpenter ProviderType = "karpenter"

type ControllerMode string

const (
	ControllerSelfManaged ControllerMode = "self_managed"
	ControllerEKSAuto     ControllerMode = "eks_auto"
	ControllerUnknown     ControllerMode = "unknown"
)

type ProviderFeatures struct {
	StaticCapacity *bool `json:"staticCapacity,omitempty"`
	Metrics        *bool `json:"metrics,omitempty"`
}

type APIKind struct {
	Group    string   `json:"group"`
	Kind     string   `json:"kind"`
	Resource string   `json:"resource"`
	Versions []string `json:"versions"`
}

type Provider struct {
	Type              ProviderType        `json:"type"`
	ControllerMode    ControllerMode      `json:"controllerMode"`
	APIVersionsByKind map[string][]string `json:"apiVersionsByKind"`
	NodeClassKinds    []APIKind           `json:"nodeClassKinds"`
	Features          ProviderFeatures    `json:"features"`
}

func NewProvider() Provider {
	return Provider{
		Type:              ProviderKarpenter,
		ControllerMode:    ControllerUnknown,
		APIVersionsByKind: map[string][]string{},
		NodeClassKinds:    []APIKind{},
	}
}

type CoverageSource string

const (
	CoverageNodePools             CoverageSource = "nodePools"
	CoverageNodeClaims            CoverageSource = "nodeClaims"
	CoverageNodeClasses           CoverageSource = "nodeClasses"
	CoverageNodes                 CoverageSource = "nodes"
	CoveragePods                  CoverageSource = "pods"
	CoverageWorkloads             CoverageSource = "workloads"
	CoverageKarpenterObjectEvents CoverageSource = "karpenterObjectEvents"
	CoverageNodeMetrics           CoverageSource = "nodeMetrics"
	CoverageTimeline              CoverageSource = "timeline"
	CoverageAutoscalerStatus      CoverageSource = "autoscalerStatus"
)

var CoverageSources = []CoverageSource{
	CoverageNodePools,
	CoverageNodeClaims,
	CoverageNodeClasses,
	CoverageNodes,
	CoveragePods,
	CoverageWorkloads,
	CoverageKarpenterObjectEvents,
	CoverageNodeMetrics,
	CoverageTimeline,
	CoverageAutoscalerStatus,
}

type CoverageStatus string

const (
	CoverageAvailable   CoverageStatus = "available"
	CoveragePartial     CoverageStatus = "partial"
	CoverageDenied      CoverageStatus = "denied"
	CoverageSyncing     CoverageStatus = "syncing"
	CoverageUnavailable CoverageStatus = "unavailable"
	CoverageError       CoverageStatus = "error"
)

type CoverageScope string

const (
	CoverageScopeCluster                 CoverageScope = "cluster"
	CoverageScopeAllAuthorizedNamespaces CoverageScope = "all_authorized_namespaces"
	CoverageScopeExplicitNamespaces      CoverageScope = "explicit_namespaces"
)

type SourceCoverage struct {
	Status           CoverageStatus `json:"status"`
	ReasonCode       string         `json:"reasonCode,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	Scope            CoverageScope  `json:"scope"`
	Namespaces       []string       `json:"namespaces,omitempty"`
	ObservedAt       *time.Time     `json:"observedAt,omitempty"`
	ObservationStart *time.Time     `json:"observationStart,omitempty"`
	ItemCount        *int           `json:"itemCount,omitempty"`
	ImpactFields     []string       `json:"impactFields"`
}

func NewSourceCoverage(status CoverageStatus, scope CoverageScope) SourceCoverage {
	return SourceCoverage{
		Status:       status,
		Scope:        scope,
		ImpactFields: []string{},
	}
}

type CoverageBySource map[CoverageSource]SourceCoverage

type ResponseMeta struct {
	SchemaVersion  string           `json:"schemaVersion"`
	GeneratedAt    time.Time        `json:"generatedAt"`
	ClusterContext ClusterContext   `json:"clusterContext"`
	Provider       Provider         `json:"provider"`
	Coverage       CoverageBySource `json:"coverage"`
}

func NewResponseMeta(generatedAt time.Time) ResponseMeta {
	return ResponseMeta{
		SchemaVersion: CurrentSchemaVersion,
		GeneratedAt:   generatedAt,
		Provider:      NewProvider(),
		Coverage:      CoverageBySource{},
	}
}
