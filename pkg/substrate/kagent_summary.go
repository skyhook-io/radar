package substrate

// KagentPlacement judges one actor of the prepared template against the
// workers' bookings. Positive means only may fit: runtime overhead and
// snapshot/runtime compatibility can still prevent placement.
type KagentPlacement struct {
	State    string    `json:"state"`
	Reading  string    `json:"reading"`
	Template *Ref      `json:"template,omitempty"`
	Pools    []PoolRef `json:"pools"`
	Complete bool      `json:"complete"`
}

func KagentTemplatePlacement(t Template, workers []Worker, complete bool) KagentPlacement {
	ref := t.Ref()
	out := KagentPlacement{State: "unknown", Reading: "Worker capacity was not fully read.", Template: &ref, Pools: MatchingPools(t, KnownPools(nil, workers)), Complete: complete}
	active, slots := false, false
	for _, w := range workers {
		if w.State != WorkerActive || !SelectorMatches(t.WorkerSelector, w.Labels) {
			continue
		}
		active = true
		slots = slots || HasFreeSlot(w)
		if HasRoomFor(w, t) {
			out.State, out.Reading = "mayFit", "A new session may fit on an active worker; runtime overhead and compatibility can still prevent placement."
			return out
		}
	}
	if !complete {
		return out
	}
	switch {
	case !active:
		out.State, out.Reading = "noActiveWorkers", "No active worker matches the prepared template."
	case !slots:
		out.State, out.Reading = "full", "The matching active workers have no free actor slots."
	default:
		out.State, out.Reading = "noRoom", "Matching active workers have free actor slots, but none has the unbooked CPU and memory this prepared template requires."
	}
	return out
}
