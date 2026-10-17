// Package readreason names why Radar could not give a complete answer to a
// resource read or a search, and what resolves each reason. The values are
// wire contracts: resource-read errors carry one as error_code (REST bodies,
// MCP structuredContent and text), and coverage gaps as gaps[].code. Clients
// branch on them, so a value never changes its spelling once shipped.
//
// A prefix says whose problem it is. collector_: Radar's own Kubernetes
// identity. kind_: the kind's collection state. Namespace codes: how the
// request's namespaces were targeted. cluster_ and connection_: Radar's
// cluster connection. rbac_denied, no_namespace_access and
// permission_check_failed are the caller's own access; the other unprefixed
// codes describe the request or the answer itself.
package readreason

import "slices"

// Code is one reason a read or search could not give a complete answer.
type Code string

const (
	// RBACDenied: the caller's own RBAC denies the exact operation requested.
	RBACDenied Code = "rbac_denied"
	// NoNamespaceAccess: the caller's access reaches no namespace in the
	// request's scope (Radar's membership check: the caller can list pods or
	// deployments there). A namespace the caller named and can't see is
	// NamespaceNotVisible.
	NoNamespaceAccess Code = "no_namespace_access"
	// PermissionCheckFailed: Radar could not complete the caller's permission
	// check. It is not a denial, and a retry can succeed.
	PermissionCheckFailed Code = "permission_check_failed"

	// CollectorForbidden: Kubernetes refused Radar's own identity (its service
	// account or kubeconfig user). Granting the caller access does not help.
	CollectorForbidden Code = "collector_forbidden"
	// CollectorCredentialsRejected: Kubernetes rejected Radar's own
	// credentials (HTTP 401).
	CollectorCredentialsRejected Code = "collector_credentials_rejected"
	// CollectorScopeLimited: the target reaches namespaces Radar's collector
	// does not collect.
	CollectorScopeLimited Code = "collector_scope_limited"

	// KindSyncPending: the kind is still loading.
	KindSyncPending Code = "kind_sync_pending"
	// KindSyncFailed: the kind's initial load did not finish by its deadline.
	// Radar keeps trying and it may still finish, but repeating the request
	// does not restart it; if it persists, Radar's collector connection or
	// list/watch permissions need attention.
	KindSyncFailed Code = "kind_sync_failed"
	// KindWatchFailed: the kind's watch failed to start. A request that names
	// the kind starts it again.
	KindWatchFailed Code = "kind_watch_failed"
	// KindNotWatched: Radar has no watch for the kind, and nothing more precise
	// is known.
	KindNotWatched Code = "kind_not_watched"
	// KindUnknown: neither the built-in catalog nor discovery resolves the kind.
	KindUnknown Code = "kind_unknown"
	// KindNotWatchable: the API cannot be list/watched, so Radar cannot collect
	// it; warming a watch will not help.
	KindNotWatchable Code = "kind_not_watchable"

	// OutsideNamespaceScope: the target is outside Radar's --namespace-scope pin.
	OutsideNamespaceScope Code = "outside_namespace_scope"
	// OutsideNamespaceSelection: the target is outside the caller's saved
	// namespace selection.
	OutsideNamespaceSelection Code = "outside_namespace_selection"
	// NamespaceNotVisible: the namespace is not among those the caller can
	// see, or does not exist; the code does not say which.
	NamespaceNotVisible Code = "namespace_not_visible"
	// NamespaceNotFound: the namespace does not exist. Only a caller who can
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
	// ActionWait: retry later; if it persists, Radar's collector needs
	// attention.
	ActionWait Action = "wait"
	// ActionRequestKind: a request that names the kind starts its watch again.
	// list and get name the kind, so for them this is a retry.
	ActionRequestKind Action = "request_kind"
	// ActionCheckTarget: confirm which cluster Radar is connected to before
	// repeating the request.
	ActionCheckTarget Action = "check_target"
	// ActionChangeRequest: the request itself has to change.
	ActionChangeRequest Action = "change_request"
	// ActionCallerAccess: the caller's own Kubernetes access has to change.
	ActionCallerAccess Action = "caller_access"
	// ActionCollectorConfig: Radar's identity, credentials or collection
	// configuration has to change.
	ActionCollectorConfig Action = "collector_config"
	// ActionNone: the code is an answer, not a failure.
	ActionNone Action = "none"
)

var actions = map[Code]Action{
	RBACDenied:                   ActionCallerAccess,
	NoNamespaceAccess:            ActionCallerAccess,
	PermissionCheckFailed:        ActionRetry,
	CollectorForbidden:           ActionCollectorConfig,
	CollectorCredentialsRejected: ActionCollectorConfig,
	CollectorScopeLimited:        ActionCollectorConfig,
	KindSyncPending:              ActionRetry,
	KindSyncFailed:               ActionWait,
	KindWatchFailed:              ActionRequestKind,
	KindNotWatched:               ActionCollectorConfig,
	KindUnknown:                  ActionChangeRequest,
	KindNotWatchable:             ActionChangeRequest,
	OutsideNamespaceScope:        ActionChangeRequest,
	OutsideNamespaceSelection:    ActionChangeRequest,
	NamespaceNotVisible:          ActionChangeRequest,
	NamespaceNotFound:            ActionChangeRequest,
	NamespaceRequired:            ActionChangeRequest,
	ObjectNotFound:               ActionNone,
	ListTooLarge:                 ActionChangeRequest,
	FilterEvalFailed:             ActionChangeRequest,
	ClusterConnecting:            ActionRetry,
	ClusterDisconnected:          ActionCollectorConfig,
	ConnectionChanged:            ActionCheckTarget,
	ReadError:                    ActionRetry,
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
// client treat the cluster itself as failing.
func AffectsWholeCluster(c Code) bool {
	return c == ClusterConnecting || c == ClusterDisconnected || c == CollectorCredentialsRejected
}

// precedence orders codes for an error that has to name one, by what the
// caller should do first: fix a request no retry can answer, then wait for
// what resolves by itself — a retry then returns at least part of the answer —
// then act on their own access, then on Radar's limits.
var precedence = []Code{
	NamespaceRequired, KindUnknown, KindNotWatchable, ListTooLarge,
	ClusterDisconnected, ClusterConnecting, ConnectionChanged,
	KindSyncPending, PermissionCheckFailed, KindWatchFailed,
	RBACDenied, NoNamespaceAccess, NamespaceNotVisible, NamespaceNotFound,
	OutsideNamespaceScope, OutsideNamespaceSelection,
	CollectorCredentialsRejected, CollectorForbidden, KindNotWatched,
	KindSyncFailed, CollectorScopeLimited, FilterEvalFailed, ReadError,
	ObjectNotFound,
}

// Primary returns the code an error reports when several codes apply, such
// as a read whose every namespace failed for a different reason. The error's
// coverage keeps the rest. It returns "" for no codes.
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
