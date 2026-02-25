package opencost

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// RegisterRoutes registers OpenCost routes on the given router.
func RegisterRoutes(r chi.Router) {
	r.Get("/opencost/summary", handleSummary)
}

// handleSummary returns namespace-level cost summary from OpenCost Prometheus metrics.
func handleSummary(w http.ResponseWriter, r *http.Request) {
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, CostSummary{Available: false})
		return
	}

	// Check if Prometheus is reachable (triggers discovery if needed)
	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, CostSummary{Available: false})
		return
	}

	// Query per-namespace CPU cost
	cpuResult, err := client.Query(r.Context(),
		`sum by (namespace) (rate(container_cpu_allocation{namespace!=""}[1h]) * on(node) group_left() node_cpu_hourly_cost)`)
	if err != nil {
		// Try the opencost_container metric name variant
		cpuResult, err = client.Query(r.Context(),
			`sum by (namespace) (rate(opencost_container_cpu_cost_total[1h]))`)
		if err != nil {
			log.Printf("[opencost] CPU cost query failed: %v", err)
			writeJSON(w, http.StatusOK, CostSummary{Available: false})
			return
		}
	}

	// Query per-namespace memory cost
	memResult, err := client.Query(r.Context(),
		`sum by (namespace) (rate(container_memory_allocation_bytes{namespace!=""}[1h]) / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if err != nil {
		// Try the opencost_container metric name variant
		memResult, err = client.Query(r.Context(),
			`sum by (namespace) (rate(opencost_container_memory_cost_total[1h]))`)
		if err != nil {
			log.Printf("[opencost] Memory cost query failed: %v", err)
			writeJSON(w, http.StatusOK, CostSummary{Available: false})
			return
		}
	}

	// If both queries returned empty results, OpenCost metrics aren't available
	if len(cpuResult.Series) == 0 && len(memResult.Series) == 0 {
		writeJSON(w, http.StatusOK, CostSummary{Available: false})
		return
	}

	// Build per-namespace cost map
	nsMap := make(map[string]*NamespaceCost)

	for _, s := range cpuResult.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		if _, ok := nsMap[ns]; !ok {
			nsMap[ns] = &NamespaceCost{Name: ns}
		}
		if len(s.DataPoints) > 0 {
			nsMap[ns].CPUCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	for _, s := range memResult.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		if _, ok := nsMap[ns]; !ok {
			nsMap[ns] = &NamespaceCost{Name: ns}
		}
		if len(s.DataPoints) > 0 {
			nsMap[ns].MemoryCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	// Calculate totals
	var totalHourlyCost float64
	namespaces := make([]NamespaceCost, 0, len(nsMap))
	for _, nc := range nsMap {
		nc.HourlyCost = nc.CPUCost + nc.MemoryCost
		totalHourlyCost += nc.HourlyCost
		namespaces = append(namespaces, *nc)
	}

	// Also try to get node-level total cost for a more accurate total
	nodeResult, err := client.Query(r.Context(), `sum(node_total_hourly_cost)`)
	if err == nil && len(nodeResult.Series) > 0 && len(nodeResult.Series[0].DataPoints) > 0 {
		nodeCost := nodeResult.Series[0].DataPoints[0].Value
		if nodeCost > totalHourlyCost {
			totalHourlyCost = nodeCost
		}
	}

	// Sort by cost descending
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].HourlyCost > namespaces[j].HourlyCost
	})

	// Round to 4 decimal places for cleaner JSON
	totalHourlyCost = roundTo(totalHourlyCost, 4)
	for i := range namespaces {
		namespaces[i].HourlyCost = roundTo(namespaces[i].HourlyCost, 4)
		namespaces[i].CPUCost = roundTo(namespaces[i].CPUCost, 4)
		namespaces[i].MemoryCost = roundTo(namespaces[i].MemoryCost, 4)
	}

	writeJSON(w, http.StatusOK, CostSummary{
		Available:       true,
		Currency:        "USD",
		Window:          "1h",
		TotalHourlyCost: totalHourlyCost,
		Namespaces:      namespaces,
	})
}

func roundTo(val float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[opencost] Failed to encode JSON response: %v", err)
	}
}
