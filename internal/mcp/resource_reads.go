package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// readError is a resource-read failure with the code an agent branches on.
// Its Go error text is "<code>: <explanation>"; readErrorResult sends it to
// MCP clients as a resourceapi.ReadError body. unread, when the failure was
// assessed namespace by namespace, becomes the body's coverage.
type readError struct {
	code   readreason.Code
	err    error
	unread *gapSet
	// coverage is attached by a list, which knows the read's scope. It is
	// sent beside the sentence, so a program sees every cause and the
	// namespaces each affects, not only the most actionable code.
	coverage *resourceapi.Coverage
	// next is what the agent can do itself to get an answer, when it depends
	// on the request; otherwise the code decides (errorNext).
	next string
}

func (e *readError) Error() string { return string(e.code) + ": " + e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

func readErrorf(code readreason.Code, format string, args ...any) error {
	return &readError{code: code, err: fmt.Errorf(format, args...)}
}

// readErrorResult renders a readError as an MCP tool error. The body goes in
// both structuredContent and the text block: clients that read only text —
// including Radar Hub, which records the text as investigation evidence —
// must get the same code and coverage. It reports false for any other error,
// which keeps the SDK's text-only form.
func readErrorResult(err error) (*mcp.CallToolResult, bool) {
	var re *readError
	if !errors.As(err, &re) {
		return nil, false
	}
	next := re.next
	if next == "" && re.coverage != nil {
		// A failed list's gaps can have causes other than its code, and
		// some of them clear on their own.
		next = retrySteps(*re.coverage)
	}
	if next == "" {
		next = errorNext(re.code)
	}
	body := mcpReadError{Error: re.err.Error(), ErrorCode: re.code, Next: next, Coverage: re.coverage}
	text, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return nil, false
	}
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
		StructuredContent: body,
	}
	result.SetError(err)
	return result, true
}

// mcpReadError is resourceapi.ReadError plus `next`, which only MCP sends: it
// is addressed to the agent, which REST's readers are not. next comes before
// coverage so a client that keeps only the head of a long result keeps it.
type mcpReadError struct {
	Error     string                `json:"error"`
	ErrorCode readreason.Code       `json:"error_code"`
	Next      string                `json:"next,omitempty"`
	Coverage  *resourceapi.Coverage `json:"coverage,omitempty"`
}

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

// errConnectionChanged rejects a read whose cache, dynamic cache or discovery
// handle was replaced while it ran: its rows may mix two clusters, and a retry
// may reach a different one.
func errConnectionChanged() error {
	return readErrorf(readreason.ConnectionChanged, "Radar's cluster connection changed during this read (a context switch or reconnect), so the result was discarded.")
}

// listEnvelope is every list_resources result, sent as the only text block.
// Findings parses the text agent backends hand it, and the evidence ledger
// matches that text against the producer's exactly, so the summary, the rows
// and their coverage must be one JSON value: a second block would be joined
// into something neither accepts. The summary leads: it is what a model reads
// first, and a reader that keeps only the head of a long result still has it.
//
// next, when present, is what the agent can do itself to complete the answer.
// The summary stays a description a person can read too: Findings shows it.
type listEnvelope struct {
	Summary  string               `json:"summary"`
	Next     string               `json:"next,omitempty"`
	Coverage resourceapi.Coverage `json:"coverage"`
	Items    []any                `json:"items"`
}

