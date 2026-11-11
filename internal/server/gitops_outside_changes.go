package server

// The outside-changes endpoint lists, for a Flux Kustomization or
// HelmRelease, which fields of the objects it manages were last written by a
// person's tool (kubectl, Lens, Radar…) rather than by Flux. The evidence is
// metadata.managedFields, which Radar's caches strip, so the members are
// listed live, as the caller, by the root's Flux labels. Only manager names,
// times, field paths and an allowlist of scalar values leave the server, and
// Secrets are never listed: a LIST would return their data.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/pkg/gitops/fieldwriters"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const maxOutsideListGroups = 40

type outsideRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type outsideChange struct {
	Manager   string            `json:"manager"`
	Tool      string            `json:"tool"`
	Operation string            `json:"operation"`
	Time      string            `json:"time,omitempty"`
	Paths     []string          `json:"paths"`
	MorePaths int               `json:"morePaths"`
	Values    map[string]string `json:"values,omitempty"`
	Outcome   string            `json:"outcome"`
	Harmless  bool              `json:"harmless"`
}

type outsideObject struct {
	Ref     outsideRef      `json:"ref"`
	Changes []outsideChange `json:"changes"`
}

type outsideToolCount struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

type outsideOtherWriter struct {
	Manager string `json:"manager"`
	Objects int    `json:"objects"`
}

type outsideDenied struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
}

