package runtimeevidence

import "encoding/json"

type VaultFacts struct {
	Initialized         bool  `json:"initialized"`
	Sealed              bool  `json:"sealed"`
	Standby             bool  `json:"standby"`
	PerformanceStandby  *bool `json:"performanceStandby,omitempty"`
	HAConnectionHealthy *bool `json:"haConnectionHealthy,omitempty"`
	RemovedFromCluster  *bool `json:"removedFromCluster,omitempty"`
}

func parseVault(body []byte) (*VaultFacts, error) {
	var response struct {
		Initialized         *bool           `json:"initialized"`
		Sealed              *bool           `json:"sealed"`
		Standby             *bool           `json:"standby"`
		PerformanceStandby  *bool           `json:"performance_standby"`
		HAConnectionHealthy json.RawMessage `json:"ha_connection_healthy"`
		RemovedFromCluster  json.RawMessage `json:"removed_from_cluster"`
	}
	if err := decode(body, &response); err != nil {
		return nil, err
	}
	if response.Initialized == nil || response.Sealed == nil || response.Standby == nil {
		return nil, UnexpectedShape
	}
	return &VaultFacts{
		Initialized: *response.Initialized, Sealed: *response.Sealed, Standby: *response.Standby,
		PerformanceStandby:  response.PerformanceStandby,
		HAConnectionHealthy: optionalBool(response.HAConnectionHealthy),
		RemovedFromCluster:  optionalBool(response.RemovedFromCluster),
	}, nil
}

// Malformed connection or removal reports must not discard valid base facts.
func optionalBool(raw json.RawMessage) *bool {
	var value *bool
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}