// listResult renders a list of resource (its plural, as the summary names it).
func listResult(ctx context.Context, resource string, items []any, coverage resourceapi.Coverage) (*mcp.CallToolResult, any, error) {
	if items == nil {
		items = []any{}
	}
	body, err := json.Marshal(listEnvelope{Summary: listSummary(ctx, resource, len(items), onlyRowKind(items), coverage), Next: listNext(coverage), Coverage: coverage, Items: items})
	if err != nil {
		log.Printf("[mcp] Failed to marshal result: %v", err)
		return nil, nil, fmt.Errorf("failed to marshal result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil, nil
}

// readTarget is what a namespaced read is about: the namespace the request
// named, or what remained of "every namespace" after Radar's --namespace-scope
// pin and the caller's access narrowed it. A named namespace stays the target
// even when it can't be read, so what's missing is a gap; implicit narrowing
// is declared instead, and "complete" then means complete within it.
type readTarget struct {
	namespaces []string // nil: every namespace
	explicit   bool
	narrowedBy []resourceapi.Narrowing
}

func resolveReadTarget(ctx context.Context, requested []string) (readTarget, error) {
	target := readTarget{explicit: requested != nil}
	user, perms, checked := resolveUserPermsDecision(ctx)
	if !checked {
		return target, readErrorf(readreason.UserPermissionCheckFailed, "Radar couldn't check which namespaces you can access, so it read nothing.")
	}
	// The pin is judged only against namespaces the caller can see: one they
	// can't is answered the same whether or not Radar is pinned to it, so
	// guessing names can't find the pin.
	clamped, ok := clampToNamespacePin(requested)
	outside := requested
	if visible := pkgauth.FilterNamespacesForUser(requested, user, perms); requested != nil && visible != nil {
		if len(visible) == 0 {
			// Nothing named is visible: answered below as not visible.
			clamped, ok = requested, true
		} else {
			clamped, ok = clampToNamespacePin(visible)
			outside = visible
		}
	}
	if !ok {
		return target, readErrorf(readreason.OutsideNamespacePin, "Radar is limited to other namespaces by its --namespace-scope setting, so it can't read namespace %q. Whoever runs Radar sets --namespace-scope.", namespaceForError(outside))
	}
	allowed := pkgauth.FilterNamespacesForUser(clamped, user, perms)
	unrestricted := pkgauth.FilterNamespacesForUser(nil, user, perms) == nil
	if allowed != nil && len(allowed) == 0 {
		// A namespace the caller named and can't see may not exist either;
		// the code doesn't say which. Unscoped, clamped is the
		// --namespace-scope target, which this caller cannot read: name only
		// what the caller asked for.
		if target.explicit {
			scope := fmt.Sprintf("Namespace %q doesn't exist or you can't access it", clamped[0])
			if len(clamped) > 1 {
				scope = fmt.Sprintf("Namespaces %q don't exist or you can't access them", namespaceForError(clamped))
			}
			err := &readError{code: readreason.UserNamespaceNotVisible, err: fmt.Errorf("%s, so Radar can't say what's in it. If the name is right, %s %s", scope, withCloudRole(ctx, "a cluster admin can grant access"), namespaceAccessRule)}
			// list_namespaces needs Radar to list Namespaces, which an identity
			// limited to some namespaces may not be allowed to.
			if cache := k8s.GetResourceCache(); cache != nil && cache.Namespaces() != nil {
				err.next = "Call list_namespaces to see the namespaces you can access."
			}
			return target, err
		}
		return target, readErrorf(readreason.UserNoNamespaceAccess, "You can't access any namespace in this request's scope, so Radar read nothing. %s %s", withCloudRole(ctx, "A cluster admin can grant access"), namespaceAccessRule)
	}
	// Only a caller who can see every namespace learns that one doesn't
	// exist; anyone else was answered above without telling the two apart.
	if target.explicit && unrestricted {
		if missing := missingNamespaces(clamped); len(missing) > 0 && len(missing) == len(clamped) {
			scope := fmt.Sprintf("Namespace %q doesn't exist in this cluster", missing[0])
			if len(missing) > 1 {
				scope = fmt.Sprintf("Namespaces %q don't exist in this cluster", namespaceForError(missing))
			}
			return target, &readError{code: readreason.NamespaceNotFound, err: fmt.Errorf("%s, so there is nothing to read there.", scope), next: "Call list_namespaces to see the namespaces that exist."}
		}
	}
	// Every narrowing that applied to a request that named no namespace, as
	// REST and search report it: the caller's access when it is restricted,
	// and the pin.
	if !target.explicit {
		if !unrestricted {
			target.narrowedBy = append(target.narrowedBy, resourceapi.NarrowedByUserAccess)
		}
		if clamped != nil {
			target.narrowedBy = append(target.narrowedBy, resourceapi.NarrowedByNamespacePin)
		}
	}
	target.namespaces = allowed
	return target, nil
}

// missingNamespaces returns the namespaces Radar knows don't exist: its
// Namespace informer has synced and holds none by that name. Without a synced
// informer, none is known to be missing.
func missingNamespaces(namespaces []string) []string {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Namespaces() == nil || !cache.IsKindReady("namespaces") {
		return nil
	}
	var missing []string
	for _, ns := range namespaces {
		if _, err := cache.Namespaces().Get(ns); apierrors.IsNotFound(err) {
			missing = append(missing, ns)
		}
	}
	return missing
}

// narrowedToCollection is t narrowed to what plan's kind is collected in. A
// finite target keeps the namespaces read elsewhere withheld (such as Secrets
// the caller can't list); only the ones Radar doesn't collect leave it.
func (t readTarget) narrowedToCollection(plan *typedRead) readTarget {
	return t.narrowedByNamespaceCollection(plan.collected, plan.uncovered)
}

// narrowedByNamespaceCollection narrows t to collected when it is every namespace, or
// else removes uncovered from it.
func (t readTarget) narrowedByNamespaceCollection(collected, uncovered []string) readTarget {
	if t.namespaces == nil {
		t.namespaces = collected
	} else {
		t.namespaces = withoutNamespaces(t.namespaces, uncovered)
	}
	if !slices.Contains(t.narrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		t.narrowedBy = append(slices.Clone(t.narrowedBy), resourceapi.NarrowedByNamespaceCollection)
	}
	return t
}

// withoutNamespaces returns namespaces less those in drop, in linear time:
// both can hold thousands of names.
func withoutNamespaces(namespaces, drop []string) []string {
	dropped := make(map[string]bool, len(drop))
	for _, ns := range drop {
		dropped[ns] = true
	}
	return slices.DeleteFunc(slices.Clone(namespaces), func(ns string) bool { return dropped[ns] })
}

// resourceReadNamespaces returns the namespaces a read of requested may reach
// for this caller: nil for every namespace.
func resourceReadNamespaces(ctx context.Context, requested []string) ([]string, error) {
	target, err := resolveReadTarget(ctx, requested)
	return target.namespaces, err
}

func namespaceForError(namespaces []string) string {
	if len(namespaces) == 1 {
		return namespaces[0]
	}
	if len(namespaces) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(namespaces[:3], ","), len(namespaces)-3)
	}
	return strings.Join(namespaces, ",")
}

// namespaceAccessRule says how Radar decides which namespaces a caller can
// access, for errors whose fix is that access.
const namespaceAccessRule = "(Radar counts a namespace as yours when you can list pods or deployments in it, and re-checks about every 2 minutes)."

// qualifiedResource names a resource as kubectl does: the plural, then its API
// group when it has one.
func qualifiedResource(group, resource string) string {
	if group == "" {
		return resource
	}
	return resource + "." + group
}

func resourcePermissionError(ctx context.Context, checked bool, verb, group, resource, namespace string) error {
	return resourcePermissionErrorNext(ctx, checked, verb, group, resource, namespace, "")
}

// resourcePermissionErrorNext is resourcePermissionError with what the caller
// can still ask for after a denial.
func resourcePermissionErrorNext(ctx context.Context, checked bool, verb, group, resource, namespace, next string) error {
	scope := "across the cluster"
	if strings.Contains(namespace, ",") {
		scope = fmt.Sprintf("in namespaces %q", namespace)
	} else if namespace != "" {
		scope = fmt.Sprintf("in namespace %q", namespace)
	}
	name := qualifiedResource(group, resource)
	if !checked {
		return readErrorf(readreason.UserPermissionCheckFailed, "Radar couldn't check whether you can %s %s %s, so it read nothing.", verb, name, scope)
	}
	return &readError{code: readreason.UserForbidden, err: fmt.Errorf("Your Kubernetes access doesn't allow you to %s %s %s, so Radar can't say what exists there. %s.", verb, name, scope, withCloudRole(ctx, "A cluster admin can grant that permission")), next: next}
}

