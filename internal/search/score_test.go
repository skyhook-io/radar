package search

import (
	"strings"
	"testing"
)

func cand() candidate {
	return candidate{
		Kind:        "Pod",
		Namespace:   "prod",
		Name:        "redis-master",
		Labels:      map[string]string{"app": "redis", "tier": "cache"},
		Annotations: map[string]string{"owner": "platform-team"},
		Images:      []string{"redis:6.2.7"},
	}
}

func TestMatch_FreeTokenScoresHighestSite(t *testing.T) {
	q := Parse("redis")
	score, _, _, ok := match(q, cand())
	if !ok {
		t.Fatal("expected match")
	}
	// Best site is name-prefix (redis-master) at 60.
	if score != scoreNamePrefix {
		t.Fatalf("score=%d, expected %d (name prefix)", score, scoreNamePrefix)
	}
}

func TestMatch_TwoTokensSummed(t *testing.T) {
	q := Parse("redis cache")
	score, matched, _, ok := match(q, cand())
	if !ok {
		t.Fatal("expected match")
	}
	// redis: prefix on name (60). cache: exact label value (25).
	if score != scoreNamePrefix+scoreLabelValExact {
		t.Fatalf("score=%d", score)
	}
	if len(matched) != 2 {
		t.Fatalf("matched=%+v", matched)
	}
}

func TestMatch_TokenMustMatchSomewhere(t *testing.T) {
	q := Parse("redis nope-not-here")
	if _, _, _, ok := match(q, cand()); ok {
		t.Fatal("expected no match — second token must reject")
	}
}

func TestMatch_KindFilter(t *testing.T) {
	c := cand()
	if _, _, _, ok := match(Parse("kind:Service"), c); ok {
		t.Fatal("kind:Service should reject a Pod candidate")
	}
	if _, _, _, ok := match(Parse("kind:Pod"), c); !ok {
		t.Fatal("kind:Pod should match a Pod candidate")
	}
	// Pluralized form too — radar fetch.go uses lowercase plural keys.
	if _, _, _, ok := match(Parse("kind:pods"), c); !ok {
		t.Fatal("kind:pods should match")
	}
}

func TestMatch_NSFilter(t *testing.T) {
	c := cand()
	if _, _, _, ok := match(Parse("ns:dev"), c); ok {
		t.Fatal("ns:dev should reject prod candidate")
	}
	if _, _, _, ok := match(Parse("ns:prod"), c); !ok {
		t.Fatal("ns:prod should match")
	}
}

func TestMatch_LabelFilter(t *testing.T) {
	c := cand()
	if _, _, _, ok := match(Parse("label:app=redis"), c); !ok {
		t.Fatal("label:app=redis should match")
	}
	if _, _, _, ok := match(Parse("label:app=postgres"), c); ok {
		t.Fatal("label:app=postgres should reject")
	}
	if _, _, _, ok := match(Parse("label:app"), c); !ok {
		t.Fatal("label:app (key-only) should match when label exists")
	}
	if _, _, _, ok := match(Parse("label:missing"), c); ok {
		t.Fatal("label:missing should reject when label absent")
	}
}

func TestMatch_ImageFilter(t *testing.T) {
	c := cand()
	if _, _, _, ok := match(Parse("image:redis"), c); !ok {
		t.Fatal("image:redis should match")
	}
	if _, _, _, ok := match(Parse("image:nginx"), c); ok {
		t.Fatal("image:nginx should reject")
	}
}

func TestMatch_PureFilterReturnsFlatScore(t *testing.T) {
	// Filter-only query (no free tokens) should return a positive flat
	// score so candidates show up at all.
	score, _, _, ok := match(Parse("kind:Pod ns:prod"), cand())
	if !ok || score <= 0 {
		t.Fatalf("filter-only match: score=%d ok=%v", score, ok)
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	q := Parse("REDIS")
	if _, _, _, ok := match(q, cand()); !ok {
		t.Fatal("expected case-insensitive match")
	}
}

func TestMatch_ContentSnippet(t *testing.T) {
	c := cand()
	c.Content = []ContentField{{
		Path:  "data.flags.json",
		Value: `{"adServiceFailure":{"defaultVariant":"on"}}`,
	}}
	score, matched, snippets, ok := match(Parse("adServiceFailure"), c)
	if !ok {
		t.Fatal("expected content match")
	}
	if score != scoreContentSubstr {
		t.Fatalf("score=%d, expected content score %d", score, scoreContentSubstr)
	}
	if len(matched) != 1 || matched[0].Site != "content:data.flags.json" {
		t.Fatalf("matched=%+v", matched)
	}
	if len(snippets) != 1 || snippets[0].Path != "data.flags.json" || !strings.Contains(snippets[0].Snippet, "adServiceFailure") {
		t.Fatalf("snippets=%+v", snippets)
	}
}

func TestKindMatches_Variants(t *testing.T) {
	cases := []struct {
		kind, filter string
		want         bool
	}{
		{"Pod", "pod", true},
		{"Pod", "Pod", true},
		{"Pod", "pods", true},
		{"Service", "svc", false}, // we don't expand short names
		{"Deployment", "deployment", true},
		{"Deployment", "deployments", true},
		{"Pod", "service", false},
	}
	for _, tc := range cases {
		got := kindMatches(tc.kind, []string{tc.filter})
		if got != tc.want {
			t.Errorf("kindMatches(%q, %q) = %v, want %v", tc.kind, tc.filter, got, tc.want)
		}
	}
}
