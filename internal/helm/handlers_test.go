package helm

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"helm.sh/helm/v3/pkg/storage/driver"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestChartSourceRoutesAndWriteAuthorization(t *testing.T) {
	old := globalClient
	globalClient = nil
	defer func() { globalClient = old }()
	router := chi.NewRouter()
	NewHandlers(nil).RegisterRoutes(router)

	get := httptest.NewRequest(http.MethodGet, "/helm/releases/default/example/source", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET source status = %d, want registered route status %d", getRec.Code, http.StatusServiceUnavailable)
	}

	// Both writes change a release Secret, so a caller whose RBAC denies
	// secrets/create in the release namespace is refused before any client
	// or network work.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			writeK8sStatus(t, w, http.StatusNotFound, "NotFound", "unexpected test request")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeTestJSON(t, w, authv1.SubjectAccessReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"},
			Status:   authv1.SubjectAccessReviewStatus{Allowed: false},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatalf("create test client: %v", err)
	}
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })
	prevStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevStatus) })

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/helm/releases/default/example/source", `{"type":"repository","reference":"repo","url":"https://example.test"}`},
		{http.MethodPost, "/helm/repositories", `{"name":"repo","url":"https://example.test","namespace":"default","releaseName":"example"}`},
		{http.MethodPost, "/helm/releases/default/example/source-discovery/artifacthub", ``},
		{http.MethodPost, "/helm/releases/default/example/source-recovery/artifacthub", `{"type":"repository","reference":"repo","url":"https://example.test"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "chart-source-viewer", Groups: []string{"radar:viewer"}}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want RBAC-gated 403 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestChartSourceErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid source", err: fmt.Errorf("%w: bad selection", errInvalidChartSource), want: http.StatusBadRequest},
		{name: "invalid repository", err: fmt.Errorf("%w: bad URL", errInvalidRepositoryRequest), want: http.StatusBadRequest},
		{name: "alias conflict", err: fmt.Errorf("%w: alias owned", errRepositoryConflict), want: http.StatusConflict},
		{name: "missing release", err: fmt.Errorf("get release: %w", driver.ErrReleaseNotFound), want: http.StatusNotFound},
		{name: "kubernetes denial", err: fmt.Errorf("update release: %w", apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "sh.helm.release.v1.example.v1", errors.New("denied"))), want: http.StatusForbidden},
		{name: "repository 401", err: errors.New("failed to fetch https://charts.example.test/index.yaml : 401 Unauthorized"), want: http.StatusInternalServerError},
		{name: "ArtifactHub repository authentication", err: &artifactHubVerificationError{kind: "authentication", err: errors.New("401 Unauthorized")}, want: http.StatusUnprocessableEntity},
		{name: "ArtifactHub infrastructure", err: &artifactHubVerificationError{kind: "infrastructure", err: errors.New("timeout")}, want: http.StatusBadGateway},
		{name: "storage failure", err: errors.New("storage unavailable"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chartSourceErrorStatus(tc.err); got != tc.want {
				t.Fatalf("chartSourceErrorStatus(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestDecodeOptionalApplyValuesRequest(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		hasBody bool
		want    map[string]any
		wantErr bool
	}{
		{name: "nil body"},
		{name: "empty body", hasBody: true},
		{name: "explicit empty values stays non nil", body: `{"values":{}}`, hasBody: true, want: map[string]any{}},
		{name: "populated values", body: `{"values":{"replicaCount":2}}`, hasBody: true, want: map[string]any{"replicaCount": float64(2)}},
		{name: "invalid json", body: `{"values":`, hasBody: true, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.hasBody {
				body = strings.NewReader(tc.body)
			}

			got, err := decodeOptionalApplyValuesRequest(body)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeOptionalApplyValuesRequest returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("values = %#v, want %#v", got, tc.want)
			}
			if tc.want != nil && got == nil {
				t.Fatal("values = nil, want explicit non-nil map")
			}
		})
	}
}

// TestDecodeApplyValuesRequest pins the apply contract: the endpoint refuses
// a chart-version change instead of silently dropping it. Preview accepts
// Version/Repository and renders against the target chart, so the same body
// sent to apply must fail loudly; apply always targets the release's current
// chart, and version changes belong to the upgrade endpoints.
func TestDecodeApplyValuesRequest(t *testing.T) {
	rejected := []struct {
		name string
		body string
	}{
		{"version set", `{"values":{"a":1},"version":"1.1.0"}`},
		{"repository set", `{"values":{"a":1},"repository":"my-repo"}`},
		{"both set", `{"values":{"a":1},"version":"1.1.0","repository":"my-repo"}`},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeApplyValuesRequest(strings.NewReader(tc.body))
			if err == nil {
				t.Fatal("expected rejection (version/repository must be refused, not dropped)")
			}
			if !strings.Contains(err.Error(), "upgrade endpoint") {
				t.Errorf("error = %q, want it to point at the upgrade endpoint", err.Error())
			}
		})
	}

	t.Run("values only accepted", func(t *testing.T) {
		req, err := decodeApplyValuesRequest(strings.NewReader(`{"values":{"a":1}}`))
		if err != nil {
			t.Fatalf("decodeApplyValuesRequest returned error: %v", err)
		}
		if !reflect.DeepEqual(req.Values, map[string]any{"a": float64(1)}) {
			t.Fatalf("values = %#v, want the decoded map", req.Values)
		}
	})

	t.Run("malformed body rejected", func(t *testing.T) {
		_, err := decodeApplyValuesRequest(strings.NewReader(`{"values":`))
		if err == nil {
			t.Fatal("expected error for malformed JSON")
		}
		if !strings.Contains(err.Error(), "invalid request body") {
			t.Errorf("error = %q, want an invalid-request-body message", err.Error())
		}
	})
}

// TestHelmHandlers_NotGatedOnCloudRole pins that Helm operations are not
// refused on the caller's Radar Cloud role. They run as the impersonated user,
// so Kubernetes RBAC decides; a role check here would override IdP-group
// bindings that grant the user access (a viewer whose group can edit a
// namespace must be able to roll back there). Other refusals (no Helm client,
// rbac.helm off) are fine here; only a role refusal fails.
func TestHelmHandlers_NotGatedOnCloudRole(t *testing.T) {
	h := NewHandlers(nil)

	cases := []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{"GetManifest", http.MethodGet, h.handleGetManifest},
		{"GetValues", http.MethodGet, h.handleGetValues},
		{"GetValuesDiff", http.MethodGet, h.handleGetValuesDiff},
		{"GetDiff", http.MethodGet, h.handleGetDiff},
		{"GetNotesDiff", http.MethodGet, h.handleGetNotesDiff},
		{"GetHooksDiff", http.MethodGet, h.handleGetHooksDiff},
		{"GetResourceDiff", http.MethodGet, h.handleGetResourceDiff},
		{"PreviewValues", http.MethodPost, h.handlePreviewValues},
		{"ApplyValues", http.MethodPut, h.handleApplyValues},
		{"Rollback", http.MethodPost, h.handleRollback},
		{"RollbackStream", http.MethodPost, h.handleRollbackStream},
		{"Uninstall", http.MethodDelete, h.handleUninstall},
		{"Upgrade", http.MethodPost, h.handleUpgrade},
		{"UpgradeStream", http.MethodPost, h.handleUpgradeStream},
		{"Install", http.MethodPost, h.handleInstall},
		{"InstallStream", http.MethodPost, h.handleInstallStream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/test", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{
				Username: "viewer-test",
				Groups:   []string{"radar:viewer", "radar:idp:team-a-devs"},
			}))
			rec := httptest.NewRecorder()

			tc.handler(rec, req)

			body := rec.Body.String()
			if strings.Contains(body, auth.ErrCodeCloudRoleInsufficient) || strings.Contains(body, "Radar Cloud role") {
				t.Fatalf("status = %d body = %s; a Cloud viewer must reach Kubernetes RBAC, not a role gate", rec.Code, body)
			}
		})
	}
}

// A release read runs as the caller, so a Kubernetes denial is a 403 the UI
// can explain, not a 500 recorded as a server fault.
func TestWriteReleaseReadError(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New(`secrets is forbidden: User "alice" cannot list resource "secrets" in API group "" in the namespace "team-a"`), http.StatusForbidden},
		{errors.New("release: not found"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeReleaseReadError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("%q: status = %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}