// gapSet collects the namespaces a read could not serve, by code. A code
// recorded with no namespaces reaches the whole scope.
type gapSet struct {
	namespaces map[readreason.Code][]string
	unknown    map[readreason.Code]bool
	firstErr   map[readreason.Code]error
}

func (g *gapSet) add(code readreason.Code, err error, namespaces ...string) {
	if g.namespaces == nil {
		g.namespaces, g.unknown, g.firstErr = map[readreason.Code][]string{}, map[readreason.Code]bool{}, map[readreason.Code]error{}
	}
	if len(namespaces) == 0 {
		g.unknown[code] = true
	}
	g.namespaces[code] = append(g.namespaces[code], namespaces...)
	if _, ok := g.firstErr[code]; !ok && err != nil {
		g.firstErr[code] = err
	}
}

func (g *gapSet) merge(other gapSet) {
	for code, namespaces := range other.namespaces {
		g.add(code, other.firstErr[code], namespaces...)
		if other.unknown[code] {
			g.unknown[code] = true
		}
	}
}

func (g *gapSet) empty() bool { return len(g.namespaces) == 0 }

// absorb adds a failed read's gaps to g: its assessed gaps, or its code over
// within, the namespaces that read covered (nil for the whole scope). It
// reports false for an error that isn't a read failure.
func (g *gapSet) absorb(err error, within []string) bool {
	var re *readError
	if !errors.As(err, &re) {
		return false
	}
	if re.unread != nil {
		g.merge(*re.unread)
	} else {
		g.add(re.code, err, within...)
	}
	return true
}

func (g *gapSet) codes() []readreason.Code {
	codes := make([]readreason.Code, 0, len(g.namespaces))
	for code := range g.namespaces {
		codes = append(codes, code)
	}
	slices.SortFunc(codes, func(a, b readreason.Code) int {
		switch {
		case a == b:
			return 0
		case readreason.Primary([]readreason.Code{a, b}) == a:
			return -1
		default:
			return 1
		}
	})
	return codes
}

// gaps renders the set, most actionable first, each with the namespaces it
// reaches: all of the scope for a code recorded without namespaces, otherwise
// the ones the caller may see. A code whose every namespace is hidden keeps
// its gap with an unknown extent, because the gap is real but where it lies
// is not theirs to learn. A cluster-scoped kind's gaps have no namespaces.
func (g *gapSet) gaps(ctx context.Context, limit int, clusterScoped bool) []resourceapi.Gap {
	out := []resourceapi.Gap{}
	for _, code := range g.codes() {
		gap := resourceapi.Gap{Code: code}
		switch {
		case clusterScoped:
		case g.unknown[code]:
			gap.Namespaces = resourceapi.AllNames()
		default:
			if visible := callerVisibleNamespaces(ctx, g.namespaces[code]); len(visible) > 0 {
				gap.Namespaces = resourceapi.Names(visible, limit)
			} else {
				gap.Namespaces = resourceapi.UnknownNames()
			}
		}
		out = append(out, gap)
	}
	return out
}

// failure is the error for a read that served nothing: the most actionable
// code, its own explanation, and every gap.
func (g *gapSet) failure() error {
	code := readreason.Primary(g.codes())
	var re *readError
	if errors.As(g.firstErr[code], &re) {
		return &readError{code: re.code, err: re.err, unread: g}
	}
	return &readError{code: code, err: errors.New("Radar couldn't read any part of the requested scope, so it can't say what exists there."), unread: g}
}

// listScope is the scope a list's coverage reports for target.
func listScope(target readTarget, clusterScoped bool) resourceapi.Scope {
	switch {
	case clusterScoped:
		return resourceapi.Scope{Type: resourceapi.ScopeCluster}
	case target.namespaces == nil:
		return resourceapi.Scope{Type: resourceapi.ScopeAllNamespaces}
	default:
		return resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(target.namespaces, resourceapi.MCPSetLimit)}
	}
}

// listCoverageFor assembles a successful list's coverage. read is what Radar
// read; readAll means the whole scope.
func listCoverageFor(clusterScoped bool, target readTarget, read []string, readAll bool, gaps []resourceapi.Gap) resourceapi.Coverage {
	cov := resourceapi.Coverage{
		Status: resourceapi.CoverageComplete,
		Scope:  listScope(target, clusterScoped),
		Gaps:   gaps,
	}
	if !clusterScoped {
		cov.NarrowedBy = target.narrowedBy
	}
	readScope := cov.Scope
	if !readAll && !clusterScoped {
		readScope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(read, resourceapi.MCPSetLimit)}
	}
	cov.Read = &readScope
	if len(gaps) > 0 {
		cov.Status = resourceapi.CoveragePartial
	}
	return cov
}

// withListCoverage attaches a failed list's coverage: its scope and every gap,
// with no read scope because the error returns no rows. The sentence says the
// same in words, for the model: every cause when namespaces failed for
// different reasons (one cause keeps its own explanation), and how an unscoped
// request was narrowed when something other than the caller's own access
// narrowed it.
func withListCoverage(ctx context.Context, err error, resource string, target readTarget, clusterScoped bool) error {
	var re *readError
	if !errors.As(err, &re) {
		return err
	}
	if re.unread == nil {
		// A failure that wasn't assessed namespace by namespace reached all
		// of the target.
		whole := &gapSet{}
		whole.add(re.code, re.err, target.namespaces...)
		re = &readError{code: re.code, err: re.err, unread: whole, next: re.next}
	}
	cov := &resourceapi.Coverage{Status: resourceapi.CoveragePartial, Scope: listScope(target, clusterScoped), Gaps: re.unread.gaps(ctx, resourceapi.MCPSetLimit, clusterScoped)}
	if !clusterScoped {
		cov.NarrowedBy = target.narrowedBy
	}
	s := []string{re.err.Error()}
	if len(cov.Gaps) > 1 {
		s = []string{fmt.Sprintf("Radar couldn't read any %s, so it can't say what exists.", resource)}
		for _, gap := range cov.Gaps {
			s = append(s, gapSentence(ctx, resource, gap))
		}
	}
	if beyondCallerAccess(cov.NarrowedBy) && cov.Scope.Namespaces != nil {
		s = append(s, otherNamespacesUnknown(ctx, resource, *cov))
	}
	explanation := re.err
	if len(s) > 1 || s[0] != re.err.Error() {
		explanation = &explainedError{msg: strings.Join(s, " "), cause: re.err}
	}
	return &readError{code: re.code, err: explanation, unread: re.unread, coverage: cov}
}

