package k8s

import (
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

func historyQueryBoundary(start time.Time, rows []timeline.TimelineEvent, limit int) time.Time {
	if len(rows) < limit {
		return start
	}
	oldest := rows[0].Timestamp
	for _, row := range rows {
		if row.Timestamp.Before(oldest) {
			oldest = row.Timestamp
		}
	}
	if oldest.After(start) {
		return oldest
	}
	return start
}
