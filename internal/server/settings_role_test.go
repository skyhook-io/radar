package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
)

// userWithGroups builds an authenticated user carrying the given groups, used
// to drive the Cloud-role gate (radar:<tier> prefix).
func userWithGroups(groups ...string) *auth.User {
	return &auth.User{Username: "u@example.com", Groups: groups}
}

func TestPutConfigPersistsAndAppliesOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	s := &Server{openCostCurrency: internalopencost.NewCurrencyResolver("JPY")}
	r := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"port":9280,"opencostCurrency":" gbp "}`))
	w := httptest.NewRecorder()

	s.handlePutConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenCostCurrency; got != "GBP" {
		t.Errorf("opencostCurrency = %q, want GBP", got)
	}
	if got := s.openCostCurrency.Resolve(); got != "GBP" {
		t.Errorf("running currency = %q, want GBP", got)
	}
}

func TestPutConfigPreservesEmptyOpenCostCurrencyAsAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	if err := config.Save(config.Config{OpenCostCurrency: "GBP"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{openCostCurrency: internalopencost.NewCurrencyResolver("GBP")}
	r := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"opencostCurrency":""}`))
	w := httptest.NewRecorder()

	s.handlePutConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenCostCurrency; got != "" {
		t.Errorf("opencostCurrency = %q, want auto", got)
	}
	if got := s.openCostCurrency.Resolve(); got != "USD" {
		t.Errorf("running currency = %q, want auto fallback USD", got)
	}
}

func TestPutConfigDoesNotReplaceManagedOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	s := &Server{
		openCostCurrency: internalopencost.NewCurrencyResolver("GBP"),
		currencyManaged:  true,
	}
	r := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"opencostCurrency":"JPY"}`))
	w := httptest.NewRecorder()

	s.handlePutConfig(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenCostCurrency; got != "JPY" {
		t.Errorf("persisted opencostCurrency = %q, want JPY", got)
	}
	if got := s.openCostCurrency.Resolve(); got != "GBP" {
		t.Errorf("running currency = %q, want managed GBP", got)
	}
}

func TestGetConfigReportsManagedOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Server{
		effectiveConfig: &config.Config{OpenCostCurrency: "GBP"},
		currencyManaged: true,
	}
	w := httptest.NewRecorder()

	s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Effective struct {
			OpenCostCurrency string `json:"opencostCurrency"`
		} `json:"effective"`
		Managed bool `json:"openCostCurrencyManaged"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Managed || body.Effective.OpenCostCurrency != "GBP" {
		t.Fatalf("managed config response = %+v, want managed GBP", body)
	}
}

func TestGetConfigNormalizesPersistedOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(config.Config{OpenCostCurrency: " eur "}); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	w := httptest.NewRecorder()

	s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		File struct {
			OpenCostCurrency string `json:"opencostCurrency"`
		} `json:"file"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.File.OpenCostCurrency != "EUR" {
		t.Fatalf("file opencostCurrency = %q, want EUR", body.File.OpenCostCurrency)
	}
}

func TestGetConfigClearsInvalidPersistedOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	if err := config.Save(config.Config{OpenCostCurrency: "EURO"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	w := httptest.NewRecorder()

	s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		File config.Config `json:"file"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.File.OpenCostCurrency != "" {
		t.Fatalf("file opencostCurrency = %q, want auto", body.File.OpenCostCurrency)
	}

	requestBody, err := json.Marshal(body.File)
	if err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRecorder()
	s.handlePutConfig(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(requestBody))))
	if put.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", put.Code, put.Body.String())
	}
	if got := config.Load().OpenCostCurrency; got != "" {
		t.Fatalf("saved opencostCurrency = %q, want auto", got)
	}
}

func TestPutConfigRejectsInvalidOpenCostCurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	s := &Server{}
	r := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"opencostCurrency":"EURO"}`))
	w := httptest.NewRecorder()

	s.handlePutConfig(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenCostCurrency; got != "" {
		t.Errorf("opencostCurrency = %q, want config unchanged", got)
	}
}

func putConfigStatus(t *testing.T, user *auth.User) (int, string) {
	t.Helper()
	// Redirect config persistence to a temp HOME so pass-through cases that
	// reach config.Update() don't clobber the developer's real ~/.radar.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	s := &Server{}
	r := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{}`))
	if user != nil {
		r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	}
	w := httptest.NewRecorder()
	s.handlePutConfig(w, r)
	return w.Code, w.Body.String()
}

func TestPutConfig_RoleGate(t *testing.T) {
	// Non-owner Cloud roles are rejected with the stable error_code so the
	// frontend can branch on it.
	for _, tier := range []string{"radar:viewer", "radar:member"} {
		code, body := putConfigStatus(t, userWithGroups(tier))
		if code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403; body=%s", tier, code, body)
		}
		var resp map[string]string
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s: bad json: %v", tier, err)
		}
		if resp["error_code"] != auth.ErrCodeCloudRoleInsufficient {
			t.Errorf("%s: error_code = %q, want %q", tier, resp["error_code"], auth.ErrCodeCloudRoleInsufficient)
		}
	}
}

func TestPutConfig_OwnerAndOSSBypassRoleGate(t *testing.T) {
	// Owners and non-Cloud callers (no role group → OSS / OIDC / kubectl
	// plugin) must get past the role gate. They may still fail later writing
	// the config file, but they must NOT be 403'd by the gate — a single-user
	// laptop owns its own config.
	cases := []struct {
		name string
		user *auth.User
	}{
		{"owner", userWithGroups("radar:owner")},
		{"no-role-groups", userWithGroups("devs")},
		{"no-user", nil},
	}
	for _, tc := range cases {
		code, body := putConfigStatus(t, tc.user)
		if code == http.StatusForbidden {
			t.Errorf("%s: got 403 from role gate, want pass-through; body=%s", tc.name, body)
		}
	}
}
