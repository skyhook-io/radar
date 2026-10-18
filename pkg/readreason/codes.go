// Package readreason names why Radar could not give a complete answer to a
// resource read or a search, and what resolves each reason. The values are
// wire contracts: resource-read errors carry one as error_code (REST bodies,
// MCP structuredContent and text), and coverage gaps as gaps[].code. Clients
// branch on them, so a spelling changes only deliberately, and a newer UI
// maps a renamed code it can still receive from an older Radar.
//
// A code names its subject and condition. Where whose limit it is changes the
// remedy, the prefix names that identity: user_ for the access of the user
// asking (a person, or an agent acting as one), radar_ for Radar's own
// Kubernetes identity and what it collects. kind_
// codes are a kind's loading lifecycle; namespace_ and outside_ codes are how
// the request targeted namespaces; cluster_ and connection_ are Radar's
// cluster connection; the rest describe the request or the answer itself.
// Each code's Action says what resolves it.
package readreason

import "slices"

// Code is one reason a read or search could not give a complete answer.
type Code string

const (
	// UserForbidden: the user's own RBAC denies the exact operation requested.
	UserForbidden Code = "user_forbidden"
	// UserNoNamespaceAccess: the user's access reaches no namespace in the
	// request's scope (Radar's membership check: the user can list pods or
	// deployments there). A namespace the user named and can't see is
	// UserNamespaceNotVisible.
	UserNoNamespaceAccess Code = "user_no_namespace_access"
	// UserPermissionCheckFailed: Radar could not complete the user's permission
	// check. It is not a denial, and a retry can succeed.
	UserPermissionCheckFailed Code = "user_permission_check_failed"

	// RadarForbidden: Kubernetes refused Radar's own identity (its service
	// account or kubeconfig user). Granting the user access does not help.
	RadarForbidden Code = "radar_forbidden"
	// RadarCredentialsRejected: Kubernetes rejected Radar's own
	// credentials (HTTP 401).
	RadarCredentialsRejected Code = "radar_credentials_rejected"
	// RadarNamespaceNotCollected: the target reaches namespaces Radar
	// does not collect.
	RadarNamespaceNotCollected Code = "radar_namespace_not_collected"

	// KindSyncPending: the kind is still loading.
	KindSyncPending Code = "kind_sync_pending"
	// KindSyncTimedOut: the kind's initial load did not finish by its deadline.
	// Radar keeps trying and it may still finish, but repeating the request
	// does not restart it; if it persists, Radar's connection or
	// list/watch permissions need attention.
	KindSyncTimedOut Code = "kind_sync_timed_out"
	// KindStale: the kind had loaded, but its lists have kept failing for
	// minutes, so Radar's copy stopped updating and Radar doesn't use it.
	// Radar keeps retrying; if it persists, Radar's cluster connection or the
	// API server (for a custom resource, often a conversion webhook) needs
	// attention. When Kubernetes refuses or rejects Radar's own identity
	// instead, the code is RadarForbidden or RadarCredentialsRejected.
	KindStale Code = "kind_stale"
	// RadarKindNotCollected: Radar's setup leaves the kind out, so it can't say
	// which exist. Whoever runs Radar can include it, if Radar's identity may
	// list and watch it.
	RadarKindNotCollected Code = "radar_kind_not_collected"
	// KindUnknown: neither the built-in catalog nor discovery resolves the kind.
	KindUnknown Code = "kind_unknown"
	// KindNotWatchable: the API cannot be list/watched, so Radar cannot collect
	// it; warming a watch will not help.
	KindNotWatchable Code = "kind_not_watchable"

	// OutsideNamespacePin: a namespace the request names is outside Radar's
	// --namespace-scope pin, which whoever runs Radar sets.
	OutsideNamespacePin Code = "outside_namespace_pin"
	// OutsideNamespaceSelection: the target is outside the user's saved
	// namespace selection.
	OutsideNamespaceSelection Code = "outside_namespace_selection"
	// UserNamespaceNotVisible: the namespace is not among those the user can
	// see, or does not exist; the code does not say which.
	UserNamespaceNotVisible Code = "user_namespace_not_visible"
	// NamespaceNotFound: the namespace does not exist. Only a user who can
	// see every namespace is told this.
	NamespaceNotFound Code = "namespace_not_found"
	// NamespaceRequired: a get of a namespaced kind named no namespace.
	NamespaceRequired Code = "namespace_required"

	// ObjectNotFound: the object is absent from a scope Radar read in full.
	ObjectNotFound Code = "object_not_found"
	// ListTooLarge: the list is over the request's size budget.
	ListTooLarge Code = "list_too_large"
	// FilterEvalFailed: a search filter could not be evaluated on objects
	// Radar did read, so whether they match is unknown.
	FilterEvalFailed Code = "filter_eval_failed"

	// ClusterConnecting: Radar is still connecting to the cluster.
	ClusterConnecting Code = "cluster_connecting"
	// ClusterDisconnected: Radar has no working cluster connection; the
	// explanation carries the cause.
	ClusterDisconnected Code = "cluster_disconnected"
	// ConnectionChanged: Radar's cluster connection or cache was replaced
	// during the read (a context switch or a reconnect), so the read cannot be
	// trusted and may now reach a different cluster.
	ConnectionChanged Code = "connection_changed"

	// ReadError: an unclassified read or discovery failure; the explanation
	// carries the cause.
	ReadError Code = "read_error"
)

