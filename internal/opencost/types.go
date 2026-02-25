package opencost

// CostSummary is the response for the /api/opencost/summary endpoint.
type CostSummary struct {
	Available       bool            `json:"available"`
	Currency        string          `json:"currency,omitempty"`
	Window          string          `json:"window,omitempty"`
	TotalHourlyCost float64         `json:"totalHourlyCost,omitempty"`
	Namespaces      []NamespaceCost `json:"namespaces,omitempty"`
}

// NamespaceCost holds per-namespace cost breakdown.
type NamespaceCost struct {
	Name       string  `json:"name"`
	HourlyCost float64 `json:"hourlyCost"`
	CPUCost    float64 `json:"cpuCost"`
	MemoryCost float64 `json:"memoryCost"`
}
