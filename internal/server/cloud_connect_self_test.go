package server

import (
	"net/http"
	"net/http/httptest"
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
	if q.Get("utm_content") != "wizard-deeplink" {
		t.Fatalf("deep link missing lane marker: %v", q)
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
// a command that targets someone else's release. Identity comes from the
// downward API, so a missing namespace OR deployment name is unusable.
func TestInspectSelfInstallDegradesWithoutIdentity(t *testing.T) {
	srv := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.test.example"}}
	req := httptest.NewRequest(http.MethodGet, "/api/cloud/connect/self", nil)

	for _, tc := range []struct{ ns, deployment string }{
		{"", ""}, {"radar", ""}, {"", "radar"},
	} {
		self := srv.inspectSelfInstall(t.Context(), req, tc.ns, tc.deployment)
		if self.Ownership != "unknown" || self.Namespace != "" || self.Release != "" {
			t.Fatalf("ns=%q deployment=%q → self = %+v", tc.ns, tc.deployment, self)
		}
		u, err := url.Parse(self.WizardURL)
		if err != nil || u.Host != "app.test.example" || u.Path != "/install" {
			t.Fatalf("wizard url = %q (%v)", self.WizardURL, err)
		}
		if u.Query().Get("utm_content") != "wizard-generic" {
			t.Fatalf("generic wizard link missing lane marker: %q", self.WizardURL)
		}
	}
}
