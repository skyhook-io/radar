package opencost

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScopeForIsUnrestrictedWithoutResolver(t *testing.T) {
	t.Cleanup(func() { SetScopeResolver(nil) })
	SetScopeResolver(nil)

	scope := scopeFor(httptest.NewRequest(http.MethodGet, "/opencost/summary", nil))
	if scope.Namespaces != nil {
		t.Errorf("Namespaces = %v, want nil (unrestricted)", scope.Namespaces)
	}
	if !scope.CanReadNodes {
		t.Error("CanReadNodes = false, want true without a resolver")
	}
	if scope.Restricted() {
		t.Error("Restricted() = true, want false without a resolver")
	}
}

func TestScopeForUsesInstalledResolver(t *testing.T) {
	t.Cleanup(func() { SetScopeResolver(nil) })
	SetScopeResolver(func(*http.Request) Scope {
		return Scope{Namespaces: []string{"team-a"}, CanReadNodes: false}
	})

	scope := scopeFor(httptest.NewRequest(http.MethodGet, "/opencost/summary", nil))
	if len(scope.Namespaces) != 1 || scope.Namespaces[0] != "team-a" {
		t.Errorf("Namespaces = %v, want [team-a]", scope.Namespaces)
	}
	if scope.CanReadNodes {
		t.Error("CanReadNodes = true, want false")
	}
	if !scope.Restricted() {
		t.Error("Restricted() = false, want true")
	}
}

// A resolver left installed by one test would silently gate every later test in
// the package; SetScopeResolver(nil) must actually clear it.
func TestSetScopeResolverNilClearsPreviousResolver(t *testing.T) {
	t.Cleanup(func() { SetScopeResolver(nil) })
	SetScopeResolver(func(*http.Request) Scope { return Scope{Namespaces: []string{}} })

	r := httptest.NewRequest(http.MethodGet, "/opencost/summary", nil)
	if scopeFor(r).Namespaces == nil {
		t.Fatal("resolver was not installed")
	}

	SetScopeResolver(nil)
	if scopeFor(r).Namespaces != nil {
		t.Error("scopeFor should be unrestricted after SetScopeResolver(nil)")
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
