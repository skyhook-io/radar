package usagedata

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceAndPanelViews(t *testing.T) {
	for _, v := range []string{"cnpg", "capacity", "panel:logs", "panel:yaml", "panel:metrics", "panel:events"} {
		if !IsAllowedView(v) {
			t.Errorf("%q should be countable", v)
		}
	}
	for _, v := range []string{"panel:permissions", "panel:gitops", "panel:", "panel:prod-db", "cnpg:prod"} {
		if IsAllowedView(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

func TestIssueActionsAndWorkspaceCrashes(t *testing.T) {
	for _, v := range []string{"issue_action:open", "issue_action:investigate", "issue_action:capacity", "ui_error:CloudNativePG", "ui_error:Capacity"} {
		if !IsAllowedUIEvent(v) {
			t.Errorf("%q should be countable", v)
		}
	}
	for _, v := range []string{"issue_action:", "issue_action:delete", "issue_action:prod-db", "ui_error:cnpg"} {
		if IsAllowedUIEvent(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

func TestErrorStatusesExactOnlyFromFixedSet(t *testing.T) {
	for status, want := range map[int]string{
		401: "401", 403: "403", 404: "404", 409: "409", 429: "429",
		500: "500", 502: "502", 503: "503", 504: "504",
		400: "4xx", 413: "4xx", 418: "4xx", 422: "4xx", 501: "5xx", 507: "5xx", 599: "5xx",
	} {
		if got := strconvStatusClass(status); got != want {
			t.Errorf("strconvStatusClass(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestAutomaticAndNotConnectedRequests(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/prometheus/connect", Status: 200, Automatic: true})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/whats-new/seen", Status: 204, Automatic: true})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/connection/retry", Status: 503, Automatic: true})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/prometheus/connect", Status: 200})
	RecordRequest(APIRequest{Method: "GET", Pattern: "/api/dashboard", Status: 503, NotConnected: true})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/workloads/{kind}/{namespace}/{name}/restart", Status: 503, NotConnected: true})
	RecordRequest(APIRequest{Method: "GET", Pattern: "/api/dashboard", Status: 503})

	st := h.c.Status()
	wantActions := map[string]int{"POST /api/prometheus/connect": 1}
	if len(st.Preview.Actions) != len(wantActions) || st.Preview.Actions["POST /api/prometheus/connect"] != 1 {
		t.Errorf("actions = %v, want %v", st.Preview.Actions, wantActions)
	}
	wantErrors := map[string]int{
		"POST /api/connection/retry 503": 1, // an automatic request that fails still counts
		"GET /api/dashboard 503":         1, // a 503 that isn't "not connected" counts
	}
	if len(st.Preview.Errors) != len(wantErrors) {
		t.Errorf("errors = %v, want %v", st.Preview.Errors, wantErrors)
	}
	for k, v := range wantErrors {
		if st.Preview.Errors[k] != v {
			t.Errorf("errors[%q] = %d, want %d (all: %v)", k, st.Preview.Errors[k], v, st.Preview.Errors)
		}
	}
}

func TestActionKindsNameBuiltinKindsOnly(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	del := "/api/resources/{kind}/{namespace}/{name}"
	restart := "/api/workloads/{kind}/{namespace}/{name}/restart"
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "pods"})
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "Pods"})
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "configmaps", Group: ""})
	RecordRequest(APIRequest{Method: "POST", Pattern: restart, Status: 200, Kind: "deployments", Group: "apps"})
	// Never named: custom kinds, a custom group sharing a built-in name,
	// failures, and routes that don't count kinds, such as restart.
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "certificates", Group: "cert-manager.io"})
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "acme-payments-dbs"})
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 200, Kind: "services", Group: "serving.knative.dev"})
	RecordRequest(APIRequest{Method: "POST", Pattern: restart, Status: 200, Kind: "rollouts", Group: "argoproj.io"})
	RecordRequest(APIRequest{Method: "DELETE", Pattern: del, Status: 403, Kind: "secrets"})
	RecordRequest(APIRequest{Method: "PUT", Pattern: del, Status: 200, Kind: "pods"})
	RecordRequest(APIRequest{Method: "POST", Pattern: restart, Status: 200, Kind: "deployments", Automatic: true})

	st := h.c.Status()
	want := map[string]int{
		"delete:pods":       2,
		"delete:configmaps": 1,
	}
	if len(st.Preview.ActionKinds) != len(want) {
		t.Errorf("actionKinds = %v, want %v", st.Preview.ActionKinds, want)
	}
	for k, v := range want {
		if st.Preview.ActionKinds[k] != v {
			t.Errorf("actionKinds[%q] = %d, want %d (all: %v)", k, st.Preview.ActionKinds[k], v, st.Preview.ActionKinds)
		}
	}
	// The custom-kind deletes still count as actions, by route only.
	if st.Preview.Actions["DELETE "+del] != 6 {
		t.Errorf("delete actions = %d, want 6", st.Preview.Actions["DELETE "+del])
	}
}

