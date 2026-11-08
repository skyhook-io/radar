package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// investigationCapacityCode marks a start or turn refused because every
// investigation slot is taken. The UI keys its recovery (the running runs, a
// Stop on each, then retrying the same start) on this code, not the sentence.
const investigationCapacityCode = "investigations_at_capacity"

// writeInvestigationCapacity answers a start or turn refused by ErrAtCapacity.
// limit lets the UI say how many run at once without hardcoding the server's
// cap; the runs holding the slots are the running entries of the runs list.
// context is the cluster the request was refused in: a retry after a cluster
// switch would resolve the same namespace and name against a different one.
func writeInvestigationCapacity(w http.ResponseWriter, limit int, context string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	body := map[string]any{
		"error": fmt.Sprintf("%d investigations are already running, the most Radar runs at once. Stop one or wait for one to finish, then try again.", limit),
		"code":  investigationCapacityCode,
		"limit": limit,
	}
	if context != "" {
		body["context"] = context
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[ai] Failed to encode capacity response: %v", err)
	}
}
