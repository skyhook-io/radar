package vulnerabilities

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/audit"
)

var (
	privilegedRisk = audit.PodRisk{Factor: audit.PodRiskPrivileged, CheckID: "privileged", Message: `Container "app" runs in privileged mode`}
	hostNetRisk    = audit.PodRisk{Factor: audit.PodRiskHostAccess, CheckID: "hostNetwork", Message: "Pod uses host network"}
	rootRisk       = audit.PodRisk{Factor: audit.PodRiskRunsAsRoot, CheckID: "runAsRoot", Message: `Container "app" runs as root (runAsUser: 0)`}
	mayRootRisk    = audit.PodRisk{Factor: audit.PodRiskMayRunAsRoot, CheckID: "runAsRoot", Message: `Container "app" may run as root (runAsNonRoot not set)`}
)

func risky(t target, risks ...audit.PodRisk) target {
	t.risks = risks
	return t
}

// frontend (privileged) and worker (may run as root) share acme/base, with a
// High; checkout runs acme/checkout, with a Critical, and sets nothing risky.
func riskFixture() ([]*unstructured.Unstructured, []target) {
	items, targets := exposureFixture()
	targets[0] = risky(targets[0], privilegedRisk, mayRootRisk)
	targets[1] = risky(targets[1], mayRootRisk)
	return items, targets
}

// Each row carries its workloads' factors together, may run as root
// included; the answer names each workload with another factor once, with
// the settings behind it, and counts what the privileged filter keeps
// before the filters.
func TestRiskFactorsOnEveryRow(t *testing.T) {
	items, targets := riskFixture()
	both := []RiskFactor{RiskPrivileged, RiskMayRunAsRoot}
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload} {
		resp := mustBuild(t, items, targets, view, Filter{})
		var got [][]RiskFactor
		switch view {
		case ViewImage:
			for _, r := range resp.Images {
				got = append(got, r.RiskFactors)
			}
		case ViewFix:
			for _, r := range resp.Fixes {
				got = append(got, r.RiskFactors)
			}
		case ViewCVE:
			for _, r := range resp.CVEs {
				got = append(got, r.RiskFactors)
			}
		case ViewWorkload:
			for _, r := range resp.Workloads {
				got = append(got, r.RiskFactors)
			}
		}
		want := [][]RiskFactor{nil, both}
		if view == ViewWorkload {
			// checkout, frontend, worker.
			want = [][]RiskFactor{nil, both, {RiskMayRunAsRoot}}
		}
		if !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("%s: rows' factors %v, want %v", view, got, want)
		}
		if resp.PodRisk == nil || resp.PodRisk.Counts[RiskPrivileged] != 1 || resp.PodRisk.Counts[RiskMayRunAsRoot] != 2 {
			t.Fatalf("%s: podRisk %+v", view, resp.PodRisk)
		}
		names := map[string][]RiskFactor{}
		settings := map[string]int{}
		for _, w := range resp.PodRisk.Workloads {
			if w.Container != "" {
				t.Errorf("%s: %+v", view, w)
			}
			names[w.Name], settings[w.Name] = w.Factors, len(w.Settings)
		}
		// Every risky workload is named with all its factors; settings come
		// only with the factors the screen marks, so worker's may has none.
		if len(names) != 2 || !slices.Equal(names["frontend"], both) || !slices.Equal(names["worker"], []RiskFactor{RiskMayRunAsRoot}) || settings["frontend"] != 1 || settings["worker"] != 0 {
			t.Errorf("%s: named %v, settings %v", view, names, settings)
		}
	}
	// The settings are the checks' words, strongest factor first.
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	front := resp.PodRisk.Workloads[slices.IndexFunc(resp.PodRisk.Workloads, func(w RiskyWorkload) bool { return w.Name == "frontend" })]
	if want := []RiskSetting{{RiskPrivileged, "privileged", privilegedRisk.Message}}; !slices.Equal(front.Settings, want) {
		t.Fatalf("settings %+v", front.Settings)
	}
	// Selecting may run as root in the filter brings its settings too.
	resp = mustBuild(t, items, targets, ViewImage, Filter{RiskFactors: []RiskFactor{RiskMayRunAsRoot}})
	front = resp.PodRisk.Workloads[slices.IndexFunc(resp.PodRisk.Workloads, func(w RiskyWorkload) bool { return w.Name == "frontend" })]
	if want := []RiskSetting{{RiskPrivileged, "privileged", privilegedRisk.Message}, {RiskMayRunAsRoot, "runAsRoot", mayRootRisk.Message}}; !slices.Equal(front.Settings, want) {
		t.Fatalf("selected settings %+v", front.Settings)
	}
}

