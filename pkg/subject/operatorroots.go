package subject

// operatorCRKind identifies an operator CR that owns a generated workload and
// is itself a first-class Subject root ONE LEVEL ABOVE that workload. The
// {Group,Kind} pair is the allowlist entry — matched against a workload's
// immediate owner.
type operatorCRKind struct {
	Group string
	Kind  string
}

// defaultOperatorCRKinds is the curated V1 allowlist — start curated, expand
// later. An operator CR not on this list degrades to the generated workload
// (raw-always), never hidden.
var defaultOperatorCRKinds = map[operatorCRKind]bool{
	{Group: "postgresql.cnpg.io", Kind: "Cluster"}: true, // CloudNativePG
	{Group: "kafka.strimzi.io", Kind: "Kafka"}:     true, // Strimzi
	// Crossplane composite resources (XRs) are matched structurally below
	// because their group/kind are unbounded (one CRD per composition).
}

// crossplaneXRGroups is an exact-match allowlist of API groups whose resources
// are Crossplane composite resources (XRs). Author-defined XR groups (e.g.
// "platform.acme.io") are unbounded and can't be enumerated, so this matches
// the apiextensions.crossplane.io group that composed-resource ownerRefs are
// typed with. Kept narrow on purpose — a false positive over-collapses.
var crossplaneXRGroups = map[string]bool{
	"apiextensions.crossplane.io": true,
}

// OwnerLookup returns the immediate CONTROLLER owner of a resource (one hop),
// used by DefaultOperatorRoots to inspect a workload's parent without pkg/subject
// importing topology. Same contract as OwnerResolver: controller ownership only
// (the generated workload's controllerRef points at the operator CR) — NOT
// declarative management edges. A topology adapter must resolve from
// controllerReferences, not from a raw EdgeManages walk.
type OwnerLookup interface {
	ImmediateOwner(child Ref) (parent Ref, ok bool)
}

// DefaultOperatorRoots is the V1 OperatorRootHook: it consults a curated
// allowlist of operator CR kinds. Given the resolved workload, it looks up the
// workload's immediate owner and, if that owner is a recognized operator CR,
// returns it as the Subject root. Unknown owners return false (degrade to the
// workload — raw-always).
type DefaultOperatorRoots struct {
	Owners OwnerLookup
}

func (d DefaultOperatorRoots) RootFor(workload Ref) (Ref, bool) {
	if d.Owners == nil {
		return Ref{}, false
	}
	owner, ok := d.Owners.ImmediateOwner(workload)
	if !ok {
		return Ref{}, false
	}
	if isOperatorCR(owner) {
		return owner, true
	}
	return Ref{}, false
}

func isOperatorCR(ref Ref) bool {
	if defaultOperatorCRKinds[operatorCRKind{Group: ref.Group, Kind: ref.Kind}] {
		return true
	}
	if crossplaneXRGroups[ref.Group] {
		return true
	}
	return false
}
