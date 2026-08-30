package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// printerColumn is one vendor-declared column from a CRD's
// spec.versions[].additionalPrinterColumns.
type printerColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Format      string `json:"format,omitempty"`
	Description string `json:"description,omitempty"`
	// Priority > 0 is kubectl's `-o wide` tier: declared, but not shown by
	// default. Forwarded so the client can honour the same distinction.
	Priority int32 `json:"priority,omitempty"`
}

// resourceTableResponse is the `?table=1` envelope. Its shape does not vary
// across the outcomes that answer 200: a preflight RBAC denial, a CRD Radar
// cannot read, and a kind that simply declares no columns all return the same
// three fields with Columns/Cells nil, so a consumer never has to switch on the
// top-level type. Outcomes that answer 4xx/5xx are unchanged — they return
// {"error": ...} as every other handler does.
type resourceTableResponse struct {
	Items any `json:"items"`
	// The resource these columns describe — the lowercased plural from the
	// request path, not a Kubernetes Kind. The client changes its selection
	// before the new list resolves, so without this it could pair the previous
	// selection's columns with the new one's rows.
	Kind  string `json:"kind,omitempty"`
	Group string `json:"group,omitempty"`
	// Nil when this kind has no usable printer columns, for any reason.
	Columns []printerColumn `json:"columns"`
	// metadata.uid -> one value per entry in Columns, same order. Nil whenever
	// Columns is nil. A column that does not resolve for a given object yields
	// a null in that slot rather than being omitted.
	Cells map[string][]any `json:"cells"`
}

// minSubstantivePrinterColumns is the eligibility gate. Radar already renders
// Name, Namespace and Age, so a CRD declaring only those (or one lone column
// beside them) would replace the generic Status column with less than it took
// away. Such kinds keep the generic columns instead.
const minSubstantivePrinterColumns = 2

// redundantPrinterColumns are columns Radar renders itself. Most CRDs declare
// Age, so without this the table would carry it twice.
var redundantPrinterColumns = map[string]bool{"name": true, "namespace": true, "age": true}

