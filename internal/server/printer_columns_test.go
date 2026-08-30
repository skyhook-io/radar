package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
)

func crdWith(version string, cols []map[string]any) *unstructured.Unstructured {
	entry := map[string]any{"name": version, "served": true, "storage": true}
	if cols != nil {
		raw := make([]any, 0, len(cols))
		for _, c := range cols {
			raw = append(raw, c)
		}
		entry["additionalPrinterColumns"] = raw
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "widgets.example.com"},
		"spec":       map[string]any{"versions": []any{entry}},
	}}
}

func pcObj(uid string, content map[string]any) *unstructured.Unstructured {
	o := map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w-" + uid, "uid": uid},
	}
	for k, v := range content {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func pcCol(name, jsonPath, colType string, priority int64) map[string]any {
	c := map[string]any{"name": name, "jsonPath": jsonPath, "type": colType}
	if priority != 0 {
		c["priority"] = priority
	}
	return c
}

func TestTableFromCRD_EvaluatesAndTypesCells(t *testing.T) {
	crd := crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol("Replicas", ".status.replicas", "integer", 0),
		pcCol("Ready", ".status.ready", "boolean", 0),
	})
	items := []*unstructured.Unstructured{
		pcObj("a", map[string]any{"status": map[string]any{"phase": "Running", "replicas": int64(3), "ready": true}}),
		pcObj("b", map[string]any{"status": map[string]any{"phase": "Pending"}}),
	}

	columns, cells := tableFromCRD(context.Background(), crd, "v1", items)
	if len(columns) != 3 {
		t.Fatalf("want 3 columns, got %d (%+v)", len(columns), columns)
	}
	if columns[0].Name != "Phase" || columns[1].Type != "integer" {
		t.Errorf("column metadata not forwarded: %+v", columns)
	}
	if got := cells["a"]; len(got) != 3 || got[0] != "Running" || got[1] != int64(3) || got[2] != true {
		t.Errorf("row a: values must keep their declared types, got %#v", got)
	}
	// A path that resolves for one object and not another must leave a hole,
	// not shift the remaining values into the wrong columns.
	if got := cells["b"]; len(got) != 3 || got[0] != "Pending" || got[1] != nil || got[2] != nil {
		t.Errorf("row b: unresolved paths must be nil in place, got %#v", got)
	}
}

func TestTableFromCRD_DropsColumnsRadarAlreadyRenders(t *testing.T) {
	crd := crdWith("v1", []map[string]any{
		pcCol("Age", ".metadata.creationTimestamp", "date", 0),
		pcCol("Namespace", ".metadata.namespace", "string", 0),
		pcCol("Host", ".spec.host", "string", 0),
		pcCol("Port", ".spec.port", "integer", 0),
	})
	items := []*unstructured.Unstructured{pcObj("a", map[string]any{"spec": map[string]any{"host": "h", "port": int64(80)}})}

	columns, cells := tableFromCRD(context.Background(), crd, "v1", items)
	if len(columns) != 2 || columns[0].Name != "Host" || columns[1].Name != "Port" {
		t.Fatalf("Age/Namespace must be dropped, got %+v", columns)
	}
	if got := cells["a"]; len(got) != 2 || got[0] != "h" || got[1] != int64(80) {
		t.Errorf("cells must realign to the kept columns, got %#v", got)
	}
}

func TestTableFromCRD_EligibilityGate(t *testing.T) {
	tests := []struct {
		name string
		cols []map[string]any
		want bool
	}{
		// The regression this gate exists to stop: exclusive mode would replace
		// the generic Status column with a table narrower than it took away.
		{"only Age", []map[string]any{pcCol("Age", ".metadata.creationTimestamp", "date", 0)}, false},
		{"one substantive column", []map[string]any{
			pcCol("Age", ".metadata.creationTimestamp", "date", 0),
			pcCol("Status", ".status.phase", "string", 0),
		}, false},
		{"two substantive columns", []map[string]any{
			pcCol("Status", ".status.phase", "string", 0),
			pcCol("Host", ".spec.host", "string", 0),
		}, true},
		// Columns kubectl hides behind -o wide cannot carry the gate on their own.
		{"substantive only at wide priority", []map[string]any{
			pcCol("Status", ".status.phase", "string", 0),
			pcCol("UID", ".metadata.uid", "string", 1),
			pcCol("Hash", ".metadata.labels.hash", "string", 1),
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			columns, _ := tableFromCRD(context.Background(), crdWith("v1", tc.cols), "v1",
				[]*unstructured.Unstructured{pcObj("a", nil)})
			if got := columns != nil; got != tc.want {
				t.Errorf("eligible = %v, want %v (columns %+v)", got, tc.want, columns)
			}
		})
	}
}