// explainedError is a cause reworded for the caller. It still unwraps to the
// cause, so the API server's response stays available to logs.
type explainedError struct {
	msg   string
	cause error
}

func (e *explainedError) Error() string { return e.msg }
func (e *explainedError) Unwrap() error { return e.cause }

// beyondCallerAccess reports a narrowing the caller's own access doesn't
// explain: what Radar collects, its namespace pin, or a saved selection. A
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
		case resourceapi.NarrowedByNamespaceSelection:
			names = append(names, "your saved namespace selection")
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
		case resourceapi.NarrowedByNamespaceSelection:
			return "your saved namespace selection covers only " + these
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
		case resourceapi.NarrowedByNamespaceSelection:
			causes = append(causes, "your saved namespace selection")
		case resourceapi.NarrowedByNamespaceCollection:
			causes = append(causes, "what Radar collects")
		}
	}
	if len(causes) > 1 {
		causes = []string{strings.Join(causes[:len(causes)-1], ", ") + " and " + causes[len(causes)-1]}
	}
	return fmt.Sprintf("the scope is where %s overlap", strings.Join(causes, ""))
}

// narrowingRemedy says, as sentences, who widens a narrowing. A saved
// selection is the caller's own to change, so it needs none, and a Radar
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

func resourceReadCacheIsCurrent(cache *k8s.ResourceCache) bool {
	active, syncing := k8s.SnapshotCaches()
	if active != nil {
		return cache == active
	}
	return cache != nil && cache == syncing
}

// typedRead is what a typed kind's cache can serve of a target now.
type typedRead struct {
	resource string
	// all: the target is every namespace and all of it is readable.
	all bool
	// read: the namespaces to read when !all; non-nil.
	read []string
	// whole: the kind is ready as a whole, so its listers serve the read. A
	// kind still loading elsewhere is read from its namespaces' own stores.
	whole bool
	gaps  gapSet
	// collected, when non-nil, is the part of an implicit target Radar
	// collects: the read was narrowed to it. uncovered is what it left out of
	// a finite target.
	collected, uncovered []string
}

// resourceReadCache returns the cache to read kind from and, for a typed kind,
// what of namespaces (nil: every namespace) it can serve now and why the rest
// is unread. It fails only when nothing can be read; a failure within the
// namespaces Radar collects still returns its plan, so the error can say that
// scope. Dynamic kinds learn their coverage by reading, so they get no plan.
//
// Namespaces Radar doesn't collect are a gap when the request named them
// (explicit); otherwise they narrow the read, like the --namespace-scope pin,
// and collected is set to what remains. A namespace whose credentials
// Kubernetes rejected when the cache was built is a gap either way: Radar
// meant to collect it.
func resourceReadCache(ctx context.Context, kind, group string, namespaces []string, explicit bool) (*k8s.ResourceCache, *typedRead, error) {
	gvr, builtin := k8s.BuiltinGVRAnyGroup(kind)
	typed := builtin && (group == "" || group == gvr.Group) && k8s.TypedKindOwnsGroup(kind, gvr.Group)
	if !typed {
		cache := k8s.GetResourceCache()
		if cache == nil {
			return nil, nil, errNotConnected()
		}
		return cache, nil, nil
	}
	cache, syncing := k8s.SnapshotCaches()
	if cache == nil {
		cache = syncing
	}
	if cache == nil {
		return nil, nil, errNotConnected()
	}
	result := cache.PermissionResult()
	rejected, rejectedCause := recordedCredentialRejections(result, gvr.Resource, namespaces)
	a := cache.AssessKindRead(gvr.Resource, namespaces)
	if a.Readiness == k8s.KindUnavailable {
		if len(rejected) > 0 {
			return nil, nil, errRecordedCredentialsRejected(gvr.Resource, rejected, rejectedCause)
		}
		if result != nil {
			if scope, probed := result.Scopes[gvr.Resource]; probed && !scope.Enabled {
				if apierrors.IsUnauthorized(result.ProbeErrors[gvr.Resource]) {
					return nil, nil, errRecordedCredentialsRejected(gvr.Resource, nil, result.ProbeErrors[gvr.Resource])
				}
				where := " in the namespaces you need"
				if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group); clusterScoped {
					where = ""
				}
				name := qualifiedResource(gvr.Group, gvr.Resource)
				return nil, nil, readErrorf(readreason.RadarForbidden, "Kubernetes doesn't allow %s to list/watch %s, so Radar has none to show and can't say which exist. %s.%s", collectorName(), name, upperFirst(identityRemedy(name, where, "list/watch")), notYourAccess(ctx))
			}
		}
		return nil, nil, errKindNotCollected(qualifiedResource(gvr.Group, gvr.Resource))
	}
	pending := errKindLoading(gvr.Resource)
	failed := errKindLoadFailed(gvr.Resource)
	plan := &typedRead{resource: gvr.Resource, whole: a.Readiness == k8s.KindReady}
	if a.AllNamespaces && namespaces == nil {
		switch a.Readiness {
		case k8s.KindReady:
			plan.all = true
			return cache, plan, nil
		case k8s.KindFailed:
			plan.gaps.add(readreason.KindSyncTimedOut, failed)
		default:
			plan.gaps.add(readreason.KindSyncPending, pending)
		}
		return nil, nil, plan.gaps.failure()
	}
	target := collectorTarget{resource: gvr.Resource, group: gvr.Group, action: "list/watch"}
	notCollected := slices.DeleteFunc(slices.Clone(a.Uncovered), func(ns string) bool { return slices.Contains(rejected, ns) })
	uncovered := len(notCollected) > 0 || (namespaces == nil && !a.AllNamespaces)
	switch {
	case explicit && len(notCollected) > 0:
		plan.gaps.add(readreason.RadarNamespaceNotCollected, coverageError(ctx, target, notCollected, cache.KindNamespaces(gvr.Resource), nil), notCollected...)
	case uncovered:
		plan.collected = slices.Concat(a.Read, a.Loading, a.Failed, rejected)
		slices.Sort(plan.collected)
		plan.collected = slices.Compact(plan.collected)
		plan.uncovered = notCollected
		if len(plan.collected) == 0 {
			plan.gaps.add(readreason.RadarNamespaceNotCollected, coverageError(ctx, target, notCollected, cache.KindNamespaces(gvr.Resource), nil))
		}
	}
	if len(a.Loading) > 0 {
		plan.gaps.add(readreason.KindSyncPending, pending, a.Loading...)
	}
	if len(a.Failed) > 0 {
		plan.gaps.add(readreason.KindSyncTimedOut, failed, a.Failed...)
	}
	if len(rejected) > 0 {
		plan.gaps.add(readreason.RadarCredentialsRejected, errRecordedCredentialsRejected(gvr.Resource, rejected, rejectedCause), rejected...)
	}
	if len(a.Read) == 0 {
		return nil, plan, plan.gaps.failure()
	}
	plan.read = a.Read
	return cache, plan, nil
}

