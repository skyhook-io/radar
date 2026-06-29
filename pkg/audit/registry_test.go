package audit

import "testing"

// Every catalog entry must declare a category in the same vocabulary findings
// emit under — the Hub's Checks settings groups the catalog by it, so an empty
// or mistyped category would file a check under a section its findings never
// appear in (or drop it from the list entirely).
func TestRegistryCategoriesValid(t *testing.T) {
	valid := map[string]bool{
		CategorySecurity:    true,
		CategoryReliability: true,
		CategoryEfficiency:  true,
	}
	for id, meta := range CheckRegistry {
		if !valid[meta.Category] {
			t.Errorf("check %q has category %q, want one of Security/Reliability/Efficiency", id, meta.Category)
		}
	}
}

// DefaultSeverity drives what the Hub's Checks settings shows for a non-overridden
// check, so every catalog entry must carry a real ladder level.
func TestRegistryDefaultSeverity(t *testing.T) {
	for id, meta := range CheckRegistry {
		if meta.DefaultSeverity != sevHigh && meta.DefaultSeverity != sevMedium {
			t.Errorf("check %q has DefaultSeverity %q, want %q or %q", id, meta.DefaultSeverity, sevHigh, sevMedium)
		}
	}
	// Every nonMediumDefault key must name a real check, or it's silently dead.
	for id := range nonMediumDefault {
		if _, ok := CheckRegistry[id]; !ok {
			t.Errorf("nonMediumDefault names %q which is not in CheckRegistry", id)
		}
	}
}