func TestTableFromCRD_WideColumnsAreForwardedNotDropped(t *testing.T) {
	// The gate ignores them; the response still carries them so the client can
	// offer them in the column picker the way `kubectl -o wide` does.
	columns, _ := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Status", ".status.phase", "string", 0),
		pcCol("Host", ".spec.host", "string", 0),
		pcCol("UID", ".metadata.uid", "string", 1),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})

	if len(columns) != 3 || columns[2].Name != "UID" || columns[2].Priority != 1 {
		t.Fatalf("wide-tier column must survive with its priority, got %+v", columns)
	}
}

func TestPrinterColumnDefsForVersion_MatchesTheServedVersion(t *testing.T) {
	// Printer columns are per-version and the API server converts objects into
	// the requested version, so reading another version's paths would address a
	// schema these objects do not have.
	crd := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"versions": []any{
		map[string]any{"name": "v1alpha1", "additionalPrinterColumns": []any{pcCol("Old", ".status.old", "string", 0)}},
		map[string]any{"name": "v1", "storage": true, "additionalPrinterColumns": []any{pcCol("New", ".status.new", "string", 0)}},
	}}}}

	if defs := printerColumnDefsForVersion(crd, "v1"); len(defs) != 1 || defs[0].Name != "New" {
		t.Errorf("v1 must resolve its own columns, got %+v", defs)
	}
	if defs := printerColumnDefsForVersion(crd, "v1alpha1"); len(defs) != 1 || defs[0].Name != "Old" {
		t.Errorf("v1alpha1 must resolve its own columns, got %+v", defs)
	}
	if defs := printerColumnDefsForVersion(crd, "v2"); defs != nil {
		t.Errorf("an absent version must resolve nothing, got %+v", defs)
	}
}

func TestPrinterColumnDefsForVersion_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		crd  *unstructured.Unstructured
	}{
		{"no versions", &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}},
		{"no columns", crdWith("v1", nil)},
		{"columns of the wrong shape", &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"versions": []any{
			map[string]any{"name": "v1", "additionalPrinterColumns": "not-a-list"},
		}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if defs := printerColumnDefsForVersion(tc.crd, "v1"); defs != nil {
				t.Errorf("want nil, got %+v", defs)
			}
		})
	}
}

func TestTableFromCRD_UnparseableJSONPathYieldsNoTable(t *testing.T) {
	// A single bad path makes tableconvertor.New return a Name-only convertor,
	// which is not worth replacing the generic columns with.
	columns, cells := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Good", ".status.phase", "string", 0),
		pcCol("Bad", ".status[", "string", 0),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})
	if columns != nil || cells != nil {
		t.Errorf("want no table, got columns %+v cells %+v", columns, cells)
	}
}

func TestTableFromCRD_SkipsObjectsWithoutUID(t *testing.T) {
	// Cells are keyed by uid because the client joins them against a separately
	// rendered row set; an object with no uid has nothing to join on.
	noUID := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "anon"},
		"status":   map[string]any{"phase": "Running", "host": "h"},
	}}
	_, cells := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol("Host", ".status.host", "string", 0),
	}), "v1", []*unstructured.Unstructured{noUID, pcObj("a", map[string]any{"status": map[string]any{"phase": "P", "host": "x"}})})

	if len(cells) != 1 {
		t.Fatalf("want only the identifiable object, got %#v", cells)
	}
	if got := cells["a"]; len(got) != 2 || got[0] != "P" {
		t.Errorf("the surviving row must keep its own values, got %#v", got)
	}
}