// collectorTarget is what Radar's collector reads for a kind: the resource,
// its API group, and the action it performs (list/watch for a cache, or the
// verb of a direct read).
type collectorTarget struct {
	resource, group, action string
}

func dynamicCollectorTarget(kind, group, verb string) collectorTarget {
	gvr, ok := dynamicReadGVR(kind, group)
	if !ok {
		return collectorTarget{resource: kind, group: group, action: "list/watch"}
	}
	action := "list/watch"
	if directRead(gvr, verb) {
		action = verb
	}
	return collectorTarget{resource: gvr.Resource, group: gvr.Group, action: action}
}

// directRead reports a read that goes straight to the API server instead of an
// informer.
func directRead(gvr schema.GroupVersionResource, verb string) bool {
	return k8s.ShouldBypassDynamicInformer(gvr) ||
		(verb == "get" && ((gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions") ||
			(gvr.Group == "apiregistration.k8s.io" && gvr.Resource == "apiservices")))
}

func errKindLoading(resource string) error {
	return readErrorf(readreason.KindSyncPending, "Radar is still loading %s, so it can't answer yet.", resource)
}

func errKindLoadFailed(resource string) error {
	return readErrorf(readreason.KindSyncTimedOut, "Radar's load of %s didn't finish in time, so it can't answer yet. It keeps trying; if this persists, whoever runs Radar should check its cluster connection and list/watch permissions.", resource)
}

func errKindNotCollected(resource string) error {
	return readErrorf(readreason.RadarKindNotCollected, "Radar isn't collecting %s, so it can't list them or say which exist. Whoever runs Radar must check that its identity can list/watch %s and that Radar is set to collect them.", resource, resource)
}

// coverageError reports requested namespaces Radar's collector could not
// serve. With a refusal — Radar read them for this request and the API server
// denied its identity — the code is radar_forbidden; without one they lie
// outside the coverage the collector established, Radar did not read them,
// and the code is radar_namespace_not_collected. No uncovered namespaces means the
// collector covers only some namespaces of an all-namespaces read. Only
// namespaces the caller may read are named, and coverage in namespaces the
// caller cannot read never changes the wording, so the error discloses
// nothing about other tenants' namespaces.
func coverageError(ctx context.Context, target collectorTarget, uncovered, covered []string, refusal error) error {
	covered = slices.DeleteFunc(slices.Clone(covered), func(ns string) bool { return slices.Contains(uncovered, ns) })
	covered = callerVisibleNamespaces(ctx, covered)
	where := ""
	if len(uncovered) > 0 {
		uncovered = callerVisibleNamespaces(ctx, uncovered)
		switch len(uncovered) {
		case 0:
			where = "in the requested namespaces"
		case 1:
			where = fmt.Sprintf("in namespace %q", uncovered[0])
		default:
			where = fmt.Sprintf("in namespaces %q", namespaceForError(uncovered))
		}
	}
	var coverage string
	switch {
	case len(covered) > 0:
		coverage = fmt.Sprintf(" It collects %s in %s.", target.resource, namespacesForError(covered))
	case refusal == nil:
		// A refusal for this request says nothing about where else the
		// collector could read, so only established coverage gets this.
		coverage = fmt.Sprintf(" It doesn't collect %s in any namespace you can access.", target.resource)
	}
	name := qualifiedResource(target.group, target.resource)
	if refusal != nil {
		at, there := where, " there"
		if at == "" {
			at, there = "across the cluster", ""
		}
		return &readError{code: readreason.RadarForbidden, err: &collectorError{
			msg:   fmt.Sprintf("Kubernetes refused %s when Radar tried to %s %s %s, so Radar can't say what exists there.%s %s.%s", collectorName(), target.action, name, at, coverage, upperFirst(identityRemedy(name, there, target.action)), notYourAccess(ctx)),
			cause: refusal,
		}}
	}
	remedy := upperFirst(collectionRemedy(target.resource)) + "." + notYourAccess(ctx)
	if where == "" {
		return readErrorf(readreason.RadarNamespaceNotCollected, "Radar collects %s only in some namespaces, so it can't answer for the whole cluster.%s %s", target.resource, coverage, remedy)
	}
	return readErrorf(readreason.RadarNamespaceNotCollected, "Radar doesn't collect %s %s, so it can't say what exists there.%s %s", target.resource, where, coverage, remedy)
}

// namespacesForError names namespaces in an error sentence, abbreviated as
// namespaceForError does.
func namespacesForError(namespaces []string) string {
	if len(namespaces) == 1 {
		return fmt.Sprintf("namespace %q", namespaces[0])
	}
	return fmt.Sprintf("namespaces %q", namespaceForError(namespaces))
}

// collectorError is a caller-facing error about a read Radar made with its own
// identity. Its message never includes the API server's response, which names
// that identity (for a service account, the namespace Radar runs in) and the
// scope it was refused in. The response stays reachable through Unwrap, for
// classification and for collectorCause to log.
type collectorError struct {
	msg   string
	cause error
}

func (e *collectorError) Error() string { return e.msg }
func (e *collectorError) Unwrap() error { return e.cause }

// collectorCause returns the API server response behind a collector error, or
// nil when err carries none.
func collectorCause(err error) error {
	var cause error
	for {
		var ce *collectorError
		if !errors.As(err, &ce) || ce.cause == nil {
			return cause
		}
		cause, err = ce.cause, ce.cause
	}
}

func collectorName() string {
	if k8s.IsInCluster() {
		return "Radar's service account"
	}
	return "Radar's kubeconfig identity"
}

// collectorAuthError is the caller-facing error for the API server refusing a
// read Radar made with its own identity, or nil when err is not a refusal.
func collectorAuthError(ctx context.Context, err error, target collectorTarget, namespace string) error {
	switch {
	case apierrors.IsUnauthorized(err):
		return collectorUnauthorizedError(target.resource, err)
	case apierrors.IsForbidden(err):
		var uncovered []string
		if namespace != "" {
			uncovered = []string{namespace}
		}
		return coverageError(ctx, target, uncovered, nil, err)
	}
	return nil
}

// callerVisibleNamespaces keeps the namespaces the caller may read. It fails
// closed: when the caller's access cannot be resolved, nothing is kept.
func callerVisibleNamespaces(ctx context.Context, namespaces []string) []string {
	visible, err := resourceReadNamespaces(ctx, nil)
	if err != nil {
		return nil
	}
	if visible == nil {
		return namespaces
	}
	var out []string
	for _, ns := range namespaces {
		if slices.Contains(visible, ns) {
			out = append(out, ns)
		}
	}
	return out
}

// Dynamic reads start informers on demand. Check their scope after the read so
// a cold or incomplete store cannot establish absence. The bool reports that
// the scope is now synced, so a read that may have preceded the sync can be
// repeated. Direct API reads need no informer and remain authoritative without
// one.
func checkDynamicResourceRead(ctx context.Context, kind, group, namespace, verb string, readErr error) (bool, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	gvr, ok := dynamicReadGVR(kind, group)
	return checkDynamicResourceReadFrom(ctx, kind, group, namespace, verb, readErr, dynamicCache, gvr, ok)
}

func checkDynamicResourceReadFrom(ctx context.Context, kind, group, namespace, verb string, readErr error, dynamicCache *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, ok bool) (bool, error) {
	var classified *readError
	if errors.As(readErr, &classified) {
		return false, readErr
	}
	if errors.Is(readErr, k8s.ErrUnknownDynamicKind) {
		return false, errKindUnknown(kind, group, readErr)
	}
	if errors.Is(readErr, k8s.ErrDynamicNotReady) {
		return false, errDynamicUnavailable(kind, readErr)
	}
	direct := ok && directRead(gvr, verb)
	if apierrors.IsForbidden(readErr) && ok && dynamicCache != nil && !direct && namespace != "" {
		var covered []string
		if observation := dynamicCache.Observation(gvr); observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces {
			covered = observation.Namespaces
		}
		return false, coverageError(ctx, dynamicCollectorTarget(kind, group, verb), []string{namespace}, covered, readErr)
	}
	if err := collectorAuthError(ctx, readErr, dynamicCollectorTarget(kind, group, verb), namespace); err != nil {
		return false, err
	}
	if readErr != nil && !isObjectNotFound(readErr) {
		// A kind discovery lists without watch fails the read before any
		// informer exists; the observation says so without parsing the error.
		if ok && dynamicCache != nil && !direct && dynamicCache.Observation(gvr).State == k8score.DynamicObservationUnsupported {
			return false, readErrorf(readreason.KindNotWatchable, "Radar can't collect %s: its API doesn't support both list and watch, which Radar needs. Retrying won't help; read it directly from the Kubernetes API instead.", kind)
		}
		return false, readErrorf(readreason.ReadError, "Radar failed to %s %s, so it can't say what exists (%w).", verb, kind, readErr)
	}
	if !ok || dynamicCache == nil || direct {
		return false, readErr
	}
	if readErr == nil || isObjectNotFound(readErr) {
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			_, waitErr := dynamicCache.ListBlocking(gvr, namespace, dynamicSyncWait)
			if waitErr != nil {
				return checkDynamicResourceReadFrom(ctx, kind, group, namespace, verb, waitErr, dynamicCache, gvr, ok)
			}
		}
	}
	observation := dynamicCache.Observation(gvr)
	switch observation.State {
	case k8score.DynamicObservationSyncing:
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			// Stalled is this namespace's informer, not the oldest one any
			// request started for the kind.
			if dynamicCache.NamespaceObservation(gvr, namespace).ReasonCode == "sync_stalled" {
				return false, errKindLoadFailed(kind)
			}
			return false, errKindLoading(kind)
		}
	case k8score.DynamicObservationSynced:
		if namespace != "" && !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			return false, readErrorf(readreason.RadarNamespaceNotCollected, "Radar doesn't collect %s in namespace %q, so it can't say what exists there. %s.%s", kind, namespace, upperFirst(collectionRemedy(kind)), notYourAccess(ctx))
		}
	case k8score.DynamicObservationUnsupported:
		return false, readErrorf(readreason.KindNotWatchable, "Radar can't collect %s: its API doesn't support both list and watch, which Radar needs. Retrying won't help; read it directly from the Kubernetes API instead.", kind)
	}
	return dynamicCache.IsNamespaceSynced(gvr, namespace), readErr
}

