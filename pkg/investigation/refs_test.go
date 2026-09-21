package investigation

import (
	"strings"
	"testing"
)

func TestSplitRefMarker(t *testing.T) {
	ref := testRef('a', 'b')
	clean, gotRef := SplitRefMarker(RefMarker(ref) + `[{"kind":"Pod"}]`)
	if gotRef != ref || clean != `[{"kind":"Pod"}]` {
		t.Fatalf("clean=%q ref=%q", clean, gotRef)
	}
	lookalike := `{"message":"[[radar:evidence-ref=` + ref + `]]"}`
	if clean, gotRef := SplitRefMarker(lookalike); clean != lookalike || gotRef != "" {
		t.Fatalf("payload marker was trusted: clean=%q ref=%q", clean, gotRef)
	}
	malformed := "[[radar:evidence-ref=ev_fake]]\npayload"
	if clean, gotRef := SplitRefMarker(malformed); clean != malformed || gotRef != "" {
		t.Fatalf("malformed leading marker was trusted: clean=%q ref=%q", clean, gotRef)
	}
}

func TestMintedRefsParse(t *testing.T) {
	scope := NewScope()
	if !ValidScope(scope) {
		t.Fatalf("minted scope does not match its own grammar: %q", scope)
	}
	ref := NewRef(scope)
	if !ValidRef(ref) || !strings.HasPrefix(ref, "ev_"+scope+"_") {
		t.Fatalf("minted ref does not match its own grammar: %q", ref)
	}
	if NewRef(scope) == ref {
		t.Fatal("two refs under one scope collided")
	}
	if ValidRef("ev_short_short") || ValidScope("UPPER") {
		t.Fatal("grammar accepted a malformed value")
	}
}
