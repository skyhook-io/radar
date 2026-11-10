package summarycontext

import "time"

// HTTP integration tests live in another package and must exercise this deadline.
func SetTestEnrichmentBudget(budget time.Duration) func() {
	previous := enrichmentBudget
	enrichmentBudget = budget
	return func() { enrichmentBudget = previous }
}
