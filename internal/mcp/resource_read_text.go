package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// errorNext is what an agent can do itself after a failure with code, or ""
// when only a person can change the outcome, and the error says who.
func errorNext(code readreason.Code) string {
	switch code {
	case readreason.ClusterConnecting:
		return "" // its message already says to retry shortly
	case readreason.ReadError:
		return "Retry in a few seconds; if it keeps failing, report the error to whoever runs Radar."
	case readreason.KindUnknown:
		return "Check the spelling, or pass group for a custom resource; list_resources with kind=customresourcedefinitions lists the custom kinds installed here."
	}
	switch action, _ := readreason.ActionFor(code); action {
	case readreason.ActionRetry:
		return "Retry in a few seconds."
	case readreason.ActionWait:
		return "Retry in a minute."
	case readreason.ActionCheckTarget:
		return "Confirm which cluster Radar is connected to, then retry."
	}
	return ""
}

// beyondCallerAccess reports a narrowing the caller's own access doesn't
// explain: what Radar collects, or its namespace pin. A
// denial already names the caller's namespaces.
func beyondCallerAccess(by []resourceapi.Narrowing) bool {
	for _, n := range by {
		if n != resourceapi.NarrowedByUserAccess {
			return true
		}
	}
	return false
}

// listSummary is what a model reads first: whether the rows are complete for
// what was asked, which parts are unknown and why, and who can widen the read.
// It leads with that verdict and keeps every part that went unread, because
// readers drop caveats that come last; coverage carries the same facts as
// data for code to branch on.
func listSummary(ctx context.Context, resource string, rows int, rowKind string, cov resourceapi.Coverage) string {
	count := countPhrase(rows, resource, rowKind)
	narrowed := len(cov.NarrowedBy) > 0 && cov.Scope.Namespaces != nil
	if cov.Status != resourceapi.CoveragePartial {
		switch {
		case narrowed:
			return fmt.Sprintf("Complete within %s: %s in %s. Other namespaces are unknown.%s", narrowingScopeName(cov.NarrowedBy), count, namespacePhrase(cov.Scope.Namespaces), narrowingRemedy(cov.NarrowedBy))
		case cov.Scope.Type == resourceapi.ScopeCluster:
			return fmt.Sprintf("Complete: %s in the cluster.", count)
		case cov.Scope.Namespaces != nil:
			return fmt.Sprintf("Complete: %s in %s.", count, namespacePhrase(cov.Scope.Namespaces))
		case rows == 0:
			return fmt.Sprintf("Complete: no %s in any namespace.", resource)
		default:
			return fmt.Sprintf("Complete: %s across every namespace.", count)
		}
	}
	where := "the part of the scope Radar could read"
	if cov.Read != nil && cov.Read.Namespaces != nil {
		where = namespacePhrase(cov.Read.Namespaces)
	}
	s := []string{fmt.Sprintf("Partial: %s from %s.", count, where)}
	if rows == 0 {
		s[0] = fmt.Sprintf("Partial: no %s in %s.", resource, where)
	}
	for _, gap := range cov.Gaps {
		s = append(s, gapSentence(ctx, resource, gap))
	}
	if narrowed {
		s = append(s, otherNamespacesUnknown(ctx, resource, cov))
	}
	return strings.Join(s, " ")
}

// otherNamespacesUnknown says why namespaces outside a narrowed scope weren't
// read, and who can widen it.
func otherNamespacesUnknown(ctx context.Context, resource string, cov resourceapi.Coverage) string {
	return fmt.Sprintf("Other namespaces are unknown: %s.%s", narrowingCause(resource, cov.NarrowedBy, namespacePhrase(cov.Scope.Namespaces)), narrowingRemedy(cov.NarrowedBy))
}

// countPhrase is "no services", "1 Service" or "3 services". A single row is
// named by its Kind: resource plurals don't reliably singularize (Endpoints,
// or a CRD's chosen plural).
func countPhrase(n int, resource, rowKind string) string {
	switch {
	case n == 0:
		return "no " + resource
	case n == 1 && rowKind != "":
		return "1 " + rowKind
	}
	return fmt.Sprintf("%d %s", n, resource)
}

// onlyRowKind is the Kind of a list's only row, or "" when it has more or none.
func onlyRowKind(items []any) string {
	if len(items) != 1 {
		return ""
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		return ""
	}
	var row struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &row) != nil {
		return ""
	}
	return row.Kind
}