// A workload that runs as root in one Pod doesn't also read "may": the
// stronger claim says it. Replicas merged into one target bring their own
// settings, as a StatefulSet revision that changes only its Pod settings.
func TestRunsAsRootReplacesMay(t *testing.T) {
	items, targets := exposureFixture()
	targets[0] = risky(targets[0], mayRootRisk)
	again := risky(targets[0], rootRisk, hostNetRisk)
	targets = append(targets, again)
	resp := mustBuild(t, items, targets, ViewWorkload, Filter{})
	var front Workload
	for _, w := range resp.Workloads {
		if w.Name == "frontend" {
			front = w
		}
	}
	if !slices.Equal(front.RiskFactors, []RiskFactor{RiskHostAccess, RiskRunsAsRoot}) {
		t.Fatalf("factors %v", front.RiskFactors)
	}
	for _, w := range resp.PodRisk.Workloads {
		if slices.ContainsFunc(w.Settings, func(s RiskSetting) bool { return s.Factor == RiskMayRunAsRoot }) {
			t.Fatalf("may kept beside runs as root: %+v", w.Settings)
		}
	}
	if c := resp.PodRisk.Counts; c[RiskRunsAsRoot] != 1 || c[RiskMayRunAsRoot] != 0 || c[RiskHostAccess] != 1 {
		t.Fatalf("counts %v", c)
	}
	if got := mergeRisks([]audit.PodRisk{mayRootRisk}, []audit.PodRisk{mayRootRisk, rootRisk, rootRisk}); !slices.Equal(got, []audit.PodRisk{mayRootRisk, rootRisk}) {
		t.Fatalf("mergeRisks %v", got)
	}
}

