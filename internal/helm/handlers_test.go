package helm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

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
