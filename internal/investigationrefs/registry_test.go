package investigationrefs

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryIssuesOnlyInsideActiveScopeAndClosesWithExactPayloads(t *testing.T) {
	registry := NewRegistry()
	scope := strings.Repeat("a", 26)
	if _, ok := registry.Issue(scope, "unregistered"); ok {
		t.Fatal("unregistered scope issued a reference")
	}

	lease, err := registry.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Active(scope) {
		t.Fatal("begun scope is not active")
	}
	if _, err := registry.Begin(scope); !errors.Is(err, ErrScopeActive) {
		t.Fatalf("duplicate Begin error = %v, want ErrScopeActive", err)
	}

	first, ok := registry.Issue(scope, " exact payload \n")
	if !ok {
		t.Fatal("active scope did not issue a reference")
	}
	second, ok := registry.Issue(scope, `{"other":true}`)
	if !ok || second == first {
		t.Fatalf("second issue = (%q, %t), first = %q", second, ok, first)
	}
	records := lease.Close()
	if got := records[first]; got != " exact payload \n" {
		t.Fatalf("first payload = %q", got)
	}
	if got := records[second]; got != `{"other":true}` {
		t.Fatalf("second payload = %q", got)
	}
	if registry.Active(scope) {
		t.Fatal("closed scope remained active")
	}
	if _, ok := registry.Issue(scope, "after close"); ok {
		t.Fatal("closed scope issued a reference")
	}

	// The lease returns defensive copies, and reopening the same textual scope
	// starts with no records from the prior turn.
	delete(records, first)
	if got := lease.Close()[first]; got != " exact payload \n" {
		t.Fatalf("idempotent Close snapshot was mutable: %q", got)
	}
	reopened, err := registry.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	if records := reopened.Close(); len(records) != 0 {
		t.Fatalf("reopened scope inherited records: %v", records)
	}
}

func TestRegistryBoundsIssuedRecordsPerActiveScope(t *testing.T) {
	registry := NewRegistry()
	scope := strings.Repeat("b", 26)
	lease, err := registry.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	issued := 0
	for i := 0; i < maxIssuedRefsPerScope+10; i++ {
		if _, ok := registry.Issue(scope, "payload"); ok {
			issued++
		}
	}
	if issued != maxIssuedRefsPerScope {
		t.Fatalf("issued = %d, want cap %d", issued, maxIssuedRefsPerScope)
	}
	if records := lease.Close(); len(records) != maxIssuedRefsPerScope {
		t.Fatalf("closed records = %d, want %d", len(records), maxIssuedRefsPerScope)
	}
}
