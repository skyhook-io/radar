package insights

// Severity is the urgency tier for an Issue. The four-tier vocabulary
// (critical → alert → warning → info) is the project-wide severity
// scale documented in CLAUDE.md and mirrored on the frontend in
// packages/k8s-ui/src/types/gitops-insights.ts.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityAlert    Severity = "alert"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Scope tags an Issue with which level of the GitOps app it concerns.
// Drives icon + grouping in the issues band.
type Scope string

const (
	ScopeOperation Scope = "operation"
	ScopeResource  Scope = "resource"
	ScopeCondition Scope = "condition"
	ScopeTree      Scope = "tree"
	ScopeLifecycle Scope = "lifecycle"
)

// Category bins a Change row in the Changes view. The vocabulary is closed:
// new values must be added here and given an explicit changeRank entry.
type Category string

const (
	CategorySynced      Category = "Synced"
	CategoryOutOfSync   Category = "OutOfSync"
	CategoryDegraded    Category = "Degraded"
	CategoryMissing     Category = "Missing"
	CategoryPruned      Category = "Pruned"
	CategoryHook        Category = "Hook"
	CategoryProgressing Category = "Progressing"
	CategoryReconciling Category = "Reconciling"
	CategorySuspended   Category = "Suspended"
	CategoryUnknown     Category = "Unknown"
)

// DriftOp is the kind of difference between desired and live spec.
type DriftOp string

const (
	DriftOpAdded   DriftOp = "added"
	DriftOpRemoved DriftOp = "removed"
	DriftOpChanged DriftOp = "changed"
)

// DriftSource identifies how desired state was derived.
type DriftSource string

const (
	// DriftSourceLastApplied: desired parsed from
	// kubectl.kubernetes.io/last-applied-configuration. SSA / Helm-installed
	// resources don't carry this annotation.
	DriftSourceLastApplied DriftSource = "lastAppliedAnnotation"
)

// RemediationKind tags a structured fix suggestion attached to an Issue.
// The frontend switches on this to render the right contextual action
// (button, link, copyable command). Add a new kind only when there's a
// concrete next-step the UI can offer; hint-only diagnoses belong in the
// Issue's Cause/Action strings and don't need this layer.
type RemediationKind string

const (
	// RemediationCreateNamespace: the destination namespace doesn't exist
	// and the Application has CreateNamespace=false. One-click fix is to
	// kubectl-create the namespace named in Remediation.Target.
	RemediationCreateNamespace RemediationKind = "create-namespace"
)
