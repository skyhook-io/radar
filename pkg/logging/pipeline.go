package logging

// Workload is a collector or aggregator the operator runs, with the owner
// reference that ties it to a Logging or FluentbitAgent.
type Workload struct {
	Kind, Namespace, Name string
	OwnerKind, OwnerName  string
	Ready, Desired        int
}

// Agent is a FluentbitAgent: the collector config for the Logging it names.
type Agent struct{ Name, LoggingRef string }

// InstanceWorkloads finds a Logging's collectors (DaemonSets owned by it, or by
// a FluentbitAgent with its loggingRef) and aggregators (StatefulSets it owns).
func InstanceWorkloads(l Logging, agents []Agent, ws []Workload) (collectors, aggregators []Workload) {
	ours := map[string]bool{}
	for _, a := range agents {
		if a.LoggingRef == l.LoggingRef {
			ours[a.Name] = true
		}
	}
	for _, w := range ws {
		byLogging := w.OwnerKind == "Logging" && w.OwnerName == l.Name
		switch {
		case w.Kind == "DaemonSet" && (byLogging || (w.OwnerKind == "FluentbitAgent" && ours[w.OwnerName])):
			collectors = append(collectors, w)
		case w.Kind == "StatefulSet" && byLogging:
			aggregators = append(aggregators, w)
		}
	}
	return collectors, aggregators
}

// Candidate is a Flow as the operator would treat it: Ignored flows forward
// nothing; OnlyMaybe flows may at most be selecting (their Logging's
// namespace selector, which Radar doesn't evaluate, decides).
type Candidate struct {
	Flow      Flow
	Ignored   bool
	OnlyMaybe bool
}

type RouteCount struct {
	Count      int
	LowerBound bool
}

// CountPods counts, per candidate, the pods it selects for certain. A pod only
// an uncertain decision could select is neither counted nor unrouted; it marks
// that route's count a lower bound and sets uncertain.
func CountPods(cands []Candidate, pods []PodFacts) (counts []RouteCount, unrouted []PodFacts, uncertain bool) {
	counts = make([]RouteCount, len(cands))
	for _, p := range pods {
		routed := false
		for i, c := range cands {
			if c.Ignored || !InScope(c.Flow, p.Namespace) {
				continue
			}
			d := Selects(c.Flow, p)
			switch {
			case d.Selected && !c.OnlyMaybe:
				counts[i].Count++
				routed = true
			case d.Selected || d.Uncertain:
				counts[i].LowerBound = true
				uncertain, routed = true, true
			}
		}
		if !routed {
			unrouted = append(unrouted, p)
		}
	}
	return counts, unrouted, uncertain
}