func TestParseTableMode(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"1", true, false},
		{"true", true, false},
		{"0", false, true},
		{"yes", false, true},
	} {
		got, err := parseTableMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseTableMode(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("parseTableMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCoerceUnstructured(t *testing.T) {
	a, b := pcObj("a", nil), pcObj("b", nil)
	// Single-namespace lists arrive typed; listPerNs merges arrive as []any.
	if got := coerceUnstructured([]*unstructured.Unstructured{a, b}); len(got) != 2 {
		t.Errorf("typed slice: want 2, got %d", len(got))
	}
	if got := coerceUnstructured([]any{a, b}); len(got) != 2 {
		t.Errorf("merged slice: want 2, got %d", len(got))
	}
	if got := coerceUnstructured([]any{a, "junk", nil, b}); len(got) != 2 {
		t.Errorf("mixed slice must keep only unstructured entries, got %d", len(got))
	}
	if got := coerceUnstructured("not a list"); got != nil {
		t.Errorf("want nil for a non-list, got %#v", got)
	}
}

func TestTableFromCRD_EmptyListStillResolvesColumns(t *testing.T) {
	// Columns belong to the CRD, not to the rows. An eligible kind holding
	// nothing must still show its headers, and the set must not appear and
	// disappear as the last object is created and deleted.
	columns, cells := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Status", ".status.phase", "string", 0),
		pcCol("Host", ".spec.host", "string", 0),
	}), "v1", nil)

	if len(columns) != 2 {
		t.Fatalf("want 2 columns for an empty list, got %+v", columns)
	}
	if len(cells) != 0 {
		t.Errorf("want no cells for an empty list, got %#v", cells)
	}
}

func TestNormalizeNilSlice(t *testing.T) {
	// A nil slice serializes as `null`. In table mode the items slice sits
	// inside the envelope, below the depth writeJSON's own check reaches, so an
	// empty list came back as `null` there while the bare-array path for the
	// same request returned `[]`.
	var typedNil []*unstructured.Unstructured
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"untyped nil", nil},
		{"typed nil slice", typedNil},
		{"nil any slice", []any(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(normalizeNilSlice(tc.in))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != "[]" {
				t.Errorf("got %s, want []", encoded)
			}
		})
	}

	// A populated slice must pass through untouched.
	items := []*unstructured.Unstructured{pcObj("a", nil)}
	if got := normalizeNilSlice(items); got == nil {
		t.Error("a populated slice must survive normalization")
	}
}

func TestWriteEmptyResourceTable_DeniedRequestCarriesNoSchema(t *testing.T) {
	// An RBAC denial and an authorized-but-empty list both produce zero items,
	// but only the second may carry columns: resolving them reads the CRD as
	// Radar's own identity, so answering a denial with them would hand schema
	// to a caller who cannot list the kind. The envelope shape stays identical
	// either way.
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeEmptyResourceTable(rec, true, "widgets", "example.com")

	var got resourceTableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Columns != nil || got.Cells != nil {
		t.Errorf("a denial must carry no table, got columns %+v cells %+v", got.Columns, got.Cells)
	}
	if got.Kind != "widgets" || got.Group != "example.com" {
		t.Errorf("identity must still be echoed, got %q/%q", got.Kind, got.Group)
	}
	// items must be [] and not null, same as every other table-mode response.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"items":[]`)) {
		t.Errorf("items must serialize as [], got %s", rec.Body.String())
	}
}

func TestWriteEmptyResourceTable_BareArrayWhenNotTableMode(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeEmptyResourceTable(rec, false, "widgets", "example.com")
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("want a bare array without table mode, got %s", body)
	}
}

func TestCRDGVRAddressesV1(t *testing.T) {
	// crdForResource looks the CRD up by this GVR against the informer rather
	// than through GetDynamicWithGroup, which routes CRDs to a live GET for the
	// YAML tab's benefit. A wrong version here would miss the informer on every
	// request and silently fall back to that ~150ms round-trip.
	if crdGVR.Group != "apiextensions.k8s.io" || crdGVR.Version != "v1" || crdGVR.Resource != "customresourcedefinitions" {
		t.Errorf("crdGVR must match the registered dynamic cache entry, got %v", crdGVR)
	}
}

