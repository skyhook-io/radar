package relations

import "testing"

func TestTheRegistryListsEachFamilyOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Families("cluster.local") {
		if seen[f.Name] {
			t.Errorf("%s listed twice", f.Name)
		}
		seen[f.Name] = true
	}
	for _, f := range SelectorFamilies() {
		if seen[f.Name] {
			t.Errorf("%s listed twice", f.Name)
		}
		seen[f.Name] = true
	}
	for _, f := range Compositions() {
		if seen[f.Name] {
			t.Errorf("%s listed twice", f.Name)
		}
		seen[f.Name] = true
	}
	if len(seen) != 41 {
		t.Errorf("the registry lists %d families; update it with the families", len(seen))
	}
}
