package investigation

import (
	"os"
	"testing"
)

// The goldens are the exact text MCP clients receive as server instructions.
// A change to the guidance must update them deliberately.
func TestGeneralGuidanceMatchesGoldens(t *testing.T) {
	cases := []struct {
		name   string
		writes bool
		got    string
		golden string
	}{
		{"full", true, GeneralGuidanceFull, "testdata/general_guidance_full.golden"},
		{"read-only", false, GeneralGuidanceReadOnly, "testdata/general_guidance_readonly.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatal(err)
			}
			if tc.got != string(want) {
				t.Fatalf("%s guidance changed:\n got %q\nwant %q", tc.name, tc.got, want)
			}
			if GeneralGuidance(tc.writes) != tc.got {
				t.Fatalf("GeneralGuidance(%v) does not return the %s constant", tc.writes, tc.name)
			}
		})
	}
}
