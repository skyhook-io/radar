package server

import (
	"net/url"
	"strings"
	"testing"
)

func TestWizardInstallURLCarriesTheRealTarget(t *testing.T) {
	srv := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.test.example"}}
	got := srv.wizardInstallURL("observability", "prod")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("ns") != "observability" || q.Get("release") != "prod" || q.Get("existing") != "1" {
		t.Fatalf("query = %v", q)
	}
	if u.Host != "app.test.example" || u.Path != "/install" {
		t.Fatalf("url = %q", got)
	}
}

// A namespace or release with URL-significant characters must not be able to
// forge extra query parameters in the wizard link.
func TestWizardInstallURLEscapesTarget(t *testing.T) {
	srv := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.test.example"}}
	got := srv.wizardInstallURL("ns&existing=0", "rel#frag")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Query().Get("existing") != "1" {
		t.Fatalf("injected parameter overrode existing: %v", u.Query())
	}
	if u.Query().Get("ns") != "ns&existing=0" || u.Query().Get("release") != "rel#frag" {
		t.Fatalf("round-trip lost the target: %v", u.Query())
	}
	if strings.Contains(u.RawQuery, "#") {
		t.Fatalf("fragment leaked into the query: %q", u.RawQuery)
	}
}

// Every failure path must degrade to a generic wizard link rather than a
// confident wrong answer — a bad namespace/release would send the operator to
// a command that targets someone else's release.
func TestInspectSelfInstallDegradesWithoutNamespace(t *testing.T) {
	srv := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.test.example"}}
	self := srv.inspectSelfInstall(t.Context(), "")
	if self.Ownership != "unknown" || self.Namespace != "" || self.Release != "" {
		t.Fatalf("self = %+v", self)
	}
	if self.WizardURL != "https://app.test.example/install" {
		t.Fatalf("wizard url = %q", self.WizardURL)
	}
}