// The risk filter keeps the images a risky workload runs, ANDs with the
// other filters, ORs within itself, and may keeps runs as root too. The
// serious three keep privileged, host access and runs as root, never may
// alone.
func TestRiskFilter(t *testing.T) {
	items, targets := riskFixture()
	privileged := Filter{RiskFactors: []RiskFactor{RiskPrivileged}}
	if got := mustBuild(t, items, targets, ViewImage, privileged).Images; len(got) != 1 || got[0].Image != "acme/base:1" || got[0].WorkloadsTotal != 2 {
		t.Fatalf("image: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewCVE, Filter{RiskFactors: []RiskFactor{RiskPrivileged}, ExposedOnly: true, Severities: []Severity{SeverityCritical}}).CVEs; len(got) != 0 {
		t.Fatalf("critical AND exposed AND privileged: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewCVE, Filter{RiskFactors: []RiskFactor{RiskPrivileged}, ExposedOnly: true, Severities: []Severity{SeverityHigh}}).CVEs; len(got) != 1 || got[0].ID != "CVE-HIGH" {
		t.Fatalf("high AND exposed AND privileged: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, privileged).Workloads; len(got) != 1 || got[0].Name != "frontend" {
		t.Fatalf("workload: %+v", got)
	}
	host := Filter{RiskFactors: []RiskFactor{RiskHostAccess}}
	if got := mustBuild(t, items, targets, ViewFix, host).Fixes; len(got) != 0 {
		t.Fatalf("host access: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: SeriousRisks}).Workloads; len(got) != 1 || got[0].Name != "frontend" {
		t.Fatalf("privileged filter leaves may alone out: %+v", got)
	}
	// A workload that runs as root (and so no longer says may) is kept by a
	// filter for may.
	targets[2] = risky(targets[2], rootRisk)
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: []RiskFactor{RiskMayRunAsRoot}}).Workloads; len(got) != 3 {
		t.Fatalf("may keeps runs as root: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: []RiskFactor{RiskPrivileged, RiskHostAccess}}).Workloads; len(got) != 1 {
		t.Fatalf("either factor: %+v", got)
	}
	// checkout now runs as root: the privileged filter keeps it.
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: SeriousRisks}).Workloads; len(got) != 2 {
		t.Fatalf("privileged filter keeps runs as root: %+v", got)
	}
	// Images not scanned yet follow it too: a workload's settings are known
	// whatever its images hold.
	unscanned := append(slices.Clone(targets), risky(liveTarget("shop", "ReplicaSet", "frontend-1", "sidecar", "acme/proxy:2", "u-f", frontend), privilegedRisk),
		liveTarget("shop", "ReplicaSet", "checkout-1", "sidecar", "acme/cache:3", "u-c", checkout))
	if got := mustBuild(t, items, unscanned, ViewImage, privileged).Unscanned; len(got) != 1 || got[0].Image != "acme/proxy:2" || !slices.Equal(got[0].RiskFactors, []RiskFactor{RiskPrivileged, RiskMayRunAsRoot}) {
		t.Fatalf("not-scanned list: %+v", got)
	}
}

// Under the risk filter a capped list keeps the workload the row is kept
// for, so its mark is on the screen, however many others run the image.
func TestCapsKeepTheRiskyWorkload(t *testing.T) {
	items, targets := sharedImage(MaxWorkloadsPerRow+20, []vuln{{"CVE-1", "HIGH", "openssl", "1", "2"}})
	targets[len(targets)-1] = risky(targets[len(targets)-1], privilegedRisk)
	last := targets[len(targets)-1].workload
	resp := mustBuild(t, items, targets, ViewImage, Filter{RiskFactors: []RiskFactor{RiskPrivileged}})
	img := resp.Images[0]
	if img.WorkloadsTotal != MaxWorkloadsPerRow+20 || len(img.Workloads) != MaxWorkloadsPerRow || img.Workloads[0].Name != last.Name {
		t.Fatalf("listed %d of %d, first %+v", len(img.Workloads), img.WorkloadsTotal, img.Workloads[0])
	}
	if len(resp.PodRisk.Workloads) != 1 || resp.PodRisk.Workloads[0].Name != last.Name {
		t.Fatalf("named %+v", resp.PodRisk.Workloads)
	}
}

// The answer names only the risky workloads its rows name: trimmed rows
// (MCP) leave the others out.
func TestKeepNamedRisksFollowsTrimmedRows(t *testing.T) {
	items, targets := riskFixture()
	resp := mustBuild(t, items, targets, ViewWorkload, Filter{})
	resp.Workloads = resp.Workloads[:1] // checkout only
	resp.KeepNamedRisks()
	if len(resp.PodRisk.Workloads) != 0 {
		t.Fatalf("named %+v", resp.PodRisk.Workloads)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"podRisk":{"counts":{"dangerousCapabilities":0,"hostAccess":0,"hostPort":0,"mayRunAsRoot":2,"privilegeEscalation":0,"privileged":1,"runsAsRoot":0}}`) {
		t.Fatalf("wire %s", body)
	}
}

// Every view's CSV has risk_factors, in the screen's words, and the findings
// export follows the risk filter per workload container.
func TestExportRiskFactors(t *testing.T) {
	items, targets := riskFixture()
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload, ViewFindings} {
		if cols := ExportColumns[view]; !slices.Contains(cols, "risk_factors") {
			t.Fatalf("%s: no risk_factors column: %v", view, cols)
		}
		rows := exportRows(t, mustExport(t, items, targets, view, Filter{}), Filter{})
		cells := map[string]bool{}
		for _, r := range rows {
			cells[r["risk_factors"]] = true
		}
		want := map[string]bool{"": true, "Privileged; May run as root": true}
		if view == ViewWorkload || view == ViewFindings {
			want["May run as root"] = true
		}
		if len(cells) != len(want) {
			t.Errorf("%s: cells %v", view, cells)
		}
		for c := range want {
			if !cells[c] {
				t.Errorf("%s: no %q cell in %v", view, c, cells)
			}
		}
	}
	f := Filter{RiskFactors: []RiskFactor{RiskPrivileged}}
	rows := exportRows(t, mustExport(t, items, targets, ViewFindings, f), f)
	if len(rows) != 1 || rows[0]["workload"] != "frontend" || rows[0]["risk_factors"] != "Privileged; May run as root" {
		t.Fatalf("findings under the risk filter: %v", rows)
	}
	// Images not scanned yet carry their workloads' factors too.
	unscanned := append(slices.Clone(targets), risky(liveTarget("shop", "ReplicaSet", "frontend-1", "sidecar", "acme/proxy:2", "u-f", frontend), hostNetRisk))
	for _, view := range []View{ViewImage, ViewCVE, ViewFix} {
		rows := withStatus(exportRows(t, mustExport(t, items, unscanned, view, Filter{}), Filter{}), StatusNotScanned)
		if len(rows) != 1 || rows[0]["risk_factors"] != "Privileged; Host access; May run as root" {
			t.Fatalf("%s: not-scanned rows %v", view, rows)
		}
	}
}

// Risk factors come from the Pods that run: each Pod's own spec, a
// CronJob's template, and nothing from a Pod that has finished.
func TestLiveTargetsReadRiskFactors(t *testing.T) {
	yes, no, root := true, false, int64(0)
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "agent-1", Namespace: "shop", UID: "uid-agent-1", OwnerReferences: owned("apps/v1", "Deployment", "agent")}}
	agent := pod("agent-1-a", corev1.PodRunning, owned("apps/v1", "ReplicaSet", "agent-1"), "acme/agent:1")
	agent.Spec.HostPID = true
	agent.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes, RunAsUser: &root}
	safe := pod("safe", corev1.PodRunning, nil, "acme/safe:1")
	safe.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: &yes}
	safe.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &no}
	done := pod("done", corev1.PodSucceeded, nil, "acme/once:1")
	done.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
	cache := syncedCache(t, rs, agent, safe, done)
	live, ok, err := liveTargets(context.Background(), cache, nil, nil, nil, exposureIndex{})
	if !ok || err != nil {
		t.Fatalf("liveTargets: %v %v", ok, err)
	}
	risk := risksOf(live.targets)
	want := map[WorkloadRef]riskBits{{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "agent"}: bitPrivileged | bitHostAccess | bitRunsAsRoot | bitEscalation}
	if len(risk) != len(want) {
		t.Fatalf("risk %v", risk)
	}
	for w, bits := range want {
		if risk[w] == nil || risk[w].bits != bits {
			t.Fatalf("%v: %+v", w, risk[w])
		}
	}
}

// The CSV words are the screen's (RISK_FACTORS in parts.tsx).
func TestRiskLabelsMatchTheScreen(t *testing.T) {
	const parts = "../../web/src/components/vulnerabilities/parts.tsx"
	raw, err := os.ReadFile(parts)
	if err != nil {
		t.Fatalf("read %s: %v", parts, err)
	}
	block := regexp.MustCompile(`(?s)export const RISK_FACTORS: .*? = \{(.*?)\n\}`).FindSubmatch(raw)
	if block == nil {
		t.Fatalf("no RISK_FACTORS table in %s; did its format change?", parts)
	}
	screen := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^  (\w+): \{ label: '([^']+)'`).FindAllSubmatch(block[1], -1) {
		screen[string(m[1])] = string(m[2])
	}
	if len(screen) != len(riskOrder) {
		t.Fatalf("screen labels %v", screen)
	}
	for _, r := range riskOrder {
		if screen[string(r.factor)] != r.label {
			t.Errorf("%s: screen %q, export %q", r.factor, screen[string(r.factor)], r.label)
		}
	}
}

var (
	capsRisk     = audit.PodRisk{Factor: audit.PodRiskDangerousCapabilities, CheckID: "dangerousCapabilities", Message: `Container "app" adds dangerous capability NET_ADMIN`}
	escalateRisk = audit.PodRisk{Factor: audit.PodRiskPrivilegeEscalation, CheckID: "privilegeEscalation", Message: `Container "app" allows privilege escalation`}
	hostPortRisk = audit.PodRisk{Factor: audit.PodRiskHostPort, Message: `Container "app" binds host port 8080`}
)

// The optional factors: rows and the CSV always carry them; PodRisk names a
// workload for one only when the request marks it; counts cover them all;
// risk= keeps the rows with any.
func TestOptionalRiskFactors(t *testing.T) {
	items, targets := exposureFixture()
	targets[1] = risky(targets[1], escalateRisk, mayRootRisk) // worker
	targets[2] = risky(targets[2], capsRisk, hostPortRisk)    // checkout
	resp := mustBuild(t, items, targets, ViewWorkload, Filter{})
	byName := map[string][]RiskFactor{}
	for _, w := range resp.Workloads {
		byName[w.Name] = w.RiskFactors
	}
	if !slices.Equal(byName["checkout"], []RiskFactor{RiskDangerousCapabilities, RiskHostPort}) || !slices.Equal(byName["worker"], []RiskFactor{RiskMayRunAsRoot, RiskPrivilegeEscalation}) {
		t.Fatalf("rows %v", byName)
	}
	for _, w := range resp.PodRisk.Workloads {
		if len(w.Settings) != 0 {
			t.Fatalf("no settings for unselected common factors: %+v", w)
		}
	}
	want := map[RiskFactor]int{RiskPrivileged: 0, RiskHostAccess: 0, RiskRunsAsRoot: 0, RiskMayRunAsRoot: 1, RiskDangerousCapabilities: 1, RiskPrivilegeEscalation: 1, RiskHostPort: 1}
	if !maps.Equal(resp.PodRisk.Counts, want) {
		t.Fatalf("counts %v", resp.PodRisk.Counts)
	}
	resp = mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: []RiskFactor{RiskHostPort}})
	if len(resp.PodRisk.Workloads) != 1 || resp.PodRisk.Workloads[0].Name != "checkout" || len(resp.PodRisk.Workloads[0].Settings) != 1 || resp.PodRisk.Workloads[0].Settings[0].Factor != RiskHostPort {
		t.Fatalf("selected host port: %+v", resp.PodRisk.Workloads)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{RiskFactors: []RiskFactor{RiskPrivilegeEscalation, RiskHostPort}}).Workloads; len(got) != 2 {
		t.Fatalf("risk filter: %+v", got)
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{})
	cells := map[string]string{}
	for _, r := range rows {
		cells[r["workload"]] = r["risk_factors"]
	}
	if cells["checkout"] != "Dangerous capabilities; Host port" || cells["worker"] != "May run as root; Allows privilege escalation" {
		t.Fatalf("csv %v", cells)
	}
	if got, err := ParseRiskFactors("hostPort, privileged"); err != nil || !slices.Equal(got, []RiskFactor{RiskHostPort, RiskPrivileged}) {
		t.Fatal(got, err)
	}
	if _, err := ParseRiskFactors("root"); err == nil {
		t.Fatal("an unknown factor is refused")
	}
}
