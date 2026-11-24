package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"

	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) handleArgoSyncDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireConnected(w) {
		return
	}
	ns, name := normalizeNamespaceParam(chi.URLParam(r, "namespace")), chi.URLParam(r, "name")
	q := r.URL.Query()
	id, err := strconv.ParseInt(q.Get("syncID"), 10, 64)
	previous, revision := q.Get("previous"), q.Get("revision")
	if err != nil || id < 1 || ns == "" || !gitops.IsFullGitSHA(previous) || !gitops.IsFullGitSHA(revision) {
		s.writeError(w, 400, "A retained sync ID and both full Git revisions are required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	conn := k8s.BoundOrCurrent(ctx)
	cache, discovery := conn.ResourceCache(), conn.Discovery()
	if cache == nil || discovery == nil {
		s.writeError(w, 503, "Kubernetes discovery is unavailable.")
		return
	}
	if failure := s.preflightResourceGetFrom(r, cache, "applications", ns, name, "argoproj.io"); failure != nil {
		s.writeReadFailure(w, "applications", failure, nil)
		return
	}
	if allowed, _ := s.grantDecision(r, Grant{Verb: "get", Group: "argoproj.io", Resource: "applications", Namespace: ns}); !allowed {
		s.writeError(w, 403, "Application access is unavailable.")
		return
	}
	app, err := cache.GetDynamicWithGroup(r.Context(), "applications", ns, name, "argoproj.io")
	if err != nil || app == nil || app.GetUID() == "" || app.GetGeneration() < 1 {
		s.writeError(w, 409, "The Application identity could not be verified.")
		return
	}
	if pair, ok := gitops.RenderableArgoSyncPairs(app)[id]; !ok || pair.Previous != previous || pair.Revision != revision {
		s.writeError(w, 409, "These revisions cannot be rendered with their recorded source settings on this cluster.")
		return
	}
	client, ok := argocd.Get()
	if !ok {
		s.writeError(w, 503, "Connect the Argo CD API to compare rendered resources.")
		return
	}
	project, _, _ := unstructured.NestedString(app.Object, "spec", "project")
	query := argoapi.ApplicationQuery{AppName: name, AppNamespace: ns, Project: project}
	matches := func() bool {
		uid, generation, err := client.ApplicationGeneration(ctx, query)
		return err == nil && uid == string(app.GetUID()) && generation == app.GetGeneration()
	}
	if !matches() {
		s.writeError(w, 409, "The connected Argo CD server could not confirm this Application and its source settings.")
		return
	}
	before, err := client.RevisionManifests(ctx, query, previous)
	if err != nil {
		s.writeError(w, 502, "Argo CD could not render the previous Git revision.")
		return
	}
	after, err := client.RevisionManifests(ctx, query, revision)
	if err != nil {
		s.writeError(w, 502, "Argo CD could not render the selected Git revision.")
		return
	}
	dest, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace")
	resolve := argoRenderResolver(dest, discovery.GetResourceWithGroup, func(info k8s.APIResource, ns, name string) bool {
		allowed, _ := s.grantDecision(r, Grant{Verb: "get", Group: info.Group, Resource: info.Name, Namespace: ns})
		return allowed && s.preflightResourceGetFrom(r, cache, info.Name, ns, name, info.Group) == nil
	})
	diff, err := compareArgoRenders(before, after, resolve)
	if err != nil {
		s.writeError(w, 422, err.Error())
		return
	}
	stillMatches := matches()
	if current, ok := argocd.Get(); !stillMatches || !ok || current != client || k8s.Current() != conn || k8s.GetResourceCache() != cache {
		s.writeError(w, 409, "The Application or connection changed during comparison. Reopen this sync to retry.")
		return
	}
	s.writeJSON(w, diff)
}

type renderedKey struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
type renderedField struct {
	Path   []string        `json:"path"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}
type renderedChange struct {
	renderedKey
	Operation string          `json:"operation"`
	Fields    []renderedField `json:"fields"`
}
type renderedSyncDiff struct {
	Changes       []renderedChange `json:"changes"`
	Partial       bool             `json:"partial,omitempty"`
	SecretsMasked bool             `json:"secretsMasked,omitempty"`
}

type resolveRenderedResource func(group, kind, ns, name string) (string, bool)

// Resolve scope and access once per comparison. A transient denial on one
// render must withhold the resource from both sides, even if only one side
// explicitly sets the Application's default namespace.
func argoRenderResolver(destination string, discover func(kind, group string) (k8s.APIResource, bool), canRead func(k8s.APIResource, string, string) bool) resolveRenderedResource {
	kinds := make(map[schema.GroupKind]k8s.APIResource)
	access := make(map[renderedKey]bool)
	return func(group, kind, ns, name string) (string, bool) {
		gk := schema.GroupKind{Group: group, Kind: kind}
		info, cached := kinds[gk]
		if !cached {
			if resource, found := discover(kind, group); found {
				info = resource
			}
			kinds[gk] = info
		}
		if info.Group != group || info.Kind != kind {
			return "", false
		}
		if !info.Namespaced {
			ns = ""
		} else if ns == "" {
			ns = destination
		}
		if info.Namespaced && ns == "" {
			return "", false
		}
		key := renderedKey{group, kind, ns, name}
		allowed, cached := access[key]
		if !cached {
			allowed = canRead(info, ns, name)
			access[key] = allowed
		}
		return ns, allowed
	}
}

func indexArgoRender(manifests []string, resolve resolveRenderedResource) (map[renderedKey]map[string]any, bool, error) {
	out := make(map[renderedKey]map[string]any)
	partial := false
	if len(manifests) > 2000 {
		return nil, false, errors.New("This application exceeds the rendered comparison limit of 2,000 resources.")
	}
	for _, manifest := range manifests {
		var obj unstructured.Unstructured
		if err := json.Unmarshal([]byte(manifest), &obj); err != nil {
			return nil, false, errors.New("Argo returned an unreadable manifest.")
		}
		gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
		if err != nil || gv.Version == "" {
			return nil, false, errors.New("Argo returned a resource with an invalid API version.")
		}
		group := gv.Group
		kind, name := obj.GetKind(), obj.GetName()
		if kind == "" || name == "" || (strings.HasSuffix(kind, "List") && obj.IsList()) {
			partial = true
			continue
		}
		ns, allowed := resolve(group, kind, obj.GetNamespace(), name)
		if !allowed {
			partial = true
			continue
		}
		key := renderedKey{group, kind, ns, name}
		if _, dup := out[key]; dup {
			return nil, false, errors.New("A rendered revision contains duplicate resource identities.")
		}
		stripManifestNoise(obj.Object)
		out[key] = obj.Object
	}
	return out, partial, nil
}

func compareArgoRenders(before, after []string, resolve resolveRenderedResource) (renderedSyncDiff, error) {
	result := renderedSyncDiff{Changes: []renderedChange{}}
	old, p1, err := indexArgoRender(before, resolve)
	if err != nil {
		return result, err
	}
	next, p2, err := indexArgoRender(after, resolve)
	if err != nil {
		return result, err
	}
	result.Partial = p1 || p2
	keys := make(map[renderedKey]bool, len(old)+len(next))
	for k := range old {
		keys[k] = true
	}
	for k := range next {
		keys[k] = true
	}
	budget := renderedDiffBudget{fields: 10000, bytes: 4 << 20}
	for k := range keys {
		a, b := old[k], next[k]
		if isCoreSecret(k.Kind, k.Group) {
			result.SecretsMasked = true
			redactSecretManifestWithMarkers(b, a, "<redacted:after>", "<redacted:before>")
		}
		// Custom resources (e.g. resource templates) can contain whole Secret
		// objects. Argo only redacts top-level Secrets; suppress embedded
		// Secrets as opaque, including when a parent field changes type.
		for _, obj := range []map[string]any{a, b} {
			for _, v := range obj {
				if maskEmbeddedSecrets(v) {
					result.SecretsMasked = true
				}
			}
		}
		op := "modified"
		if a == nil {
			op = "added"
		} else if b == nil {
			op = "removed"
		}
		budget.podSpecPath = k8score.WorkloadPodSpecPath(k.Group, k.Kind)
		change := renderedChange{renderedKey: k, Operation: op}
		if err := renderedFields(nil, a, true, b, true, &change.Fields, &budget); err != nil {
			return result, err
		}
		if len(change.Fields) > 0 {
			result.Changes = append(result.Changes, change)
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool {
		a, b := result.Changes[i].renderedKey, result.Changes[j].renderedKey
		return slices.Compare([]string{a.Kind, a.Namespace, a.Name, a.Group}, []string{b.Kind, b.Namespace, b.Name, b.Group}) < 0
	})
	return result, nil
}

// maskEmbeddedSecrets leaves the enclosing object's desired fields available.
func maskEmbeddedSecrets(v any) bool {
	masked := false
	switch node := v.(type) {
	case map[string]any:
		if node["apiVersion"] == "v1" && (node["kind"] == "Secret" || node["kind"] == "SecretList") {
			kind := node["kind"]
			clear(node)
			node["apiVersion"], node["kind"] = "v1", kind
			return true
		}
		for _, child := range node {
			if maskEmbeddedSecrets(child) {
				masked = true
			}
		}
	case []any:
		for _, child := range node {
			if maskEmbeddedSecrets(child) {
				masked = true
			}
		}
	}
	return masked
}

type renderedDiffBudget struct {
	podSpecPath []string
	fields      int
	bytes       int
}

// These list merge keys belong to the Kubernetes PodSpec schema. Arbitrary
// lists in CRs/configuration retain their literal index semantics.
func podListMergeKey(podSpec, path []string) string {
	if podSpec == nil || len(path) <= len(podSpec) || !slices.Equal(path[:len(podSpec)], podSpec) {
		return ""
	}
	tail := path[len(podSpec):]
	if len(tail) == 1 && tail[0] == "volumes" {
		return "name"
	}
	switch tail[0] {
	case "containers", "initContainers", "ephemeralContainers":
		if len(tail) == 1 {
			return "name"
		}
		if len(tail) == 3 {
			switch tail[2] {
			case "env":
				return "name"
			case "volumeMounts":
				return "mountPath"
			}
		}
	}
	return ""
}

type namedRenderItems struct {
	order []string
	byKey map[string]any
}

func indexNamedRenderItems(items []any, key string) (namedRenderItems, bool) {
	out := namedRenderItems{byKey: make(map[string]any, len(items))}
	for _, item := range items {
		obj, _ := item.(map[string]any)
		id, _ := obj[key].(string)
		if _, duplicate := out.byKey[id]; id == "" || duplicate {
			return namedRenderItems{}, false
		}
		out.order = append(out.order, id)
		out.byKey[id] = item
	}
	return out, true
}

func compareNamedRenderItems(path []string, key string, a, b namedRenderItems, out *[]renderedField, budget *renderedDiffBudget) error {
	// Report order independently of each item's fields. Env vars can depend
	// on preceding vars and init containers run sequentially.
	if len(a.order) > 0 && len(b.order) > 0 && !slices.Equal(a.order, b.order) {
		if err := renderedFields(append(path, "[order]"), a.order, true, b.order, true, out, budget); err != nil {
			return err
		}
	}
	ids := slices.Clone(a.order)
	for _, id := range b.order {
		if _, ok := a.byKey[id]; !ok {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		av, aok := a.byKey[id]
		bv, bok := b.byKey[id]
		selector := "[" + key + "=" + id + "]"
		if err := renderedFields(append(path, selector), av, aok, bv, bok, out, budget); err != nil {
			return err
		}
	}
	return nil
}

// Preserve absent vs explicit null, whole-object additions/removals, and every
// field. A path is segmented so an annotation name containing dots is unambiguous.
func renderedFields(path []string, a any, hasA bool, b any, hasB bool, out *[]renderedField, budget *renderedDiffBudget) error {
	if len(path) > 128 {
		return errors.New("The rendered resources exceed the comparison depth limit. Use the Git comparison for the full revisions.")
	}
	am, aMap := a.(map[string]any)
	bm, bMap := b.(map[string]any)
	if (aMap || !hasA) && (bMap || !hasB) && (len(am)+len(bm) > 0) {
		for _, k := range slices.Sorted(maps.Keys(unionMapKeys(am, bm))) {
			av, aok := am[k]
			bv, bok := bm[k]
			if err := renderedFields(append(path, k), av, aok, bv, bok, out, budget); err != nil {
				return err
			}
		}
		return nil
	}
	aa, aList := a.([]any)
	bb, bList := b.([]any)
	if (aList || !hasA) && (bList || !hasB) && len(aa)+len(bb) > 0 {
		if key := podListMergeKey(budget.podSpecPath, path); key != "" {
			left, lok := indexNamedRenderItems(aa, key)
			right, rok := indexNamedRenderItems(bb, key)
			if lok && rok {
				return compareNamedRenderItems(path, key, left, right, out, budget)
			}
		}
		for i := range max(len(aa), len(bb)) {
			var av, bv any
			if i < len(aa) {
				av = aa[i]
			}
			if i < len(bb) {
				bv = bb[i]
			}
			if err := renderedFields(append(path, strconv.Itoa(i)), av, i < len(aa), bv, i < len(bb), out, budget); err != nil {
				return err
			}
		}
		return nil
	}
	if hasA == hasB && reflect.DeepEqual(a, b) {
		return nil
	}
	if budget.fields <= 0 {
		return errors.New("The comparison exceeds 10,000 changed fields. Use the Git comparison for the full revisions.")
	}
	budget.fields--
	f := renderedField{Path: slices.Clone(path)}
	if hasA {
		f.Before, _ = json.Marshal(a)
	}
	if hasB {
		f.After, _ = json.Marshal(b)
	}
	encoded, err := json.Marshal(f)
	if err != nil || len(encoded) > budget.bytes {
		return errors.New("The rendered comparison is too large. Use the Git comparison for the full revisions.")
	}
	budget.bytes -= len(encoded)
	*out = append(*out, f)
	return nil
}
