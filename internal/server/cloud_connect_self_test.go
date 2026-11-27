package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/k8s"
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
	if q.Get("utm_content") != "wizard-install-link-known-install" {
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
		if u.Query().Get("utm_content") != "wizard-install-link-unknown-install" {
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

// setSelfServiceAccount installs Radar's own client, answering only the NKP
// namespace read, so a test can tell it apart from the signed-in user's.
func setSelfServiceAccount(t *testing.T, nkp bool) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/kommander-flux" || !nkp {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(corev1.Namespace{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: "kommander-flux"},
		})
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	// Hide the shared fixture's cache: its namespace list would answer the
	// NKP question before the ServiceAccount client is asked.
	restore := k8s.SwapResourceCacheForTest(nil)
	k8s.SetTestClient(client)
	k8s.InvalidatePlatformCache()
	t.Cleanup(func() {
		restore()
		k8s.InvalidatePlatformCache()
	})
}

func TestRouteNKPSendsNKPClustersToTheNKPSteps(t *testing.T) {
	s := &Server{cloudConnectCfg: CloudConnectConfig{HubAppURL: "https://app.radarhq.io"}}
	flux := "https://app.radarhq.io/install?existing=1&method=flux&ns=ws&release=ws-radar"
	for _, tc := range []struct {
		name string
		nkp  bool
		in   cloudConnectSelf
	}{
		{name: "NKP, install recognized", nkp: true, in: cloudConnectSelf{Ownership: "gitops", WizardURL: flux}},
		{name: "NKP, install not recognized", nkp: true, in: cloudConnectSelf{Ownership: "unknown"}},
		{name: "not NKP", nkp: false, in: cloudConnectSelf{Ownership: "gitops", WizardURL: flux}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setSelfServiceAccount(t, tc.nkp)
			self := tc.in
			s.routeNKP(context.Background(), &self)
			if !tc.nkp {
				if self.Platform != "" || self.WizardURL != flux {
					t.Fatalf("non-NKP cluster changed: %#v", self)
				}
				return
			}
			u, err := url.Parse(self.WizardURL)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if self.Platform != "nkp" || u.Path != "/install" || q.Get("platform") != "nkp" || q.Get("clusters") != "many" || q.Has("existing") {
				t.Fatalf("NKP cluster got %#v", self)
			}
		})
	}
}
