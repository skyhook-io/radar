package kargo

// waitingReasons are the condition reasons Kargo v1.12 writes while work is
// under way, not because something failed: the Stage controller copies them
// onto Ready from Promoting, Healthy and Verified
// (pkg/controller/stages/regular_stages.go summarizeConditions), and the
// Warehouse controller writes them before and during discovery
// (pkg/controller/warehouses).
var waitingReasons = map[string]string{
	"ActivePromotion":           "Promoting",
	"NoFreight":                 "Unknown",
	"WaitingForHealthCheck":     "Progressing",
	"Progressing":               "Progressing",
	"WaitingForVerification":    "Verifying",
	"PendingVerification":       "Verifying",
	"VerificationPending":       "Verifying",
	"VerificationRunning":       "Verifying",
	"TargetAwareStage":          "Unknown",
	"DiscoveryInProgress":       "Discovering",
	"AwaitingFreightCreation":   "Discovering",
	"ScheduledDiscovery":        "Discovering",
	"FreightCreationInProgress": "Discovering",
	"Pending":                   "Unknown",
}

// WaitingReason reports whether a Kargo condition reason means work is under
// way (a promotion, a health check, a verification, discovery, or no Freight
// yet) rather than a failure.
func WaitingReason(reason string) bool {
	_, ok := waitingReasons[reason]
	return ok
}

// cancelledReasons are a person's cancellation copied onto Ready: neither a
// failure nor progress.
var cancelledReasons = map[string]bool{"LastPromotionAborted": true, "VerificationAborted": true}