func TestConnectionFailuresUseFixedVocabulary(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	RecordConnectionFailure("auth-plugin-stuck")
	RecordConnectionFailure("tls")
	RecordConnectionFailure("tls")
	RecordConnectionFailure("")
	RecordConnectionFailure("dial tcp 10.0.0.1:443: connect: connection refused")

	st := h.c.Status()
	want := map[string]int{"auth-plugin-stuck": 1, "tls": 2, "unknown": 2}
	if len(st.Preview.ConnectionFailures) != len(want) {
		t.Errorf("connectionFailures = %v, want %v", st.Preview.ConnectionFailures, want)
	}
	for k, v := range want {
		if st.Preview.ConnectionFailures[k] != v {
			t.Errorf("connectionFailures[%q] = %d, want %d", k, st.Preview.ConnectionFailures[k], v)
		}
	}

	// Connection failures alone are not use: Radar sends nothing.
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("sent a report for a day with only connection failures: %+v", h.sent)
	}
}

func TestErrorsAloneAreNotUse(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/connection/retry", Status: 503, Automatic: true})
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("sent a report for a day with only failed requests: %+v", h.sent)
	}

	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/connection/retry", Status: 503})
	RecordSession("Mozilla/5.0 Chrome/130.0")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 || h.sent[0].Errors["POST /api/connection/retry 503"] != 1 {
		t.Fatalf("sent %+v, want one report carrying the failed retry", h.sent)
	}
}

func TestTrafficSourceVocabulary(t *testing.T) {
	for in, want := range map[string]string{"hubble": "hubble", "caretta": "caretta", "istio": "istio", "beyla": "beyla", "": "none", "acme-mesh": "none"} {
		if got := TrafficSource(in); got != want {
			t.Errorf("TrafficSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSchema4Report(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	h.c.opts.Setup = func() Setup {
		return Setup{
			AuthMode: "none", TimelineStorage: "memory", MCPEnabled: true, Prometheus: "discovered",
			CostSource: "auto", AIConfigured: true, TrafficSource: "hubble",
		}
	}
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	RecordSession("Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/128.0 Safari/537.36")
	RecordView("cnpg")
	RecordView("panel:logs")
	RecordUIEvent("issue_action:investigate")
	RecordActive(5)
	RecordRequest(APIRequest{Method: "DELETE", Pattern: "/api/resources/{kind}/{namespace}/{name}", Status: 200, Kind: "pods"})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/workloads/{kind}/{namespace}/{name}/restart", Status: 404, Kind: "deployments"})
	RecordConnectionFailure("timeout")
	h.c.tick(context.Background())
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("sent %d reports, want 1", len(h.sent))
	}
	r := h.sent[0]
	if r.Schema != 4 {
		t.Errorf("schema = %d, want 4", r.Schema)
	}
	if !r.Setup.AIConfigured || r.Setup.TrafficSource != "hubble" || r.Setup.Prometheus != "discovered" {
		t.Errorf("setup = %+v", r.Setup)
	}
	if r.ActionKinds["delete:pods"] != 1 || r.ConnectionFailures["timeout"] != 1 {
		t.Errorf("actionKinds = %v, connectionFailures = %v", r.ActionKinds, r.ConnectionFailures)
	}
	if r.Errors["POST /api/workloads/{kind}/{namespace}/{name}/restart 404"] != 1 {
		t.Errorf("errors = %v", r.Errors)
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"schema": 4`, `"aiConfigured": true`, `"trafficSource": "hubble"`, `"actionKinds"`, `"connectionFailures"`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("report lacks %s", key)
		}
	}
	t.Logf("example schema 4 report:\n%s", body)

	// Empty maps encode as {}, never null, so the receiver sees every key.
	empty, _ := json.Marshal(h.c.Status().Preview)
	for _, key := range []string{`"actionKinds":{}`, `"connectionFailures":{}`} {
		if !strings.Contains(string(empty), key) {
			t.Errorf("empty preview lacks %s: %s", key, empty)
		}
	}
}

func TestAutomaticReadRoutesAreNotActions(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true); err != nil {
		t.Fatal(err)
	}
	for pattern := range automaticReads {
		method, path, _ := strings.Cut(pattern, " ")
		RecordRequest(APIRequest{Method: method, Pattern: path, Status: 200})
	}
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/nodes/{name}/drain-plan", Status: 500})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/version-check/browser", Status: 200})
	RecordRequest(APIRequest{Method: "POST", Pattern: "/api/nodes/{name}/drain", Status: 200})

	st := h.c.Status()
	if len(st.Preview.Actions) != 1 || st.Preview.Actions["POST /api/nodes/{name}/drain"] != 1 {
		t.Errorf("actions = %v, want only the drain itself", st.Preview.Actions)
	}
	if st.Preview.Errors["POST /api/nodes/{name}/drain-plan 500"] != 1 {
		t.Errorf("errors = %v, want the failed drain estimate counted", st.Preview.Errors)
	}
}