func TestTableFromCRD_GateIgnoresBlankNames(t *testing.T) {
	// The client drops a blank name outright, so counting one here lets a kind
	// through a gate it does not actually clear: " " plus "Phase" would be two
	// columns to the server and one on screen.
	columns, _ := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol("   ", ".status.other", "string", 0),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})
	if columns != nil {
		t.Errorf("a blank name must not satisfy the gate, got %+v", columns)
	}
}

func TestTableFromCRD_GateCountsNamesTheClientWillMerge(t *testing.T) {
	// The client trims before deduplicating, so untrimmed variants of one name
	// would pass a two-column gate here and then collapse into a single column
	// there — replacing the generic Status with less than the gate promised.
	// Kubernetes requires a non-empty column name, not a trimmed one.
	columns, _ := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol(" Phase ", ".status.phase", "string", 0),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})
	if columns != nil {
		t.Errorf("padded duplicates must not satisfy the gate, got %+v", columns)
	}
}

func TestBuildResourceTable_RejectsACacheThatChangedUnderIt(t *testing.T) {
	// The rows come from the cache the handler captured; the columns are
	// resolved from the globals afterwards. If a context switch replaced the
	// cache in between, the two belong to different clusters — and kind/group
	// identity cannot tell, because both clusters call it the same kind.
	stale := &k8s.ResourceCache{}
	columns, cells := buildResourceTable(context.Background(), stale, "widgets", "example.com", nil)
	if columns != nil || cells != nil {
		t.Errorf("a stale list cache must produce no table, got columns %+v cells %+v", columns, cells)
	}
}

func TestTableFromCRD_GateHonoursFirstWinsDedupeAcrossPriorities(t *testing.T) {
	// The client deduplicates by name first-wins across all priorities, then
	// hides the wide tier. A wide-tier name appearing before its substantive
	// twin therefore shadows it on screen, and counting per-priority missed
	// that: two columns to the gate, one rendered.
	columns, _ := tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 1),
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol("Ready", ".status.ready", "string", 0),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})
	if columns != nil {
		t.Errorf("a shadowed duplicate must not satisfy the gate, got %+v", columns)
	}

	// The same three names without the shadowing still clear it.
	columns, _ = tableFromCRD(context.Background(), crdWith("v1", []map[string]any{
		pcCol("Phase", ".status.phase", "string", 0),
		pcCol("Ready", ".status.ready", "string", 0),
	}), "v1", []*unstructured.Unstructured{pcObj("a", nil)})
	if len(columns) != 2 {
		t.Errorf("two distinct substantive columns must clear the gate, got %+v", columns)
	}
}

func TestTableFromCRD_DescriptionComesFromTheCRDNotTheConvertor(t *testing.T) {
	// tableconvertor substitutes "Custom resource definition column (in JSONPath
	// format): <path>" for a column the CRD gave no description. Most CRDs give
	// none, so forwarding the convertor's headers would put the raw JSONPath in
	// a user-facing tooltip on nearly every uncurated kind.
	described := pcCol("Phase", ".status.phase", "string", 0)
	described["description"] = "Current lifecycle phase"
	crd := crdWith("v1", []map[string]any{
		described,
		pcCol("Replicas", ".status.replicas", "integer", 0),
	})

	columns, _ := tableFromCRD(context.Background(), crd, "v1", nil)
	if len(columns) != 2 {
		t.Fatalf("want 2 columns, got %d (%+v)", len(columns), columns)
	}
	if columns[0].Description != "Current lifecycle phase" {
		t.Errorf("a declared description must survive, got %q", columns[0].Description)
	}
	if columns[1].Description != "" {
		t.Errorf("an undeclared description must stay empty, got %q", columns[1].Description)
	}
	for _, c := range columns {
		if strings.Contains(c.Description, "JSONPath format") {
			t.Errorf("column %q leaked the convertor's synthetic description: %q", c.Name, c.Description)
		}
	}
}
