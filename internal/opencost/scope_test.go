package opencost

import "testing"

func TestScopeRestricted(t *testing.T) {
	tests := []struct {
		name       string
		namespaces []string
		want       bool
	}{
		{name: "nil is unrestricted", namespaces: nil, want: false},
		{name: "empty non-nil is restricted", namespaces: []string{}, want: true},
		{name: "populated is restricted", namespaces: []string{"team-a"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Scope{Namespaces: tt.namespaces}).Restricted(); got != tt.want {
				t.Errorf("Restricted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeAllows(t *testing.T) {
	tests := []struct {
		name       string
		namespaces []string
		ns         string
		want       bool
	}{
		{name: "unrestricted allows anything", namespaces: nil, ns: "team-b", want: true},
		{name: "in scope", namespaces: []string{"team-a", "team-b"}, ns: "team-b", want: true},
		{name: "out of scope", namespaces: []string{"team-a"}, ns: "team-b", want: false},
		{name: "no access at all", namespaces: []string{}, ns: "team-a", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Scope{Namespaces: tt.namespaces}).Allows(tt.ns); got != tt.want {
				t.Errorf("Allows(%q) = %v, want %v", tt.ns, got, tt.want)
			}
		})
	}
}