// dynamicSyncWait bounds a read's wait for a cold informer. A variable so tests
// can wait less than the production three seconds.
var dynamicSyncWait = 3 * time.Second

func discoveredGVR(kind, group string) (schema.GroupVersionResource, bool) {
	return gvrIn(k8s.GetResourceDiscovery(), kind, group)
}

func gvrIn(discovery *k8s.ResourceDiscovery, kind, group string) (schema.GroupVersionResource, bool) {
	if group != "" {
		return discovery.GetGVRWithGroup(kind, group)
	}
	return discovery.GetGVR(kind)
}

// refreshDiscoveryForKind refreshes discovery when it can't resolve kind, so
// a custom resource installed since the last refresh is read rather than
// reported as kind_unknown.
func refreshDiscoveryForKind(kind, group string) {
	if _, ok := dynamicReadGVR(kind, group); ok {
		return
	}
	k8s.GetResourceDiscovery().LookupRefreshingOnMiss(func(d *k8s.ResourceDiscovery) (schema.GroupVersionResource, bool) {
		return gvrIn(d, kind, group)
	})
}

// errKindUnknown reports a kind discovery doesn't resolve. Discovery that
// failed can't rule a kind out: when the named group failed, or the last
// refresh returned nothing so the kinds held are out of date, it is a failed
// read to retry; when an unnamed kind may be in a group that failed, the
// answer says so.
func errKindUnknown(kind, group string, cause error) error {
	why := ""
	if cause != nil {
		why = fmt.Sprintf(" (%v)", cause)
	}
	if d := k8s.GetResourceDiscovery(); d != nil && d.ResourceDiscovery != nil {
		if group != "" && d.GroupHadPartialDiscovery(group) {
			return readErrorf(readreason.ReadError, "Kubernetes didn't answer discovery for API group %q, so Radar can't resolve %q in it and read nothing%s.", group, kind, why)
		}
		if d.LastRefreshFailed() {
			return readErrorf(readreason.ReadError, "Radar's last discovery refresh failed, so it can't tell whether a resource kind named %q exists here and read nothing%s.", kind, why)
		}
		if group == "" && d.HasPartialDiscovery() {
			return readErrorf(readreason.KindUnknown, "Radar doesn't know a resource kind named %q in this cluster, so it read nothing%s. Discovery of some API groups failed, so it may exist behind an API that isn't answering.", kind, why)
		}
	}
	return readErrorf(readreason.KindUnknown, "Radar doesn't know a resource kind named %q in this cluster, so it read nothing%s.", kind, why)
}

