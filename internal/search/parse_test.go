package search

import (
	"reflect"
	"testing"
)

func TestParse_FreeTokens(t *testing.T) {
	q := Parse("redis prod")
	if !reflect.DeepEqual(q.Tokens, []string{"redis", "prod"}) {
		t.Fatalf("got %v", q.Tokens)
	}
	if q.KindFilter != nil || q.NSFilter != nil {
		t.Fatalf("unexpected filters: %+v", q)
	}
}

func TestParse_Modifiers(t *testing.T) {
	q := Parse("kind:Pod ns:prod label:app=redis image:redis:6.2 c:east")
	if got, want := q.KindFilter, []string{"pod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kind filter: got %v want %v", got, want)
	}
	if got, want := q.NSFilter, []string{"prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ns filter: got %v want %v", got, want)
	}
	if got, want := q.LabelFilter, []LabelEq{{"app", "redis"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("label filter: got %+v want %+v", got, want)
	}
	if got, want := q.ImageFilter, []string{"redis:6.2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("image filter: got %v want %v", got, want)
	}
	if q.Cluster != "east" {
		t.Fatalf("cluster: got %q", q.Cluster)
	}
	if len(q.Tokens) != 0 {
		t.Fatalf("expected no free tokens, got %v", q.Tokens)
	}
}

func TestParse_QuotedToken(t *testing.T) {
	q := Parse(`"my pod" ns:default`)
	if !reflect.DeepEqual(q.Tokens, []string{"my pod"}) {
		t.Fatalf("got %v", q.Tokens)
	}
	if !reflect.DeepEqual(q.NSFilter, []string{"default"}) {
		t.Fatalf("ns: %v", q.NSFilter)
	}
}

func TestParse_UnknownModifierPreserved(t *testing.T) {
	// We don't silently drop unknown modifiers — keep them as free tokens
	// so the user notices the typo (zero results vs. silently broad results).
	q := Parse("foo:bar redis")
	if !reflect.DeepEqual(q.Tokens, []string{"foo:bar", "redis"}) {
		t.Fatalf("got %v", q.Tokens)
	}
}

func TestParse_ColonInValueNotAModifier(t *testing.T) {
	// "1.2.3:80" is a port, not a modifier; the prefix isn't a letter-only key.
	q := Parse("1.2.3:80")
	if !reflect.DeepEqual(q.Tokens, []string{"1.2.3:80"}) {
		t.Fatalf("got %v", q.Tokens)
	}
}

func TestParse_LabelKeyOnly(t *testing.T) {
	// label:foo (no =) means "any label whose key is foo" — we only keep
	// the key; the value becomes a wildcard at match time.
	q := Parse("label:app")
	if got, want := q.LabelFilter, []LabelEq{{Key: "app"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestParse_Empty(t *testing.T) {
	q := Parse("")
	if q.Raw != "" || q.Tokens != nil || q.KindFilter != nil {
		t.Fatalf("expected zero query, got %+v", q)
	}
}
