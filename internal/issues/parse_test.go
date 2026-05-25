package issues

import "testing"

func TestParseSources(t *testing.T) {
	cases := []struct {
		in   string
		want []Source
	}{
		{"", nil},
		{"scheduling", []Source{SourceScheduling}},
		{"problem,missing_ref,scheduling", []Source{SourceProblem, SourceMissingRef, SourceScheduling}},
		{" scheduling , problem ", []Source{SourceScheduling, SourceProblem}},
	}
	for _, c := range cases {
		got, err := ParseSources(c.in)
		if err != nil {
			t.Fatalf("ParseSources(%q) error: %v", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("ParseSources(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("ParseSources(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseSources_Rejected(t *testing.T) {
	if _, err := ParseSources("audit"); err == nil {
		t.Error("source=audit should be rejected with a redirect to /api/audit")
	}
	if _, err := ParseSources("bogus"); err == nil {
		t.Error("unknown source should be rejected")
	}
}
