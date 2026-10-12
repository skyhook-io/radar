package configrefs

import "testing"

func TestContourSecretRef(t *testing.T) {
	for _, tt := range []struct{ ref, wantNs, wantName string }{
		{ref: "cert", wantNs: "edge", wantName: "cert"},
		{ref: "certs/wildcard", wantNs: "certs", wantName: "wildcard"},
		{ref: "/cert", wantNs: "edge", wantName: "cert"},
		{ref: "certs/a/b", wantNs: "certs", wantName: "a/b"},
	} {
		ns, name := ContourSecretRef("edge", tt.ref)
		if ns != tt.wantNs || name != tt.wantName {
			t.Errorf("ContourSecretRef(edge, %q) = %s/%s, want %s/%s", tt.ref, ns, name, tt.wantNs, tt.wantName)
		}
	}
}
