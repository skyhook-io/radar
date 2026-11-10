package helm

import (
	"testing"
)

func TestNormalizeOCIPrefix(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "oci://ghcr.io/myorg/charts", want: "oci://ghcr.io/myorg/charts"},
		{name: "trailing slash trimmed", in: "oci://ghcr.io/myorg/charts/", want: "oci://ghcr.io/myorg/charts"},
		{name: "whitespace trimmed", in: "  oci://ghcr.io/myorg/charts  ", want: "oci://ghcr.io/myorg/charts"},
		{name: "host only", in: "oci://ghcr.io", want: "oci://ghcr.io"},
		{name: "missing scheme", in: "ghcr.io/myorg/charts", wantErr: true},
		{name: "http scheme rejected", in: "https://charts.example.com", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "scheme only", in: "oci://", wantErr: true},
		{name: "metadata IP blocked", in: "oci://169.254.169.254/charts", wantErr: true},
		{name: "link-local with port blocked", in: "oci://169.254.0.1:5000/charts", wantErr: true},
		{name: "IPv6 link-local blocked", in: "oci://[fe80::1]/charts", wantErr: true},
		{name: "IPv6 link-local with port blocked", in: "oci://[fe80::1]:5000/charts", wantErr: true},
		{name: "IPv6 link-local with zone blocked", in: "oci://[fe80::1%25eth0]:5000/charts", wantErr: true},
		{name: "loopback allowed (local dev registry)", in: "oci://localhost:5000/charts", want: "oci://localhost:5000/charts"},
		{name: "private IP allowed (local dev registry)", in: "oci://10.0.0.5:5000/charts", want: "oci://10.0.0.5:5000/charts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOCIPrefix(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeOCIPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOCIRefAndURL(t *testing.T) {
	prefix := "oci://ghcr.io/myorg/charts"
	if got, want := ociRef(prefix, "mychart"), "ghcr.io/myorg/charts/mychart"; got != want {
		t.Errorf("ociRef = %q, want %q (registry client wants no scheme)", got, want)
	}
	if got, want := ociChartURL(prefix, "mychart"), "oci://ghcr.io/myorg/charts/mychart"; got != want {
		t.Errorf("ociChartURL = %q, want %q (LocateChart wants the scheme)", got, want)
	}
}

// fakeTagLister returns canned tags per ref and records calls.
type fakeTagLister struct {
	tags  map[string][]string
	errs  map[string]error
	calls []string
}

func (f *fakeTagLister) Tags(ref string) ([]string, error) {
	f.calls = append(f.calls, ref)
	if err, ok := f.errs[ref]; ok {
		return nil, err
	}
	return f.tags[ref], nil
}

func TestSortVersionsDesc(t *testing.T) {
	in := []string{"1.2.0", "1.10.0", "1.2.3", "0.9.0", "2.0.0"}
	got := sortVersionsDesc(in)
	want := []string{"2.0.0", "1.10.0", "1.2.3", "1.2.0", "0.9.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (semver order, not lexical)", got, want)
		}
	}
	// Input must not be mutated.
	if in[0] != "1.2.0" {
		t.Errorf("sortVersionsDesc mutated its input: %v", in)
	}
}

func TestCapVersions(t *testing.T) {
	short := []string{"3.0.0", "2.0.0", "1.0.0"}
	if got := capVersions(short); len(got) != 3 {
		t.Errorf("short list should pass through, got %d", len(got))
	}

	many := make([]string, maxAvailableVersions+25)
	for i := range many {
		many[i] = "v"
	}
	got := capVersions(many)
	if len(got) != maxAvailableVersions {
		t.Errorf("capped list = %d, want %d", len(got), maxAvailableVersions)
	}
}

// withOCISources points settings at a temp HOME and seeds the registered OCI
// sources for the duration of the test.
func withOCISources(t *testing.T, sources []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, s := range sources {
		if _, err := AddOCISource(s); err != nil {
			t.Fatalf("seeding source %q: %v", s, err)
		}
	}
}
