package ownership

// Domain is where a search for dependents looks. Nothing is implied: the zero
// Domain searches nothing.
type Domain struct {
	// AllNamespaces searches every namespace of the namespaced kinds;
	// otherwise Namespaces lists the ones to search. Only AllNamespaces means
	// every namespace: an empty entry in Namespaces is not one.
	AllNamespaces bool
	Namespaces    []string
	// ClusterScoped also searches the cluster-scoped kinds.
	ClusterScoped bool
}

// Everywhere is where the garbage collector looks for a UID's dependents:
// every namespace and the cluster-scoped kinds.
func Everywhere() Domain {
	return Domain{AllNamespaces: true, ClusterScoped: true}
}

// InNamespace searches one namespace, where a namespaced owner's dependents
// can validly be.
func InNamespace(namespace string) Domain {
	return Domain{Namespaces: []string{namespace}}
}

// Searches reports whether d searches kinds of this scope.
func (d Domain) Searches(namespaced bool) bool {
	if namespaced {
		return d.AllNamespaces || len(d.Namespaces) > 0
	}
	return d.ClusterScoped
}

// CandidateRead is what a search for one UID's dependents found.
type CandidateRead struct {
	// Candidates are the objects in the domain with an owner reference naming
	// the UID, whatever the reference's other fields say, ordered by group,
	// kind, namespace and name.
	Candidates []Observation
	// Gaps are the parts of the domain the search could not read. A Gap with
	// no Kind stands for kinds discovery didn't list (read_error), or for an
	// empty namespace the domain named (namespace_required), which is never
	// taken to mean all namespaces. One with no Namespace on a namespaced kind
	// stands for namespaces of that kind Radar doesn't collect.
	Gaps []Gap
	// Truncated: the search stopped at its limit, so more may exist.
	Truncated bool
}

// Complete reports whether Candidates are all of the UID's dependents in the
// domain.
func (r CandidateRead) Complete() bool {
	return len(r.Gaps) == 0 && !r.Truncated
}
