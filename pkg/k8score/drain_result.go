package k8score

type DrainCompletionOutcome string

const (
	DrainComplete    DrainCompletionOutcome = "complete"
	DrainIncomplete  DrainCompletionOutcome = "incomplete"
	DrainUnconfirmed DrainCompletionOutcome = "unconfirmed"
	DrainStarted     DrainCompletionOutcome = "started"
)

func (r *DrainResult) CompletionOutcome() DrainCompletionOutcome {
	if len(r.Errors) > 0 || len(r.PendingPods) > 0 {
		return DrainIncomplete
	}
	for _, pod := range r.SkippedPods {
		if pod.BlocksDrain() {
			return DrainIncomplete
		}
	}
	if len(r.UnconfirmedPods) > 0 || r.VerificationError != "" {
		return DrainUnconfirmed
	}
	if len(r.EvictedPods) > 0 && !r.WaitedForDeletion {
		return DrainStarted
	}
	return DrainComplete
}