// Action is what resolves a code. It is a property of the code, not of one
// response: clients choose their own retry budgets and delays.
type Action string

const (
	// ActionRetry: the same request may succeed later; nothing has to change.
	ActionRetry Action = "retry"
	// ActionWait: retry later; if it persists, Radar needs
	// attention.
	ActionWait Action = "wait"
	// ActionCheckTarget: confirm which cluster Radar is connected to before
	// repeating the request.
	ActionCheckTarget Action = "check_target"
	// ActionChangeRequest: the request itself has to change.
	ActionChangeRequest Action = "change_request"
	// ActionUserAccess: the user's own Kubernetes access has to change.
	ActionUserAccess Action = "user_access"
	// ActionRadarConfig: Radar's identity, credentials or collection
	// configuration has to change.
	ActionRadarConfig Action = "radar_config"
	// ActionNone: the code is an answer, not a failure.
	ActionNone Action = "none"
)

var actions = map[Code]Action{
	UserForbidden:              ActionUserAccess,
	UserNoNamespaceAccess:      ActionUserAccess,
	UserPermissionCheckFailed:  ActionRetry,
	RadarForbidden:             ActionRadarConfig,
	RadarCredentialsRejected:   ActionRadarConfig,
	RadarNamespaceNotCollected: ActionRadarConfig,
	KindSyncPending:            ActionRetry,
	KindSyncTimedOut:           ActionWait,
	KindStale:                  ActionWait,
	RadarKindNotCollected:      ActionRadarConfig,
	KindUnknown:                ActionChangeRequest,
	KindNotWatchable:           ActionChangeRequest,
	OutsideNamespacePin:        ActionRadarConfig,
	OutsideNamespaceSelection:  ActionChangeRequest,
	UserNamespaceNotVisible:    ActionChangeRequest,
	NamespaceNotFound:          ActionChangeRequest,
	NamespaceRequired:          ActionChangeRequest,
	ObjectNotFound:             ActionNone,
	ListTooLarge:               ActionChangeRequest,
	FilterEvalFailed:           ActionChangeRequest,
	ClusterConnecting:          ActionRetry,
	ClusterDisconnected:        ActionRadarConfig,
	ConnectionChanged:          ActionCheckTarget,
	ReadError:                  ActionRetry,
}

// ActionFor reports what resolves c, and false for an undeclared code.
func ActionFor(c Code) (Action, bool) {
	a, ok := actions[c]
	return a, ok
}

// Known reports whether c is a declared code.
func Known(c Code) bool {
	_, ok := actions[c]
	return ok
}

// Codes returns every declared code, sorted.
func Codes() []Code {
	out := make([]Code, 0, len(actions))
	for c := range actions {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// AffectsWholeCluster reports a code that blocks every read from this Radar,
// not just one kind or namespace — the only codes that should make a fleet
// client treat the cluster itself as failing. RadarCredentialsRejected is
// not one: a single read can be rejected while others succeed, as when a
// token rotates mid-read. Once Radar confirms its credentials are rejected,
// its connection is down and reads report ClusterDisconnected.
func AffectsWholeCluster(c Code) bool {
	return c == ClusterConnecting || c == ClusterDisconnected
}

// precedence orders codes for an error that has to name one, by what the
// user should do first: fix a request no retry can answer; restore the
// connection, without which nothing is read; wait for what resolves by
// itself, since a retry then returns at least part of the answer; act on
// their own access and namespace targeting; then Radar's identity and setup,
// a load that is overdue, and what Radar doesn't collect. A search filter's
// failure and the unclassified read_error come last among failures, so an
// error names a specific cause when it has one. object_not_found is an
// answer, not a failure.
var precedence = []Code{
	NamespaceRequired, KindUnknown, KindNotWatchable, ListTooLarge,
	ClusterDisconnected, ClusterConnecting, ConnectionChanged,
	KindSyncPending, UserPermissionCheckFailed,
	UserForbidden, UserNoNamespaceAccess, UserNamespaceNotVisible, NamespaceNotFound,
	OutsideNamespaceSelection, OutsideNamespacePin,
	RadarCredentialsRejected, RadarForbidden, RadarKindNotCollected,
	KindSyncTimedOut, KindStale, RadarNamespaceNotCollected, FilterEvalFailed, ReadError,
	ObjectNotFound,
}

// Primary returns the code an error reports when several codes apply, such
// as a read whose every namespace failed for a different reason. A REST
// error's coverage keeps the rest; an MCP error names them in its sentence.
// It returns "" for no codes.
func Primary(codes []Code) Code {
	best, rank := Code(""), len(precedence)+1
	for _, c := range codes {
		r := slices.Index(precedence, c)
		if r < 0 {
			r = len(precedence)
		}
		if r < rank {
			best, rank = c, r
		}
	}
	return best
}
