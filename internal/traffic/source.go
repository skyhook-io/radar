// Package traffic provides traffic source detection and flow aggregation.
// Core types and aggregation logic live in pkg/traffic for reuse by Skyhook.
// This package re-exports those types and adds Radar-specific manager/lifecycle logic.
package traffic

import (
	pkgtraffic "github.com/skyhook-io/radar/pkg/traffic"
)

// Re-export types from pkg/traffic so existing consumers don't need dual imports.
type TrafficSource = pkgtraffic.TrafficSource
type DetectionResult = pkgtraffic.DetectionResult
type FlowOptions = pkgtraffic.FlowOptions
type Flow = pkgtraffic.Flow
type Endpoint = pkgtraffic.Endpoint
type PolicyVerdict = pkgtraffic.PolicyVerdict
type PolicyRef = pkgtraffic.PolicyRef

const (
	EndpointKindPod      = pkgtraffic.EndpointKindPod
	EndpointKindExternal = pkgtraffic.EndpointKindExternal
	EndpointKindHost     = pkgtraffic.EndpointKindHost
	EndpointKindUnknown  = pkgtraffic.EndpointKindUnknown
)

type FlowsResponse = pkgtraffic.FlowsResponse
type AggregatedFlow = pkgtraffic.AggregatedFlow
type HTTPPathStat = pkgtraffic.HTTPPathStat
type DNSQueryStat = pkgtraffic.DNSQueryStat
type ClusterInfo = pkgtraffic.ClusterInfo
type SourceStatus = pkgtraffic.SourceStatus
type Recommendation = pkgtraffic.Recommendation
type HelmChartInfo = pkgtraffic.HelmChartInfo
type SourcesResponse = pkgtraffic.SourcesResponse

// Re-export FlowsResponse.WarningKind values from pkg/traffic.
const (
	WarningTransient = pkgtraffic.WarningTransient
	WarningPartial   = pkgtraffic.WarningPartial
)

// Re-export functions from pkg/traffic.
var AggregateFlows = pkgtraffic.AggregateFlows
var RoundRate = pkgtraffic.RoundRate
var DefaultFlowOptions = pkgtraffic.DefaultFlowOptions
