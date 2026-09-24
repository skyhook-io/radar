package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecordAPIClassifiesByRoutePattern(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordAPI("POST", "/api/helm/releases/{namespace}/{name}/rollback", 200)
	RecordAPI("POST", "/radar/api/contexts/{name}", 200)       // base path dropped
	RecordAPI("GET", "/api/pods/{namespace}/{name}/exec", 0)   // hijacked WebSocket
	RecordAPI("GET", "/api/pods/{namespace}/{name}/logs", 200) // a read that is the action
	RecordAPI("GET", "/api/dashboard", 200)                    // background polling: ignored
	RecordAPI("GET", "/api/resources/{kind}", 500)             // failed read
	RecordAPI("PUT", "/api/settings", 403)                     // failed change
	RecordAPI("POST", "/api/usage-data/event", 204)            // our own traffic: ignored
	RecordAPI("GET", "/api/pods/prod/payments-db/logs", 200)   // not a pattern shape we trust? still no braces
	RecordAPI("POST", "/api/resources/<script>", 200)          // rejected by the pattern check

	st := h.c.Status()
	want := map[string]int{
		"POST /api/helm/releases/{namespace}/{name}/rollback": 1,
		"POST /api/contexts/{name}":                           1,
		"GET /api/pods/{namespace}/{name}/exec":               1,
		"GET /api/pods/{namespace}/{name}/logs":               1,
	}
	for k, v := range want {
		if st.Preview.Actions[k] != v {
			t.Errorf("actions[%q] = %d, want %d (all: %v)", k, st.Preview.Actions[k], v, st.Preview.Actions)
		}
	}
	if st.Preview.Actions["GET /api/dashboard"] != 0 || st.Preview.Actions["POST /api/usage-data/event"] != 0 {
		t.Errorf("polling or usage-data traffic counted: %v", st.Preview.Actions)
	}
	if st.Preview.Errors["GET /api/resources/{kind} 5xx"] != 1 || st.Preview.Errors["PUT /api/settings 4xx"] != 1 {
		t.Errorf("errors = %v", st.Preview.Errors)
	}
	if _, bad := st.Preview.Actions["POST /api/resources/<script>"]; bad {
		t.Errorf("unsafe pattern recorded")
	}
}

func TestViewsAcceptBuiltinKindsOnly(t *testing.T) {
	if !IsAllowedView("resources:deployments") || !IsAllowedView("resources:Pods") {
		t.Fatalf("built-in kinds should be countable")
	}
	for _, v := range []string{"resources:certificates", "resources:my-internal-crds", "resources:", "/resources/secrets/prod"} {
		if IsAllowedView(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

func TestUIEvents(t *testing.T) {
	if !IsAllowedUIEvent("command_palette") || !IsAllowedUIEvent("ui_error:TopologyView") {
		t.Fatalf("known events rejected")
	}
	for _, v := range []string{"ui_error:", "ui_error:topology view", "ui_error:<svg>", "clicked prod-db"} {
		if IsAllowedUIEvent(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

func TestBuckets(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 2: "2", 3: "3-4", 4: "3-4", 9: "5-9", 11: "10-19", 49: "20-49", 1500: "1000-1999", 50000: "50000+", -3: "0"}
	for n, want := range cases {
		if got := Bucket(n); got != want {
			t.Errorf("Bucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMinorVersionDropsVendorSuffix(t *testing.T) {
	for in, want := range map[string]string{"v1.30.4-eks-a1b2c3": "1.30", "1.36.1": "1.36", "v1.29.2+k3s1": "1.29", "": "unknown"} {
		if got := MinorVersion(in); got != want {
			t.Errorf("MinorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrowserFamily(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/128.0 Safari/537.36":           "chrome",
		"Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/128.0 Safari/537.36 Edg/128.0": "edge",
		"Mozilla/5.0 (Macintosh) Gecko/20100101 Firefox/130.0":                            "firefox",
		"Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15":       "safari",
		"curl/8.4": "other",
	} {
		if got := BrowserFamily(ua); got != want {
			t.Errorf("BrowserFamily(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestReportCarriesNothingThatLinksReports(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	RecordView("helm")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("sent %d reports, want 1", len(h.sent))
	}
	body, err := json.Marshal(h.sent[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"installId", `"id"`, "pods", "namespaces", "crds", "aiAgents", "authPlugins", "kube-system-uid-1"} {
		if strings.Contains(string(body), field) {
			t.Errorf("report carries %s: %s", field, body)
		}
	}
	if h.saved.UsageData == nil || !h.saved.UsageData.Enabled {
		t.Fatalf("choice not saved")
	}
	raw, err := json.Marshal(h.saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "usageIdentity") {
		t.Errorf("settings keep an identity: %s", raw)
	}
}

func TestClusterShapesSampledPerCluster(t *testing.T) {
	releaseBuild(t)
	h := newHarness(t, nil, false)
	if _, err := h.c.SetChoice(true, ""); err != nil {
		t.Fatal(err)
	}
	current := "uid-a"
	h.c.opts.ClusterSample = func() (string, ClusterShape, bool) {
		return current, ClusterShape{Platform: current}, true
	}
	h.c.tick(context.Background())
	current = "uid-b"
	h.c.tick(context.Background())
	RecordView("home")
	h.now = h.now.Add(reportInterval + time.Minute)
	h.c.tick(context.Background())
	if len(h.sent) != 1 || h.sent[0].Clusters.Used != 2 {
		t.Fatalf("clusters = %+v", h.sent[0].Clusters)
	}
	for _, s := range h.sent[0].Clusters.Shapes {
		if s.Platform != "uid-a" && s.Platform != "uid-b" {
			t.Fatalf("unexpected shape %+v", s)
		}
	}
}