// errDynamicUnavailable reports a read that needs API discovery or the
// dynamic cache while Radar has neither: still connecting, or they failed to
// start.
func errDynamicUnavailable(kind string, cause error) error {
	if k8s.GetConnectionStatus().State == k8s.StateConnecting {
		return readErrorf(readreason.KindSyncPending, "Radar is still connecting to the cluster, so it can't read %s yet (%w).", kind, cause)
	}
	return readErrorf(readreason.ReadError, "Radar's support for this cluster's API resources didn't start, so it can't read %s (%w).", kind, cause)
}

func dynamicReadGVR(kind, group string) (schema.GroupVersionResource, bool) {
	if gvr, ok := discoveredGVR(kind, group); ok {
		return gvr, true
	}
	gvr, ok := k8s.BuiltinGVRAnyGroup(kind)
	return gvr, ok && (group == "" || group == gvr.Group)
}

// readDynamicScope performs one dynamic-cache read and checks its scope. A
// read stands only when the same synced informers served the scope before and
// after it. Starting a cluster-wide informer replaces a kind's namespace
// informers inside the same cache, so a read can land on a replacement's
// still-empty store while readiness checks on either side of it both pass. A
// read that doesn't stand is repeated once its scope is ready; if the
// informers change again during the repeat, the kind is reported as loading.
// A read that stands is not repeated: that would only deep-copy the store
// again.
func readDynamicScope[T any](ctx context.Context, kind, group, namespace, verb string, read func() (T, error)) (T, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	discovery := k8s.GetResourceDiscovery()
	cache := k8s.GetResourceCache()
	gvr, ok := dynamicReadGVR(kind, group)
	current := func() bool {
		return dynamicCache == k8s.GetDynamicResourceCache() && discovery == k8s.GetResourceDiscovery() && cache == k8s.GetResourceCache()
	}
	// bracketed reads once and reports whether the same synced informers
	// served the scope throughout.
	bracketed := func() (T, bool, error) {
		if !ok || dynamicCache == nil {
			out, err := read()
			return out, false, err
		}
		before, syncedBefore := dynamicCache.ReadSource(gvr, namespace)
		out, err := read()
		after, syncedAfter := dynamicCache.ReadSource(gvr, namespace)
		return out, syncedBefore && syncedAfter && before.Same(after), err
	}
	out, settled, err := bracketed()
	if !current() {
		return out, errConnectionChanged()
	}
	ready, err := checkDynamicResourceReadFrom(ctx, kind, group, namespace, verb, err, dynamicCache, gvr, ok)
	if ready && !settled && (err == nil || isObjectNotFound(err)) {
		out, settled, err = bracketed()
		if err != nil {
			_, err = checkDynamicResourceReadFrom(ctx, kind, group, namespace, verb, err, dynamicCache, gvr, ok)
		}
		if !settled && (err == nil || isObjectNotFound(err)) {
			// A cache stopped by a context switch never settles; say so rather
			// than that the kind is loading.
			if !current() {
				return out, errConnectionChanged()
			}
			var none T
			return none, errKindLoading(kind)
		}
	}
	if !current() {
		return out, errConnectionChanged()
	}
	return out, err
}

func isObjectNotFound(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound)
}

// exactAccessResource names the resource, and its API group, that a read of
// kind needs the caller's own permission for, beyond seeing the namespace:
// Secrets, LimitRanges, and Trivy Operator reports, whose findings can quote
// a leaked credential. resource is "" for any other kind.
func exactAccessResource(kind, group string) (string, string) {
	if gvr, ok := k8s.TrivyReportGVR(kind, group); ok {
		return gvr.Group, gvr.Resource
	}
	if group != "" {
		return "", ""
	}
	switch kind {
	case "secrets", "limitranges":
		return "", kind
	}
	return "", ""
}

