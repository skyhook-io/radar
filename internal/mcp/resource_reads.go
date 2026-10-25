package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
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
	return resolveReadTargetFrom(ctx, currentReadCache(), requested)
}

func resolveReadTargetFrom(ctx context.Context, cache *k8s.ResourceCache, requested []string) (readTarget, error) {
	target := readTarget{explicit: requested != nil}
	user, perms, checked := resolveUserPermsDecision(ctx)
	ceiling := pkgauth.FilterNamespacesForUser(nil, user, perms)
	var pin *string
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		pin = &target
	}
	var missing []string
	if checked && ceiling == nil && requested != nil {
		missing = cache.KnownMissingNamespaces(requested)
	}
	selection, code := readscope.ResolveNamespaces(requested, readscope.NamespaceAccess{Namespaces: ceiling, Checked: checked}, pin, missing)
	if currentReadCache() != cache {
		return target, errConnectionChanged()
	}
	if code == readreason.UserPermissionCheckFailed {
		return target, readErrorf(code, "Radar couldn't check which namespaces you can access, so it read nothing.")
	}
	if code == readreason.UserNoNamespaceAccess {
		return target, readErrorf(code, "You can't access any namespace in this request's scope, so Radar read nothing. %s %s", withCloudRole(ctx, "A cluster admin can grant access"), namespaceAccessRule)
	}
	if len(selection.Gaps) > 0 {
		gap := selection.Gaps[0]
		switch gap.Code {
		case readreason.NamespaceNotFound:
			scope := fmt.Sprintf("Namespace %q doesn't exist in this cluster", gap.Namespaces[0])
			if len(gap.Namespaces) > 1 {
				scope = fmt.Sprintf("Namespaces %q don't exist in this cluster", namespaceForError(gap.Namespaces))
			}
			return target, &readError{code: gap.Code, err: fmt.Errorf("%s, so there is nothing to read there.", scope), next: "Call list_namespaces to see the namespaces that exist."}
		case readreason.OutsideNamespacePin:
			return target, readErrorf(gap.Code, "Radar is limited to other namespaces by its --namespace-scope setting, so it can't read namespace %q. Whoever runs Radar sets --namespace-scope.", namespaceForError(gap.Namespaces))
		case readreason.UserNamespaceNotVisible:
			scope := fmt.Sprintf("Namespace %q doesn't exist or you can't access it", gap.Namespaces[0])
			if len(gap.Namespaces) > 1 {
				scope = fmt.Sprintf("Namespaces %q don't exist or you can't access them", namespaceForError(gap.Namespaces))
			}
			err := &readError{code: gap.Code, err: fmt.Errorf("%s, so Radar can't say what's in it. If the name is right, %s %s", scope, withCloudRole(ctx, "a cluster admin can grant access"), namespaceAccessRule)}
			if cache := k8s.GetResourceCache(); cache != nil && cache.Namespaces() != nil {
				err.next = "Call list_namespaces to see the namespaces you can access."
			}
			return target, err
		}
	}
	target.namespaces, target.narrowedBy = selection.Namespaces, selection.NarrowedBy
	return target, nil
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
	t.namespaces = (readplan.CollectionScope{Namespaces: collected, Outside: uncovered}).Narrow(t.namespaces)
	if !slices.Contains(t.narrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		t.narrowedBy = append(slices.Clone(t.narrowedBy), resourceapi.NarrowedByNamespaceCollection)
	}
	return t
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
	namespaces    map[readreason.Code][]string
	unknown       map[readreason.Code]bool
	unknownExtent map[readreason.Code]bool
	firstErr      map[readreason.Code]error
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
		if other.unknownExtent[code] {
			if g.unknownExtent == nil {
				g.unknownExtent = map[readreason.Code]bool{}
			}
			g.unknownExtent[code] = true
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
		case g.unknownExtent[code]:
			gap.Namespaces = resourceapi.UnknownNames()
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
	// wholeKindReady: the kind is ready as a whole, so its listers serve the read. A
	// kind still loading elsewhere is read from its namespaces' own stores.
	wholeKindReady bool
	gaps           gapSet
	// collected, when non-nil, is the part of an implicit target Radar
	// collects: the read was narrowed to it. uncovered is what it left out of
	// a finite target.
	collected, uncovered []string
}

