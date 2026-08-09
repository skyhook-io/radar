package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/pkg/subject"
)

func TestWizardInstallURLCarriesTheRealTarget(t *testing.T) {
	srv := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.test.example"}}
	got := srv.wizardInstallURL("observability", "prod", "flux")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	q := u.Query()
	if q.Get("ns") != "observability" || q.Get("release") != "prod" || q.Get("existing") != "1" {
		t.Fatalf("query = %v", q)
	}
	if q.Get("method") != "flux" {
		t.Fatalf("deep link did not select the owning tool's tab: %v", q)
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
	got := srv.wizardInstallURL("ns&existing=0", "rel#frag", "helm")

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

// Only a verified controller earns a deep link. Suspected, unreadable, and
// stale all mean "GitOps evidence we could not confirm" — a values patch aimed
// at a controller that may not own this release is exactly the confidently
// wrong answer this endpoint exists to avoid.
func TestWizardMethodOnlyForRecognizedControllers(t *testing.T) {
	for _, tc := range []struct {
		group, kind, want string
	}{
		{"argoproj.io", "Application", "argocd"},
		{"helm.toolkit.fluxcd.io", "HelmRelease", "flux"},
		{"kustomize.toolkit.fluxcd.io", "Kustomization", "flux"},
		// Unrecognized owners get no tab, so the caller withholds the link
		// rather than sending an operator to the wrong artifact.
		{"acme.io", "Whatever", ""},
		{"argoproj.io", "ApplicationSet", ""},
	} {
		t.Run(tc.group+"/"+tc.kind, func(t *testing.T) {
			got := wizardMethodFor(subject.Ref{Group: tc.group, Kind: tc.kind})
			if got != tc.want {
				t.Fatalf("wizardMethodFor(%s/%s) = %q, want %q", tc.group, tc.kind, got, tc.want)
			}
		})
	}
}

// A stale leftover controller can sort ahead of the real owner while
// controllerClassification still reports Verified, so both the displayed owner
// and the wizard tab must come from the verified candidate — not from
// whichever discovery happened to list first.
func TestVerifiedControllerIgnoresCandidateOrder(t *testing.T) {
	candidates := []cloudinstall.ControllerCandidate{
		{
			Ref:          subject.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "stale"},
			Verification: cloudinstall.ControllerStale,
		},
		{
			Ref:          subject.Ref{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "radar"},
			Verification: cloudinstall.ControllerVerified,
		},
	}
	owner := verifiedController(candidates)
	if owner == nil || owner.Ref.Name != "radar" {
		t.Fatalf("owner = %+v, want the verified HelmRelease", owner)
	}
	if got := wizardMethodFor(owner.Ref); got != "flux" {
		t.Fatalf("method = %q, want flux — the stale Argo ref must not pick the tab", got)
	}
	if verifiedController(candidates[:1]) != nil {
		t.Fatal("a stale-only candidate list must not yield an owner")
	}
}