// narrowByExactAccess keeps the namespaces of target where the caller can
// list (or get) resource. Namespace access does not imply that access, so
// withheld namespaces are a gap within the target, not a narrowing of it. An
// unrestricted caller is checked cluster-wide.
func narrowByExactAccess(ctx context.Context, target []string, group, resource, verb string) ([]string, gapSet, error) {
	var gaps gapSet
	if target == nil {
		granted, checked := canReadInNamespaceDecision(ctx, group, resource, "", verb)
		if !granted {
			return nil, gaps, resourcePermissionErrorNext(ctx, checked, verb, group, resource, "",
				fmt.Sprintf("If you can %s %s in some namespaces, call list_resources again with namespace set to one of them.", verb, resource))
		}
		return nil, gaps, nil
	}
	allowed, denied, unchecked := partitionNamespacesByCanRead(ctx, group, resource, verb, target)
	if len(denied) > 0 {
		gaps.add(readreason.UserForbidden, resourcePermissionError(ctx, true, verb, group, resource, namespaceForError(denied)), denied...)
	}
	if len(unchecked) > 0 {
		gaps.add(readreason.UserPermissionCheckFailed, resourcePermissionError(ctx, false, verb, group, resource, namespaceForError(unchecked)), unchecked...)
	}
	if len(allowed) == 0 {
		if gaps.empty() {
			return nil, gaps, resourcePermissionError(ctx, true, verb, group, resource, namespaceForError(target))
		}
		return nil, gaps, gaps.failure()
	}
	return allowed, gaps, nil
}

// currentReadCache is the typed cache a read would use now. A handler
// captures it before authorizing and checks the cache it reads against it,
// so authorization and rows come from the same connection.
func currentReadCache() *k8s.ResourceCache {
	active, syncing := k8s.SnapshotCaches()
	if active != nil {
		return active
	}
	return syncing
}

func resourceKindName(kind, group string) string {
	if gvr, ok := k8s.BuiltinGVRAnyGroup(kind); ok && (group == "" || group == gvr.Group) {
		return gvr.Resource
	}
	return kind
}

// resourceGetError reports a get's failure. A miss is an answer only because
// callers reach this after establishing that the namespace is readable.
func resourceGetError(ctx context.Context, err error, kind, namespace, name string) error {
	if isObjectNotFound(err) {
		if gvr, ok := dynamicReadGVR(kind, ""); ok {
			kind = gvr.Resource
		}
		msg := fmt.Sprintf("No %s named %q exist in the cluster.", kind, name)
		if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, ""); !clusterScoped && namespace != "" {
			msg = fmt.Sprintf("No %s named %q exist in namespace %q.", kind, name, namespace)
		}
		return &readError{code: readreason.ObjectNotFound, err: errors.New(msg), next: notFoundNext(ctx, kind, namespace, name)}
	}
	return readErrorf(readreason.ReadError, "Radar failed to get %s %s/%s, so it can't say whether it exists (%w).", kind, namespace, name, err)
}

func fetchMCPDynamicResource(ctx context.Context, cache *k8s.ResourceCache, kind, group, namespace, name string) (*unstructured.Unstructured, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	discovery := k8s.GetResourceDiscovery()
	gvr, known := dynamicReadGVR(kind, group)
	current := func() bool {
		return cache == k8s.GetResourceCache() && dynamicCache == k8s.GetDynamicResourceCache() && discovery == k8s.GetResourceDiscovery()
	}
	if !current() {
		return nil, errConnectionChanged()
	}
	obj, err := readDynamicScope(ctx, kind, group, namespace, "get", func() (*unstructured.Unstructured, error) {
		if !current() {
			return nil, errConnectionChanged()
		}
		if known && dynamicCache != nil && !k8s.TypedKindOwnsGroup(kind, group) {
			if directRead(gvr, "get") {
				return dynamicCache.GetDirect(ctx, gvr, namespace, name)
			}
			return dynamicCache.Get(gvr, namespace, name)
		}
		return cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
	})
	if !current() {
		return nil, errConnectionChanged()
	}
	if isObjectNotFound(err) {
		return obj, resourceGetError(ctx, err, kind, namespace, name)
	}
	return obj, err
}

// recordedCredentialRejections returns the namespaces of the request (nil:
// all) whose access probe Kubernetes answered with a 401 when this cache was
// built, sorted, and the first one's error.
func recordedCredentialRejections(result *k8s.PermissionCheckResult, resource string, namespaces []string) ([]string, error) {
	if result == nil {
		return nil, nil
	}
	errs := map[string]error{}
	for ns, probeErr := range result.NamespaceProbeErrors[resource] {
		if apierrors.IsUnauthorized(probeErr) && (namespaces == nil || slices.Contains(namespaces, ns)) {
			errs[ns] = probeErr
		}
	}
	rejected := slices.Sorted(maps.Keys(errs))
	if len(rejected) == 0 {
		return nil, nil
	}
	return rejected, errs[rejected[0]]
}

// errRecordedCredentialsRejected reports a 401 Radar recorded when it built
// its cache, in namespaces (nil: for the kind as a whole). The cache keeps
// that result until it is rebuilt, as a reconnect does, so retrying the
// request alone changes nothing.
func errRecordedCredentialsRejected(kind string, namespaces []string, err error) error {
	where := ""
	if len(namespaces) > 0 {
		where = " in " + namespacePhrase(resourceapi.Names(namespaces, resourceapi.MCPSetLimit))
	}
	return &readError{code: readreason.RadarCredentialsRejected, err: &collectorError{
		msg:   fmt.Sprintf("Kubernetes rejected Radar's own %s when Radar connected and checked its access to %s%s, so Radar doesn't collect them. %s", radarCredentials(), kind, where, reconnectRemedy),
		cause: err,
	}}
}

// reconnectRemedy is the remedy for a credential rejection Radar recorded when
// it built its cache: a retry of the request reads the same recorded result.
const reconnectRemedy = "Whoever runs Radar must check those credentials, then reconnect Radar (POST /api/connection/retry); retrying the request alone won't help."

func radarCredentials() string {
	if k8s.IsInCluster() {
		return "service-account credentials"
	}
	return "kubeconfig credentials"
}

func collectorUnauthorizedError(kind string, err error) error {
	return &readError{code: readreason.RadarCredentialsRejected, err: &collectorError{
		msg:   fmt.Sprintf("Kubernetes rejected Radar's own %s while it read %s, so Radar read nothing. Whoever runs Radar must refresh them, then retry.", radarCredentials(), kind),
		cause: err,
	}}
}