type outsideCoverage struct {
	Listed int             `json:"listed"`
	Denied []outsideDenied `json:"denied,omitempty"`
	// Failed: the LIST errored for a reason other than RBAC (removed CRD,
	// server error, timeout).
	Failed     []outsideDenied `json:"failed,omitempty"`
	Skipped    []string        `json:"skipped,omitempty"`
	NotChecked []string        `json:"notChecked,omitempty"`
	// LabelScan: the release records no inventory, so a fixed set of common
	// kinds was listed by Flux's labels; other kinds weren't checked.
	LabelScan bool `json:"labelScan,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
	timedOut  bool
}

// outsideScanComplete: an answer worth keeping for the next request. A list
// that errored or a scan that ran out of time may succeed on a retry; a
// denial or a hit object bound is the same answer next time.
func outsideScanComplete(cov outsideCoverage) bool {
	return len(cov.Failed) == 0 && !cov.timedOut
}

type outsidePolicy struct {
	Tool           string `json:"tool"`
	DriftDetection string `json:"driftDetection,omitempty"`
	Suspended      bool   `json:"suspended"`
	Interval       string `json:"interval,omitempty"`
}

type outsideChangesResponse struct {
	Root         outsideRef                    `json:"root"`
	Policy       *outsidePolicy                `json:"policy,omitempty"`
	Count        int                           `json:"count"`
	Objects      []outsideObject               `json:"objects"`
	Harmless     []outsideToolCount            `json:"harmless"`
	OtherWriters []outsideOtherWriter          `json:"otherWriters"`
	DriftEvents  []gitopsinsights.EventSummary `json:"driftEvents"`
	Coverage     outsideCoverage               `json:"coverage"`
}

// classifyOutsideObject reads one member's managedFields. Subresource writes
// (status, scale) are controllers', never a hand edit.
func classifyOutsideObject(obj *unstructured.Unstructured, owner *topology.ResourceRef, ownerObj *unstructured.Unstructured) (out outsideObject, harmless []string, otherManagers []string) {
	group := resourceid.GroupFromAPIVersion(obj.GetAPIVersion())
	out.Ref = outsideRef{Group: group, Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	ref := writeEvidenceRef{Kind: obj.GetKind(), Group: group, Namespace: obj.GetNamespace(), Name: obj.GetName()}
	owners := ownerManagers(owner)
	var policy *gitOpsWritePolicy
	if ownerObj != nil {
		policy = readGitOpsWritePolicy(owner, ownerObj, obj)
	}
	entries := obj.GetManagedFields()
	entryPaths := make([][]string, len(entries))
	for i, mf := range entries {
		if mf.FieldsV1 != nil {
			entryPaths[i], _ = fieldwriters.LeafPaths(mf.FieldsV1.Raw)
		}
	}
	for i, mf := range entries {
		if mf.Subresource != "" {
			continue
		}
		switch fieldwriters.Classify(mf.Manager, owners, radarWriteManagers) {
		case fieldwriters.ClassOwner:
			continue
		case fieldwriters.ClassOther:
			otherManagers = append(otherManagers, mf.Manager)
			continue
		}
		kept, label := fieldwriters.SplitHarmless(ownedValuePaths(entryPaths, i))
		if label != "" {
			harmless = append(harmless, label)
		}
		if len(kept) == 0 {
			continue
		}
		// Each path is checked against the drift-ignore rules before the display
		// cap, and covered paths get their own row: one entry can mix both.
		var covered, uncovered []string
		for _, p := range kept {
			segs, err := parseWriteFieldPath(p)
			if err == nil && policy != nil {
				if c, _ := ignoreRuleFor(owner, ownerObj, policy, obj, ref, segs); c == "effective" {
					covered = append(covered, p)
					continue
				}
			}
			uncovered = append(uncovered, p)
		}
		for _, group := range []struct {
			paths  []string
			ignore string
		}{{uncovered, ""}, {covered, "effective"}} {
			if len(group.paths) == 0 {
				continue
			}
			change := outsideChange{Manager: mf.Manager, Tool: fieldwriters.ToolName(mf.Manager, radarWriteManagers), Operation: string(mf.Operation), Outcome: outsideOutcome(policy, group.ignore)}
			if mf.Time != nil {
				change.Time = mf.Time.UTC().Format(time.RFC3339)
			}
			shown := group.paths
			if len(shown) > fieldwriters.MaxPaths {
				change.MorePaths = len(shown) - fieldwriters.MaxPaths
				shown = shown[:fieldwriters.MaxPaths]
			}
			change.Paths = shown
			for _, p := range shown {
				if v, ok := outsideValue(obj.Object, p); ok {
					if change.Values == nil {
						change.Values = map[string]string{}
					}
					change.Values[p] = v
				}
			}
			out.Changes = append(out.Changes, change)
		}
	}
	return out, harmless, otherManagers
}

// outsideOutcome is what the root's policy says Flux does about a hand edit.
// The UI owns the sentence for each code.
func outsideOutcome(policy *gitOpsWritePolicy, ignore string) string {
	switch {
	case policy == nil:
		return "unknown"
	case policy.Suspended != nil && *policy.Suspended:
		return "suspended"
	case policy.ObjectReconcile != "" || ignore == "effective":
		return "ignored"
	case policy.DriftDetection == "":
		return "may-overwrite"
	case policy.DriftDetection == "enabled":
		return "reverted"
	case policy.DriftDetection == "warn":
		return "reported"
	}
	return "stays"
}

// outsideValue returns the live value of a path only when it is a scalar
// on the allowlist: replicas, container images and resource requests and
// limits. Env values, labels, annotations and data never leave the server.
func outsideValue(obj map[string]any, path string) (string, bool) {
	segs, err := parseWriteFieldPath(path)
	if err != nil || !outsideValueAllowed(segs) {
		return "", false
	}
	node, ok := liveNode(obj, segs)
	if !ok {
		return "", false
	}
	switch v := node.(type) {
	case string:
		return v, true
	case int64, int32, int, float64, bool:
		return fmt.Sprint(v), true
	}
	return "", false
}

// outsideValueAllowed matches parsed segments, so a quoted map key such as
// annotations["team.resources.limits.note"] never passes for a resource path.
func outsideValueAllowed(segs []fieldPathSegment) bool {
	key := func(i int, names ...string) bool {
		if i < 0 || i >= len(segs) || segs[i].typ != segKey {
			return false
		}
		for _, n := range names {
			if segs[i].key == n {
				return true
			}
		}
		return false
	}
	n := len(segs)
	if n == 2 && key(0, "spec") && key(1, "replicas") {
		return true
	}
	// …containers[sel] is the anchor; everything before it must be plain keys.
	anchor := -1
	for i, seg := range segs {
		if seg.typ == segMatch && key(i-1, "containers", "initContainers") {
			anchor = i
			break
		}
		if seg.typ != segKey {
			return false
		}
	}
	if anchor < 0 {
		return false
	}
	rest := n - anchor - 1
	switch {
	case rest == 1:
		return key(anchor+1, "image")
	case rest == 3:
		return key(anchor+1, "resources") && key(anchor+2, "requests", "limits") && segs[anchor+3].typ == segKey
	}
	return false
}

// ownedValuePaths drops a path another entry owns fields inside: this entry
// then holds only the emptied container (Kubernetes keeps it after another
// manager takes over every key), which changes no value. An atomic field
// (args, a Service selector, Ingress rules) has a single owner of its whole
// value, so it stays.
func ownedValuePaths(entryPaths [][]string, self int) []string {
	var out []string
	for _, p := range entryPaths[self] {
		container := false
		for j, others := range entryPaths {
			if j == self {
				continue
			}
			for _, o := range others {
				if strings.HasPrefix(o, p+".") || strings.HasPrefix(o, p+"[") {
					container = true
					break
				}
			}
			if container {
				break
			}
		}
		if !container {
			out = append(out, p)
		}
	}
	return out
}

func liveNode(obj map[string]any, segs []fieldPathSegment) (any, bool) {
	var node any = obj
	for _, seg := range segs {
		switch seg.typ {
		case segKey:
			m, ok := node.(map[string]any)
			if !ok {
				return nil, false
			}
			node = m[seg.key]
		case segMatch:
			list, ok := node.([]any)
			if !ok {
				return nil, false
			}
			node = nil
			for _, item := range list {
				if matchesSelector(item, seg) {
					node = item
					break
				}
			}
		default:
			return nil, false
		}
	}
	return node, node != nil
}

type outsideListGroup struct {
	GVR       schema.GroupVersionResource
	Kind      string
	Namespace string
	// Members limits the LIST result to the tree's members by name; nil for a
	// label scan, where the labels are all Radar has.
	Members map[string]bool
}

// Kinds a HelmRelease without inventory is checked for, by Flux's labels.
var outsideLabelScanKinds = []struct{ Group, Kind string }{
	{"apps", "Deployment"}, {"apps", "StatefulSet"}, {"apps", "DaemonSet"}, {"", "Service"},
	{"", "ConfigMap"}, {"networking.k8s.io", "Ingress"}, {"batch", "CronJob"}, {"batch", "Job"},
	{"", "ServiceAccount"}, {"rbac.authorization.k8s.io", "Role"}, {"rbac.authorization.k8s.io", "RoleBinding"},
	{"", "PersistentVolumeClaim"},
}

// outsideListGroups turns the tree's managed members into one LIST per
// (resource, namespace). Secrets are never listed.
func outsideListGroups(tree *gitopstree.ResourceTree, root *unstructured.Unstructured,
	resolve func(group, kind string) (schema.GroupVersionResource, bool)) (groups []outsideListGroup, cov outsideCoverage) {
	index := map[string]int{}
	skipped := map[string]bool{}
	notChecked := map[string]bool{}
	add := func(group, kind, namespace, name string) {
		if group == "" && kind == "Secret" {
			skipped["secrets"] = true
			return
		}
		gvr, ok := resolve(group, kind)
		if !ok {
			notChecked[kind] = true
			return
		}
		key := gvr.String() + "\x00" + namespace
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			g := outsideListGroup{GVR: gvr, Kind: kind, Namespace: namespace}
			if name != "" {
				g.Members = map[string]bool{}
			}
			groups = append(groups, g)
		}
		if name == "" {
			groups[i].Members = nil
		} else if groups[i].Members != nil {
			groups[i].Members[name] = true
		}
	}
	if tree != nil {
		for _, n := range tree.Nodes {
			if n.Role != gitopstree.RoleDeclared || n.Dependency || n.Remote || n.Data["relationship"] != nil {
				continue
			}
			add(n.Ref.Group, n.Ref.Kind, n.Ref.Namespace, n.Ref.Name)
		}
	}
	if root.GetKind() == "HelmRelease" {
		// The tree reads members from status.inventory.entries; when that isn't
		// a readable list, the labels are the only way to find them.
		if _, readable, err := unstructured.NestedSlice(root.Object, "status", "inventory", "entries"); !readable || err != nil {
			namespace, _, _ := unstructured.NestedString(root.Object, "spec", "targetNamespace")
			if namespace == "" {
				namespace = root.GetNamespace()
			}
			for _, k := range outsideLabelScanKinds {
				add(k.Group, k.Kind, namespace, "")
			}
			cov.LabelScan = true
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.GVR.Resource != b.GVR.Resource {
			return a.GVR.Resource < b.GVR.Resource
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.GVR.Group < b.GVR.Group
	})
	if len(groups) > maxOutsideListGroups {
		groups = groups[:maxOutsideListGroups]
		cov.Truncated = true
	}
	cov.Skipped = sortedKeys(skipped)
	cov.NotChecked = sortedKeys(notChecked)
	return groups, cov
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fluxMemberSelector selects the objects Flux labels as the root's.
func fluxMemberSelector(root *unstructured.Unstructured) string {
	prefix := "kustomize.toolkit.fluxcd.io"
	if root.GetKind() == "HelmRelease" {
		prefix = "helm.toolkit.fluxcd.io"
	}
	return fmt.Sprintf("%s/name=%s,%s/namespace=%s", prefix, root.GetName(), prefix, root.GetNamespace())
}

type outsideLimits struct {
	Lists, Objects int
	Timeout        time.Duration
}

var defaultOutsideLimits = outsideLimits{Lists: maxOutsideListGroups, Objects: 2000, Timeout: 8 * time.Second}

// listOutsideObjects runs one LIST per group, as the caller, by the root's
// Flux labels. A denied group is recorded and skipped; hitting a bound stops
// the scan and marks the answer truncated.
func listOutsideObjects(ctx context.Context, dyn dynamic.Interface, groups []outsideListGroup, selector string, limits outsideLimits, cov *outsideCoverage) []*unstructured.Unstructured {
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	var out []*unstructured.Unstructured
	lists := 0
	for _, g := range groups {
		cont := ""
		for {
			if lists >= limits.Lists || len(out) >= limits.Objects {
				cov.Truncated = true
				return out
			}
			if ctx.Err() != nil {
				cov.Truncated, cov.timedOut = true, true
				return out
			}
			lists++
			// Ask for no more than the object budget left, so one crowded
			// namespace can't make the server send everything at once.
			list, err := dyn.Resource(g.GVR).Namespace(g.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: selector, Limit: int64(limits.Objects - len(out)), Continue: cont,
			})
			switch {
			case apierrors.IsForbidden(err):
				cov.Denied = append(cov.Denied, outsideDenied{Resource: g.GVR.Resource, Namespace: g.Namespace})
			case ctx.Err() != nil:
				cov.Truncated, cov.timedOut = true, true
				return out
			case err != nil:
				log.Printf("[gitops] Failed to list %s in %s for outside changes: %v", g.GVR.Resource, sanitizeForLog(g.Namespace), err)
				cov.Failed = append(cov.Failed, outsideDenied{Resource: g.GVR.Resource, Namespace: g.Namespace})
			}
			if err != nil {
				break
			}
			for j := range list.Items {
				item := &list.Items[j]
				if g.Members != nil && !g.Members[item.GetName()] {
					continue
				}
				if len(out) >= limits.Objects {
					cov.Truncated = true
					return out
				}
				out = append(out, item)
				cov.Listed++
			}
			if cont = list.GetContinue(); cont == "" {
				break
			}
		}
	}
	return out
}

// outsideChangesMemo keeps one answer per user and root for a short window:
// the header count and the tab ask for the same thing, and the answer costs
// up to 40 LISTs. The key carries the caller's identity, so one user's answer
// never serves another.
type outsideChangesMemo struct {
	ttl time.Duration
	mu  sync.Mutex
	// gen counts context switches. An answer built under an earlier
	// generation (the request resolved its tree on the previous cluster) is
	// never stored.
	gen     uint64
	entries map[string]*outsideMemoEntry
}

type outsideMemoEntry struct {
	build sync.Mutex // one scan per key at a time
	// guarded by outsideChangesMemo.mu
	busy      int
	expiresAt time.Time
	value     outsideChangesResponse
}

func newOutsideChangesMemo(ttl time.Duration) *outsideChangesMemo {
	return &outsideChangesMemo{ttl: ttl, entries: map[string]*outsideMemoEntry{}}
}

func (m *outsideChangesMemo) generation() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen
}

// loadOr returns the stored answer for key, or builds one (once per key at a
// time) and stores it only when build reports it cacheable and no context
// switch happened since gen was read.
func (m *outsideChangesMemo) loadOr(gen uint64, key string, build func() (outsideChangesResponse, bool)) outsideChangesResponse {
	if m == nil || m.ttl <= 0 {
		resp, _ := build()
		return resp
	}
	m.mu.Lock()
	if gen != m.gen {
		m.mu.Unlock()
		resp, _ := build()
		return resp
	}
	now := time.Now()
	for k, e := range m.entries {
		if e.busy == 0 && now.After(e.expiresAt) {
			delete(m.entries, k)
		}
	}
	e, ok := m.entries[key]
	if !ok {
		e = &outsideMemoEntry{}
		m.entries[key] = e
	}
	e.busy++
	m.mu.Unlock()

	e.build.Lock()
	defer e.build.Unlock()
	m.mu.Lock()
	if time.Now().Before(e.expiresAt) {
		value := e.value
		e.busy--
		m.mu.Unlock()
		return value
	}
	m.mu.Unlock()

	value, cacheable := build()
	m.mu.Lock()
	defer m.mu.Unlock()
	e.busy--
	current := m.entries[key] == e
	switch {
	case cacheable && current && gen == m.gen:
		e.value = value
		e.expiresAt = time.Now().Add(m.ttl)
	case current && e.busy == 0 && !time.Now().Before(e.expiresAt):
		delete(m.entries, key)
	}
	return value
}

func (m *outsideChangesMemo) clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.gen++
	m.entries = map[string]*outsideMemoEntry{}
	m.mu.Unlock()
}

// outsideMemoKey: the answer depends on who asks (identity and groups) and on
// the namespaces they may read now, as well as on the root's version.
func outsideMemoKey(user *auth.User, allowed []string, owner *topology.ResourceRef, resourceVersion string) string {
	return gitopsIssuesMemoKey(user, allowed) + "\x00" + owner.Group + "/" + owner.Kind + "/" + owner.Namespace + "/" + owner.Name + "\x00" + resourceVersion
}

// handleGitOpsOutsideChanges serves GET /api/gitops/outside-changes/{kind}/{namespace}/{name}
// for a Flux Kustomization or HelmRelease. Gate: the caller's namespace
// access (parseGitOpsRequest) and the per-user tree; the member LISTs are
// impersonated, so the API server enforces the caller's RBAC on each.
// Without authentication it reads with Radar's identity like every other
// read and returns only manager names, times, paths and allowlisted values.
func (s *Server) handleGitOpsOutsideChanges(w http.ResponseWriter, r *http.Request) {
	entry, err := gitops.ResolveFluxKind(chi.URLParam(r, "kind"))
	group := r.URL.Query().Get("group")
	if err != nil || (entry.Kind != "Kustomization" && entry.Kind != "HelmRelease") || (group != "" && group != entry.GVR.Group) {
		s.writeError(w, http.StatusBadRequest, "changes made outside Flux are only available for a Flux Kustomization or HelmRelease")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	gen := s.outsideChangesMemo.generation()
	req, ok := s.parseGitOpsRequest(w, r)
	if !ok {
		return
	}
	if !req.HasNamespaceAccess() {
		s.writeError(w, http.StatusForbidden, "no namespace access")
		return
	}
	tree, root, resolver, err := s.resolveGitOpsTree(r, req)
	if err != nil {
		s.writeGitOpsBuildError(w, req, err)
		return
	}
	owner := &topology.ResourceRef{Group: entry.GVR.Group, Kind: entry.Kind, Namespace: root.GetNamespace(), Name: root.GetName()}
	key := outsideMemoKey(auth.UserFromContext(r.Context()), req.AllowedNamespaces, owner, root.GetResourceVersion())
	dyn := s.getDynamicClientForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes client not available for this request")
		return
	}
	resp := s.outsideChangesMemo.loadOr(gen, key, func() (outsideChangesResponse, bool) {
		resp, complete, err := buildOutsideChanges(r.Context(), dyn, tree, root, owner, resolver)
		// An answer cut short (the caller left, a list failed, time ran out)
		// is served but not kept for the next request.
		return resp, err == nil && complete && r.Context().Err() == nil
	})
	s.writeJSON(w, resp)
}

func buildOutsideChanges(ctx context.Context, dyn dynamic.Interface, tree *gitopstree.ResourceTree, root *unstructured.Unstructured, owner *topology.ResourceRef, resolver *insightsResolver) (resp outsideChangesResponse, complete bool, err error) {
	if dyn == nil {
		return outsideChangesResponse{}, false, errOutsideNoClient
	}
	resp = outsideChangesResponse{
		Root:         outsideRef{Group: owner.Group, Kind: owner.Kind, Namespace: owner.Namespace, Name: owner.Name},
		Objects:      []outsideObject{},
		Harmless:     []outsideToolCount{},
		OtherWriters: []outsideOtherWriter{},
		DriftEvents:  []gitopsinsights.EventSummary{},
	}
	if p := readGitOpsWritePolicy(owner, root, root); p != nil {
		resp.Policy = &outsidePolicy{Tool: p.Tool, DriftDetection: p.DriftDetection, Suspended: p.Suspended != nil && *p.Suspended, Interval: p.Interval}
	}
	groups, cov := outsideListGroups(tree, root, k8s.ServedGVR)
	resp.Coverage = cov
	harmless := map[string]int{}
	others := map[string]int{}
	for _, obj := range listOutsideObjects(ctx, dyn, groups, fluxMemberSelector(root), defaultOutsideLimits, &resp.Coverage) {
		out, labels, otherManagers := classifyOutsideObject(obj, owner, root)
		for _, l := range labels {
			harmless[l]++
		}
		seen := map[string]bool{}
		for _, m := range otherManagers {
			if !seen[m] {
				seen[m] = true
				others[m]++
			}
		}
		if len(out.Changes) > 0 {
			resp.Objects = append(resp.Objects, out)
		}
	}
	for tool, n := range harmless {
		resp.Harmless = append(resp.Harmless, outsideToolCount{Tool: tool, Count: n})
	}
	sort.Slice(resp.Harmless, func(i, j int) bool { return resp.Harmless[i].Tool < resp.Harmless[j].Tool })
	for m, n := range others {
		resp.OtherWriters = append(resp.OtherWriters, outsideOtherWriter{Manager: m, Objects: n})
	}
	sort.Slice(resp.OtherWriters, func(i, j int) bool { return resp.OtherWriters[i].Manager < resp.OtherWriters[j].Manager })
	resp.Count = len(resp.Objects)
	if resolver != nil {
		for _, ev := range resolver.RecentEvents(owner.Group, owner.Kind, owner.Namespace, owner.Name) {
			if ev.Type == "Warning" && ev.Reason == "DriftDetected" {
				resp.DriftEvents = append(resp.DriftEvents, ev)
			}
		}
	}
	return resp, outsideScanComplete(resp.Coverage), nil
}

var errOutsideNoClient = errors.New("no Kubernetes client for this request")
