package issuesapi

import "testing"

// The catalog must stay in lockstep with the category enum: every real category
// (everything in the group map) needs an order slot + a description, and nothing
// extra. Otherwise the Hub's Issues settings would silently drop a category or
// render a blank description.
func TestCatalogComplete(t *testing.T) {
	inOrder := map[Category]int{}
	for _, c := range catalogOrder {
		inOrder[c]++
		if inOrder[c] > 1 {
			t.Errorf("category %q appears more than once in catalogOrder", c)
		}
		if categoryDescription[c] == "" {
			t.Errorf("category %q has no description", c)
		}
		if _, ok := categoryGroup[c]; !ok {
			t.Errorf("category %q is in catalogOrder but not the group map", c)
		}
	}
	for c := range categoryGroup {
		if categoriesWithoutDetector[c] {
			// Excluded on purpose (no detector) — must NOT be in the catalog.
			if _, ok := inOrder[c]; ok {
				t.Errorf("category %q is marked no-detector but appears in catalogOrder", c)
			}
			continue
		}
		if _, ok := inOrder[c]; !ok {
			t.Errorf("category %q is in the group map but missing from catalogOrder/catalog", c)
		}
	}
	// Every no-detector exclusion must name a real enum category (else it's a
	// stale typo silently excluding nothing).
	for c := range categoriesWithoutDetector {
		if _, ok := categoryGroup[c]; !ok {
			t.Errorf("categoriesWithoutDetector lists %q which is not a real category", c)
		}
	}
}

// Catalog() groups by GroupOf, preserves group order, and surfaces every
// category exactly once.
func TestCatalogStructure(t *testing.T) {
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("Catalog() returned no groups")
	}
	seen := 0
	for _, g := range cat {
		if g.Title == "" {
			t.Errorf("group %q has no title", g.Group)
		}
		for _, c := range g.Categories {
			seen++
			if GroupOf(c.Category) != g.Group {
				t.Errorf("category %q filed under group %q but GroupOf says %q", c.Category, g.Group, GroupOf(c.Category))
			}
			if c.Description == "" {
				t.Errorf("category %q has empty description in Catalog()", c.Category)
			}
		}
	}
	if seen != len(catalogOrder) {
		t.Errorf("Catalog() surfaced %d categories, want %d", seen, len(catalogOrder))
	}
}