// planResourceInventory returns the cache to read kind from and, for a typed kind,
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
func planResourceInventory(ctx context.Context, kind, group string, namespaces []string, explicit bool) (*k8s.ResourceCache, *typedRead, error) {
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
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	facts := readplan.Typed(cache, result, gvr.Resource, namespaces, clusterScoped)
	inventory := readplan.ForInventory(facts, namespaces, explicit, clusterScoped)
	plan := &typedRead{resource: gvr.Resource, wholeKindReady: inventory.WholeKindReady, read: inventory.Namespaces, all: inventory.All}
	if inventory.Collection != nil {
		plan.collected, plan.uncovered = inventory.Collection.Namespaces, inventory.Collection.Outside
	}
	target := collectorTarget{resource: gvr.Resource, group: gvr.Group, action: "list/watch"}
	unavailable := cache.KindReadinessFor(gvr.Resource) == k8s.KindUnavailable
	for _, part := range inventory.Gaps {
		if part.Verdict.State == readplan.OutsideCollection {
			plan.gaps.add(part.Verdict.Code, coverageError(ctx, target, part.Namespaces, cache.KindNamespaces(gvr.Resource), nil), part.Namespaces...)
			continue
		}
		var err error
		switch part.Verdict.Code {
		case readreason.RadarCredentialsRejected:
			rejected := result.CredentialRejectedNamespaces(gvr.Resource, namespaces)
			var cause error
			if len(rejected) > 0 {
				cause = result.NamespaceProbeErrors[gvr.Resource][rejected[0]]
			} else if result != nil {
				cause = result.ProbeErrors[gvr.Resource]
			}
			if !unavailable {
				rejected = part.Namespaces
			}
			err = errRecordedCredentialsRejected(gvr.Resource, rejected, cause)
		case readreason.RadarForbidden:
			where := " in the namespaces you need"
			if clusterScoped {
				where = ""
			}
			name := qualifiedResource(gvr.Group, gvr.Resource)
			err = readErrorf(part.Verdict.Code, "Kubernetes doesn't allow %s to list/watch %s, so Radar has none to show and can't say which exist. %s.%s", collectorName(), name, upperFirst(identityRemedy(name, where, "list/watch")), notYourAccess(ctx))
		case readreason.RadarKindNotCollected:
			err = errKindNotCollected(qualifiedResource(gvr.Group, gvr.Resource))
		case readreason.KindSyncTimedOut:
			err = errKindLoadFailed(gvr.Resource)
		default:
			err = errKindLoading(gvr.Resource)
		}
		if unavailable {
			return nil, nil, err
		}
		plan.gaps.add(part.Verdict.Code, err, part.Namespaces...)
	}
	if inventory.Collection != nil && len(plan.collected) == 0 {
		plan.gaps.add(readreason.RadarNamespaceNotCollected, coverageError(ctx, target, plan.uncovered, cache.KindNamespaces(gvr.Resource), nil))
	}
	if !inventory.CanReadTarget {
		if len(facts.Partitions) == 1 && facts.Partitions[0].Extent != readplan.NamespaceSet {
			return nil, nil, plan.gaps.failure()
		}
		return nil, plan, plan.gaps.failure()
	}
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
	code, _ := readplan.CollectorErrorCode(err)
	switch code {
	case readreason.RadarCredentialsRejected:
		return collectorUnauthorizedError(target.resource, err)
	case readreason.RadarForbidden:
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
	code, _ := readplan.CollectorErrorCode(readErr)
	switch code {
	case readreason.KindUnknown:
		return false, errKindUnknown(kind, group, readErr)
	case readreason.KindSyncPending:
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
		if ok && dynamicCache != nil && !direct {
			var target []string
			if namespace != "" {
				target = []string{namespace}
			}
			clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
			facts := readplan.Dynamic(dynamicCache, gvr, target, clusterScoped)
			for _, part := range facts.Partitions {
				switch part.Verdict.Code {
				case readreason.KindSyncPending:
					if !dynamicCache.Observation(gvr).ProbeRetrying {
						continue
					}
					return false, dynamicPlanError(ctx, kind, group, namespace, verb, part.Verdict.Code)
				case readreason.KindNotWatchable, readreason.ConnectionChanged, readreason.RadarCredentialsRejected:
					return false, dynamicPlanError(ctx, kind, group, namespace, verb, part.Verdict.Code)
				}
			}
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
	var target []string
	if namespace != "" {
		target = []string{namespace}
	}
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	facts := readplan.Dynamic(dynamicCache, gvr, target, clusterScoped)
	for _, part := range facts.Partitions {
		if part.Verdict.State != readplan.Readable {
			return false, dynamicPlanError(ctx, kind, group, namespace, verb, part.Verdict.Code)
		}
	}
	return dynamicCache.IsNamespaceSynced(gvr, namespace), readErr
}

func dynamicPlanError(ctx context.Context, kind, group, namespace, verb string, code readreason.Code) error {
	switch code {
	case readreason.ConnectionChanged:
		return errConnectionChanged()
	case readreason.KindSyncPending:
		return errKindLoading(kind)
	case readreason.KindSyncTimedOut:
		return errKindLoadFailed(kind)
	case readreason.KindNotWatchable:
		return readErrorf(code, "Radar can't collect %s: its API doesn't support both list and watch, which Radar needs. Retrying won't help; read it directly from the Kubernetes API instead.", kind)
	case readreason.RadarNamespaceNotCollected:
		return readErrorf(code, "Radar doesn't collect %s in namespace %q, so it can't say what exists there. %s.%s", kind, namespace, upperFirst(collectionRemedy(kind)), notYourAccess(ctx))
	case readreason.RadarForbidden:
		return readErrorf(code, "Radar's collector cannot read %s in the requested scope.", kind)
	case readreason.RadarCredentialsRejected:
		return collectorUnauthorizedError(kind, nil)
	case readreason.RadarKindNotCollected:
		return errKindNotCollected(kind)
	default:
		return readErrorf(code, "Radar failed to %s %s, so it can't say what exists.", verb, kind)
	}
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
// A get that found its object stands without either check.
// A read that stands is not repeated: that would only deep-copy the store
// again.
func readDynamicScope[T any](ctx context.Context, kind, group, namespace, verb string, read func() (T, error)) (T, error) {
	dynamicCache := k8s.GetDynamicResourceCache()
	discovery := k8s.GetResourceDiscovery()
	cache := k8s.GetResourceCache()
	gvr, ok := dynamicReadGVR(kind, group)
	current := func() bool {
		return dynamicCache == k8s.GetDynamicResourceCache() && discovery == k8s.GetResourceDiscovery() && cache == k8s.GetResourceCache() && (dynamicCache == nil || !dynamicCache.IsStopped())
	}
	// bracketed reads once and reports whether the same synced informers
	// served the scope throughout.
	bracketed := func() (T, bool, error) {
		if !ok || dynamicCache == nil {
			out, err := read()
			return out, false, err
		}
		return k8score.ReadWithSourceCheck(dynamicCache.DynamicResourceCache, gvr, namespace, read)
	}
	out, settled, err := bracketed()
	if !current() {
		return out, errConnectionChanged()
	}
	// A found object exists whatever its store's load state, so a get that
	// found it stands. Only a miss needs a complete store to mean absence.
	if verb == "get" && err == nil && ok && dynamicCache != nil {
		return out, nil
	}
	ready, err := checkDynamicResourceReadFrom(ctx, kind, group, namespace, verb, err, dynamicCache, gvr, ok)
	if ready && !settled && (err == nil || isObjectNotFound(err)) {
		out, settled, err = bracketed()
		if !current() {
			return out, errConnectionChanged()
		}
		if verb == "get" && err == nil && ok && dynamicCache != nil {
			return out, nil
		}
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
