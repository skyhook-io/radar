package runtimeevidence

type VaultFacts struct {
	Initialized        bool  `json:"initialized"`
	Sealed             bool  `json:"sealed"`
	Standby            bool  `json:"standby"`
	PerformanceStandby *bool `json:"performanceStandby,omitempty"`
}

func parseVault(body []byte) (*VaultFacts, error) {
	var response struct {
		Initialized        *bool `json:"initialized"`
		Sealed             *bool `json:"sealed"`
		Standby            *bool `json:"standby"`
		PerformanceStandby *bool `json:"performance_standby"`
	}
	if err := decode(body, &response); err != nil {
		return nil, err
	}
	if response.Initialized == nil || response.Sealed == nil || response.Standby == nil {
		return nil, UnexpectedShape
	}
	return &VaultFacts{Initialized: *response.Initialized, Sealed: *response.Sealed, Standby: *response.Standby, PerformanceStandby: response.PerformanceStandby}, nil
}