// narrowingScopeName names what narrowed a complete list, as in "complete
// within your access".
func narrowingScopeName(by []resourceapi.Narrowing) string {
	var names []string
	for _, n := range by {
		switch n {
		case resourceapi.NarrowedByUserAccess:
			names = append(names, "your access")
		case resourceapi.NarrowedByNamespacePin:
			names = append(names, "Radar's --namespace-scope setting")
		case resourceapi.NarrowedByNamespaceCollection:
			names = append(names, "Radar's collection")
		}
	}
	if len(names) > 1 {
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	return strings.Join(names, "")
}

// narrowingCause says why an unscoped request covers only these namespaces.
// Several causes leave only their overlap, which none of them alone limits the
// request to, so they are named together.
func narrowingCause(resource string, by []resourceapi.Narrowing, these string) string {
	if len(by) == 1 {
		switch by[0] {
		case resourceapi.NarrowedByUserAccess:
			return "your access covers only " + these
		case resourceapi.NarrowedByNamespacePin:
			return "Radar is set to read only " + these + " (--namespace-scope)"
		case resourceapi.NarrowedByNamespaceCollection:
			return fmt.Sprintf("Radar collects %s only in %s", resource, these)
		}
	}
	var causes []string
	for _, n := range by {
		switch n {
		case resourceapi.NarrowedByUserAccess:
			causes = append(causes, "your access")
		case resourceapi.NarrowedByNamespacePin:
			causes = append(causes, "Radar's --namespace-scope setting")
		case resourceapi.NarrowedByNamespaceCollection:
			causes = append(causes, "what Radar collects")
		}
	}
	if len(causes) > 1 {
		causes = []string{strings.Join(causes[:len(causes)-1], ", ") + " and " + causes[len(causes)-1]}
	}
	return fmt.Sprintf("the scope is where %s overlap", strings.Join(causes, ""))
}

// narrowingRemedy says, as sentences, who widens a narrowing. A Radar
// Cloud role is named only for an actual denial, not for routine narrowing. It is short on
// purpose: narrowing is routine, and the full remedy for what Radar collects
// comes with a namespace the caller named that Radar doesn't collect.
func narrowingRemedy(by []resourceapi.Narrowing) string {
	var out string
	if slices.Contains(by, resourceapi.NarrowedByUserAccess) {
		out += " A cluster admin can widen your access."
	}
	if slices.Contains(by, resourceapi.NarrowedByNamespacePin) {
		out += " Whoever runs Radar sets --namespace-scope."
	}
	if slices.Contains(by, resourceapi.NarrowedByNamespaceCollection) {
		out += " Whoever runs Radar can widen its collection."
	}
	return out
}

// gapSentence says what part of a list's scope went unread, why, and who
// other than the agent can change that. What the agent can do itself is
// listNext's.
func gapSentence(ctx context.Context, resource string, gap resourceapi.Gap) string {
	where := "all of it"
	if gap.Namespaces != nil {
		where = namespacePhrase(gap.Namespaces)
	}
	why, who := gapReason(ctx, resource, gap.Code)
	if who == "" {
		return fmt.Sprintf("Unknown: %s, because %s.", where, why)
	}
	return fmt.Sprintf("Unknown: %s, because %s (%s).", where, why, who)
}

// gapReason is why a code left part of a list unread, and who other than the
// agent can change that ("" when retrying is enough), as clauses.
func gapReason(ctx context.Context, resource string, code readreason.Code) (why, who string) {
	switch code {
	case readreason.RadarNamespaceNotCollected:
		return fmt.Sprintf("Radar doesn't collect %s there", resource), collectionRemedy(resource) + notYourAccessClause(ctx)
	case readreason.RadarForbidden:
		return "Kubernetes refuses Radar's own identity there", identityRemedy(resource, " there", "list/watch") + notYourAccessClause(ctx)
	case readreason.RadarCredentialsRejected:
		// Recorded when Radar connected or met on this read: a reconnect
		// re-checks Radar's access either way.
		return "Kubernetes rejected Radar's own credentials there", "whoever runs Radar must check them; reconnecting Radar with POST /api/connection/retry makes it check its access again" + notYourAccessClause(ctx)
	case readreason.KindSyncPending:
		return fmt.Sprintf("Radar is still loading %s there", resource), ""
	case readreason.KindSyncTimedOut:
		return fmt.Sprintf("Radar's load of %s there didn't finish in time", resource), "if it keeps failing, whoever runs Radar should check its cluster connection and list/watch permissions"
	case readreason.UserForbidden:
		return fmt.Sprintf("your Kubernetes access doesn't allow listing %s there", resource), withCloudRole(ctx, "a cluster admin can grant it")
	case readreason.UserPermissionCheckFailed:
		return "Radar couldn't check your access there", ""
	}
	why = fmt.Sprintf("Radar couldn't read %s there (%s)", resource, code)
	switch action, _ := readreason.ActionFor(code); action {
	case readreason.ActionUserAccess:
		return why, withCloudRole(ctx, "a cluster admin can grant you access")
	case readreason.ActionRadarConfig:
		return why, "whoever runs Radar must change its access or configuration"
	}
	return why, ""
}

// listNext is what the agent can do itself to complete a list's answer, or ""
// when nothing it does would change it. Two things qualify: retrying, for
// parts that clear on their own; and naming one namespace, when the list
// counts namespaces it couldn't name, so the agent can't tell whether the one
// it cares about was read.
func listNext(cov resourceapi.Coverage) string {
	steps := []string{}
	if retry := retrySteps(cov); retry != "" {
		steps = append(steps, retry)
	}
	if countsUnnamedNamespaces(cov) {
		steps = append(steps, "To check one namespace, call list_resources again with namespace set to it.")
	}
	return strings.Join(steps, " ")
}

// retrySteps says when retrying would read the parts of cov that clear on
// their own, or "" when none would.
func retrySteps(cov resourceapi.Coverage) string {
	var steps []string
	for _, when := range []string{"in a few seconds", "in a minute"} {
		var where []string
		unnamed := false
		for _, gap := range cov.Gaps {
			if retryAfter(gap.Code) != when {
				continue
			}
			if gap.Namespaces == nil || gap.Namespaces.Names == nil {
				unnamed = true
				continue
			}
			where = append(where, gap.Namespaces.Names...)
		}
		switch {
		case unnamed || len(where) > resourceapi.MCPSetLimit:
			steps = append(steps, fmt.Sprintf("Retry %s to read what this list skipped.", when))
		case len(where) > 0:
			steps = append(steps, fmt.Sprintf("Retry %s to read %s.", when, namespacePhrase(resourceapi.Names(where, resourceapi.MCPSetLimit))))
		}
	}
	return strings.Join(steps, " ")
}

// retryAfter is when the same request may read what code left unread, or ""
// when retrying alone won't.
func retryAfter(code readreason.Code) string {
	switch action, _ := readreason.ActionFor(code); action {
	case readreason.ActionRetry:
		return "in a few seconds"
	case readreason.ActionWait:
		return "in a minute"
	}
	return ""
}

// countsUnnamedNamespaces reports a list whose scope, read or gaps count more
// namespaces than they name.
func countsUnnamedNamespaces(cov resourceapi.Coverage) bool {
	unnamed := func(set *resourceapi.NameSet) bool {
		return set != nil && (set.Extent == resourceapi.ExtentUnknown || set.Extent == "" && set.Names == nil)
	}
	if unnamed(cov.Scope.Namespaces) || (cov.Read != nil && unnamed(cov.Read.Namespaces)) {
		return true
	}
	return slices.ContainsFunc(cov.Gaps, func(gap resourceapi.Gap) bool { return unnamed(gap.Namespaces) })
}

// namespacePhrase names a set of namespaces, or counts them when the set is
// too large to list.
func namespacePhrase(set *resourceapi.NameSet) string {
	switch set.Extent {
	case resourceapi.ExtentAll:
		return "every namespace asked about"
	case resourceapi.ExtentUnknown:
		return "some namespaces Radar can't name"
	}
	if set.Names == nil {
		return fmt.Sprintf("%d namespaces", set.Count)
	}
	quoted := make([]string, len(set.Names))
	for i, ns := range set.Names {
		quoted[i] = fmt.Sprintf("%q", ns)
	}
	if set.Count == 1 {
		return "namespace " + strings.Join(quoted, ", ")
	}
	return "namespaces " + strings.Join(quoted, ", ")
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// collectionRemedy says, as a clause, who widens the namespaces Radar collects
// resource in. Naming them is the fix whether Radar could not discover them or
// stopped at its candidate cap, as long as its identity can list there.
func collectionRemedy(resource string) string {
	if k8s.IsInCluster() {
		return fmt.Sprintf("whoever installed Radar can add namespaces with --namespaces through the chart's extraArgs, if its service account can list %s there", resource)
	}
	return fmt.Sprintf("whoever runs Radar can add namespaces with --namespaces, if its kubeconfig identity can list %s there", resource)
}

// identityRemedy says, as a clause, who lets Radar's own identity perform
// action on resource. where is "" or a place such as " there".
func identityRemedy(resource, where, action string) string {
	if k8s.IsInCluster() {
		return fmt.Sprintf("whoever installed Radar must grant its service account %s on %s%s", action, resource, where)
	}
	return fmt.Sprintf("whoever runs Radar must use a kubeconfig identity that can %s %s%s", action, resource, where)
}

// notYourAccess says, with auth on, that the caller's own access is not the
// fix for a limit of Radar's identity. With auth off the caller runs Radar with
// that identity, and the sentence would be wrong.
func notYourAccess(ctx context.Context) string {
	if pkgauth.UserFromContext(ctx) != nil {
		return " Granting you access wouldn't change this."
	}
	return ""
}

func notYourAccessClause(ctx context.Context) string {
	if pkgauth.UserFromContext(ctx) != nil {
		return "; granting you access wouldn't change this"
	}
	return ""
}

// withCloudRole adds the other way a Radar Cloud viewer or member may get
// access to remedy: the chart binds each Radar Cloud role to Kubernetes
// permissions, so an organization owner can raise the role. Radar doesn't know
// the cluster's bindings, so a higher role only may grant it. An owner has no
// higher role, and outside Radar Cloud there are none.
func withCloudRole(ctx context.Context, remedy string) string {
	if !cloud.Mode() {
		return remedy
	}
	switch role := pkgauth.CloudRoleFromContext(ctx); role {
	case pkgauth.RoleViewer, pkgauth.RoleMember:
		return fmt.Sprintf("%s, or an owner of your Radar Cloud organization can raise your %s role, which may grant it", remedy, role)
	}
	return remedy
}