// buildResourceTable resolves the printer columns for a CRD kind and evaluates
// them over items. Returns nil columns (never an error) whenever the kind has
// no usable table: the caller renders its generic columns and the user sees a
// working list either way.
func buildResourceTable(ctx context.Context, listCache *k8s.ResourceCache, kind, group string, items []*unstructured.Unstructured) ([]printerColumn, map[string][]any) {
	if group == "" {
		return nil, nil
	}
	// The rows were read from listCache; the columns are resolved from whatever
	// the globals point at now. A context switch in between would pair one
	// cluster's objects with another cluster's column definitions, which the
	// kind/group identity cannot detect — both clusters call it the same kind.
	// Same identity check finalizePostContextSwitch's consumers use elsewhere.
	if listCache != nil && listCache != k8s.GetResourceCache() {
		return nil, nil
	}
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return nil, nil
	}
	gvr, ok := discovery.GetGVRWithGroup(kind, group)
	if !ok || gvr.Resource == "" {
		return nil, nil
	}

	crd := crdForResource(ctx, gvr)
	if crd == nil {
		return nil, nil
	}
	// Re-checked after the lookup, not only before it: the entry check leaves
	// discovery and the CRD read inside the window, so a switch starting right
	// after it would still pair cluster B's definitions with cluster A's rows.
	if listCache != nil && listCache != k8s.GetResourceCache() {
		return nil, nil
	}

	return tableFromCRD(ctx, crd, gvr.Version, items)
}

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// crdForResource returns the CRD that defines gvr, preferring the informer.
//
// GetDynamicWithGroup deliberately routes CRDs to a live GET so the YAML tab
// and MCP get_resource see spec.versions[].schema and spec.conversion, which
// the cache strips. Printer columns need neither — and transform.go preserves
// additionalPrinterColumns in the cache precisely so they can be read from it.
// Going through the direct path added a full apiserver round-trip (~150ms) to
// every table-mode list, repeated on each SSE-driven refetch.
//
// The dynamic cache starts a CRD informer if one isn't already up and can
// block briefly waiting for it to sync; CRDs are in its preload set, so in
// practice it is warm. The direct path remains as the fallback for when that
// read fails, so a restricted cache still resolves columns rather than
// silently dropping them.
//
// A failure here stays silent either way: reading CRDs is a separate grant
// from reading the custom resources themselves. The converse is deliberate
// too — a caller who can list the CRs sees the column definitions even
// without that grant, since those describe objects they can already read.
func crdForResource(ctx context.Context, gvr schema.GroupVersionResource) *unstructured.Unstructured {
	name := gvr.Resource + "." + gvr.Group
	if dyn := k8s.GetDynamicResourceCache(); dyn != nil {
		if crd, err := dyn.Get(crdGVR, "", name); err == nil && crd != nil {
			return crd
		}
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	crd, err := cache.GetDynamicWithGroup(ctx, "CustomResourceDefinition", "", name, "apiextensions.k8s.io")
	if err != nil {
		return nil
	}
	return crd
}

// convertorForDefs compiles the table convertor for a version's declared
// columns, or nil when the kind has no usable ones.
//
// Not cached: the compiled jsonpath.JSONPath objects carry mutable walk state
// (beginRange/inRange/lastEndNode), so a shared convertor races across
// concurrent list requests. Compiling costs noise next to evaluating the paths
// over every row.
func convertorForDefs(defs []apiextensionsv1.CustomResourceColumnDefinition) rest.TableConvertor {
	if len(defs) == 0 {
		return nil
	}
	convertor, err := tableconvertor.New(defs)
	if err != nil {
		// New returns a usable default convertor alongside the error, but that
		// default carries only the synthetic Name column — nothing worth
		// replacing the generic columns with.
		return nil
	}
	return convertor
}

// tableFromCRD is the whole derivation with the cluster lookups already done,
// so it can be exercised without a cache or discovery.
func tableFromCRD(ctx context.Context, crd *unstructured.Unstructured, version string, items []*unstructured.Unstructured) ([]printerColumn, map[string][]any) {
	defs := printerColumnDefsForVersion(crd, version)
	convertor := convertorForDefs(defs)
	if convertor == nil {
		return nil, nil
	}

	list := &unstructured.UnstructuredList{}
	for _, item := range items {
		if item != nil {
			list.Items = append(list.Items, *item)
		}
	}
	table, err := convertor.ConvertToTable(ctx, list, &metav1.TableOptions{})
	if err != nil || table == nil {
		return nil, nil
	}

	// ConvertToTable prepends a synthetic Name column that Radar already
	// renders; drop it and the vendor's own duplicates alongside it. keptIdx
	// maps a surviving column back to its cell offset in every row.
	columns := make([]printerColumn, 0, len(table.ColumnDefinitions))
	keptIdx := make([]int, 0, len(table.ColumnDefinitions))
	for i, def := range table.ColumnDefinitions {
		if i == 0 || redundantPrinterColumns[strings.ToLower(strings.TrimSpace(def.Name))] {
			continue
		}
		columns = append(columns, printerColumn{
			Name: def.Name, Type: def.Type, Format: def.Format,
			Description: vendorDescription(defs, i), Priority: def.Priority,
		})
		keptIdx = append(keptIdx, i)
	}
	if countSubstantive(columns) < minSubstantivePrinterColumns {
		return nil, nil
	}

	// Rows come back in list order from meta.EachListItem, so index alignment
	// with list.Items holds. Keyed by uid regardless, because the client joins
	// against a separately-rendered row set.
	cells := make(map[string][]any, len(table.Rows))
	for i, row := range table.Rows {
		if i >= len(list.Items) {
			break
		}
		uid := string(list.Items[i].GetUID())
		if uid == "" {
			continue
		}
		values := make([]any, len(keptIdx))
		for j, idx := range keptIdx {
			if idx < len(row.Cells) {
				values[j] = row.Cells[idx]
			}
		}
		cells[uid] = values
	}
	return columns, cells
}

// vendorDescription returns the description the CRD itself declared for the
// column at table-header index i, or "" when it declared none.
//
// The convertor's headers can't supply this: for a column with no description
// it substitutes "Custom resource definition column (in JSONPath format): <the
// path>", which is machinery, not something to show an operator — and most
// CRDs omit the field. Header 0 is the convertor's own synthetic Name column
// and every later header comes from one def in order, so header i is defs[i-1].
func vendorDescription(defs []apiextensionsv1.CustomResourceColumnDefinition, i int) string {
	if i-1 < 0 || i-1 >= len(defs) {
		return ""
	}
	return defs[i-1].Description
}

// countSubstantive counts the columns a reader would actually gain. Columns in
// kubectl's `-o wide` tier don't count toward the gate: a kind whose only
// columns are hidden by default has nothing to show.
func countSubstantive(columns []printerColumn) int {
	// Mirrors the client exactly, in its order: it deduplicates by trimmed name
	// first-wins across ALL priorities, drops blanks, and only then hides the
	// `-o wide` tier. Counting per-priority instead let a wide-tier duplicate
	// shadow a substantive one — `Phase`(wide), `Phase`, `Ready` counted two
	// here and rendered one on screen, which is exactly what the gate exists
	// to prevent. CRD validation requires neither unique nor trimmed names.
	seen := make(map[string]bool, len(columns))
	n := 0
	for _, c := range columns {
		name := strings.TrimSpace(c.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if c.Priority == 0 {
			n++
		}
	}
	return n
}

// printerColumnDefsForVersion returns the columns declared by the exact version
// being listed. Printer columns are per-version and the API server converts
// objects into the requested version, so the storage version's JSONPaths can
// address a schema these objects do not have.
func printerColumnDefsForVersion(crd *unstructured.Unstructured, version string) []apiextensionsv1.CustomResourceColumnDefinition {
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if !found || err != nil {
		return nil
	}
	for _, v := range versions {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := vm["name"].(string); name != version {
			continue
		}
		raw, ok := vm["additionalPrinterColumns"]
		if !ok {
			return nil
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		var defs []apiextensionsv1.CustomResourceColumnDefinition
		if err := json.Unmarshal(encoded, &defs); err != nil {
			return nil
		}
		return defs
	}
	return nil
}

// parseTableMode reports whether the caller asked for the printer-column
// envelope. Unknown values are rejected rather than silently ignored, matching
// parseResourcesInclude's posture.
func parseTableMode(v string) (bool, error) {
	switch v {
	case "":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("unknown table=%q (want: 1, true)", v)
	}
}

// coerceUnstructured collects the unstructured objects out of a list result.
// The dynamic path returns []*unstructured.Unstructured for a single namespace
// but []any once listPerNs merges across several, so both shapes reach here.
func coerceUnstructured(result any) []*unstructured.Unstructured {
	switch typed := result.(type) {
	case []*unstructured.Unstructured:
		return typed
	case []any:
		items := make([]*unstructured.Unstructured, 0, len(typed))
		for _, entry := range typed {
			if u, ok := entry.(*unstructured.Unstructured); ok {
				items = append(items, u)
			}
		}
		return items
	default:
		return nil
	}
}

// writeEmptyResourceTable answers a request that resolved to nothing for a
// reason that must not leak schema — today, an RBAC denial. Same envelope, no
// table, so the shape still doesn't vary with the caller's permissions.
func (s *Server) writeEmptyResourceTable(w http.ResponseWriter, tableMode bool, kind, group string) {
	if !tableMode {
		s.writeJSON(w, []any{})
		return
	}
	s.writeJSON(w, resourceTableResponse{Items: []any{}, Kind: kind, Group: group})
}

// writeResourceList writes a list response in whichever representation the
// caller asked for. In table mode the envelope is written for every outcome —
// including the ones that produce no columns — so the response shape never
// depends on the caller's permissions or on whether the kind is a CRD.
func (s *Server) writeResourceList(w http.ResponseWriter, r *http.Request, listCache *k8s.ResourceCache, tableMode bool, kind, group string, result any) {
	if !tableMode {
		s.writeJSON(w, result)
		return
	}
	// Items sits a level down from writeJSON's own nil-slice check, so it has to
	// be normalized here or an empty list serializes as `null` in table mode
	// while the bare-array path returns `[]` for the same request.
	resp := resourceTableResponse{Items: normalizeNilSlice(result), Kind: kind, Group: group}
	// Resolved even for an empty list: the columns belong to the CRD, not to
	// the rows, so an eligible kind keeps its headers when it holds nothing —
	// and the set doesn't flip as the last object comes and goes.
	resp.Columns, resp.Cells = buildResourceTable(r.Context(), listCache, kind, group, coerceUnstructured(result))
	s.writeJSON(w, resp)
}
