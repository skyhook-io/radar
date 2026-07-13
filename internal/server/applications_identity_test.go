package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
)

// identRow builds an app row the identity resolver sees. key "" derives a
// label-tier key; images become one workload each.
func identRow(name, ns string, tier int, key string, images ...string) appRow {
	r := appRow{Key: key, Name: name, Namespace: ns, Namespaces: []string{ns}, Tier: tier, Health: "healthy"}
	if r.Key == "" {
		r.Key = ns + "/app/" + name
	}
	for _, img := range images {
		r.Workloads = append(r.Workloads, appWorkload{Kind: "Deployment", Namespace: ns, Name: name, Image: img, Health: "healthy"})
	}
	return r
}

func TestEnrichRowsWithArgoStatus(t *testing.T) {
	rows := []appRow{{
		Health:        "healthy",
		RuntimeHealth: "healthy",
		SourceRef:     &appSourceRef{Type: "gitops", Tool: "argocd", Kind: "Application", Namespace: "argocd", Name: "checkout"},
	}}
	app := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "checkout"},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Degraded"},
		},
	}}

	enrichRowsWithArgoStatus(rows, []*unstructured.Unstructured{app})

	if rows[0].SourceStatus == nil || rows[0].SourceStatus.Sync != "Synced" || rows[0].SourceStatus.Health != "Degraded" {
		t.Fatalf("source status = %#v", rows[0].SourceStatus)
	}
	if rows[0].RuntimeHealth != "healthy" || rows[0].Health != "degraded" {
		t.Fatalf("runtime=%q overall=%q", rows[0].RuntimeHealth, rows[0].Health)
	}
}

func TestEnrichRowsWithNonDegradingArgoStatesPreservesHealthyRuntime(t *testing.T) {
	for _, healthStatus := range []string{"Progressing", "Unknown", "Suspended"} {
		t.Run(healthStatus, func(t *testing.T) {
			rows := []appRow{{
				Health:        "healthy",
				RuntimeHealth: "healthy",
				SourceRef:     &appSourceRef{Type: "gitops", Tool: "argocd", Kind: "Application", Namespace: "argocd", Name: "checkout"},
			}}
			app := &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"namespace": "argocd", "name": "checkout"},
				"status": map[string]any{
					"sync":   map[string]any{"status": "Synced"},
					"health": map[string]any{"status": healthStatus},
				},
			}}

			enrichRowsWithArgoStatus(rows, []*unstructured.Unstructured{app})

			if rows[0].SourceStatus == nil || rows[0].SourceStatus.Health != healthStatus {
				t.Fatalf("source status = %#v", rows[0].SourceStatus)
			}
			if rows[0].RuntimeHealth != "healthy" || rows[0].Health != "healthy" {
				t.Fatalf("runtime=%q overall=%q", rows[0].RuntimeHealth, rows[0].Health)
			}
		})
	}
}

func identOf(t *testing.T, rows []appRow, name string) *appIdentity {
	t.Helper()
	for i := range rows {
		if rows[i].Name == name {
			return rows[i].Identity
		}
	}
	t.Fatalf("row %q missing", name)
	return nil
}

// The billing shape: an Argo app with a declared env-overlay path plus a raw
// row in an env namespace, same image repo → one identity; the declared member
// is high-confidence, the corroborated one medium.
func TestIdentities_DeclaredPathPlusRawNamespaceEnv(t *testing.T) {
	rows := []appRow{
		identRow("billing-staging", "staging", 3, "/Application/billing-staging", "repo.dev/koala/billing:b_2026-06-05_01"),
		identRow("billing", "dev", 0, "", "repo.dev/koala/billing:b_2026-05-18_00"),
	}
	resolveAppIdentities(rows, map[string]string{"billing-staging": "billing/deploy/overlays/staging"}, nil, nil, nil)

	st := identOf(t, rows, "billing-staging")
	if st == nil || st.Key != "billing" || st.Env != "staging" || st.Confidence != "high" {
		t.Fatalf("billing-staging identity = %+v, want key=billing env=staging high", st)
	}
	dev := identOf(t, rows, "billing")
	if dev == nil || dev.Key != "billing" || dev.Env != "dev" || dev.Confidence != "medium" {
		t.Fatalf("billing(dev) identity = %+v, want key=billing env=dev medium", dev)
	}
}

// Env-PREFIXED hub-spoke tracking-id pairs (no in-cluster Application objects),
// same repo → medium identity when the tokens are on the universal ladder.
func TestIdentities_EnvPrefixTrackingPair(t *testing.T) {
	rows := []appRow{
		identRow("dev-koala-backend-us-east1", "dev", 3, "/Application/dev-koala-backend-us-east1", "repo.dev/koala/koala-backend:m1"),
		identRow("staging-koala-backend-us-east1", "staging", 3, "/Application/staging-koala-backend-us-east1", "repo.dev/koala/koala-backend:m2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)

	a := identOf(t, rows, "dev-koala-backend-us-east1")
	b := identOf(t, rows, "staging-koala-backend-us-east1")
	if a == nil || b == nil || a.Key != "koala-backend-us-east1" || a.Key != b.Key {
		t.Fatalf("prefix pair identities = %+v / %+v, want shared key koala-backend-us-east1", a, b)
	}
	if a.Env != "dev" || b.Env != "staging" || a.Confidence != "medium" {
		t.Fatalf("prefix pair env/conf = %s/%s %s, want dev/staging medium", a.Env, b.Env, a.Confidence)
	}
}

// The project-infra shape: identical name across three env namespaces, same
// repo → one identity with env from the namespace.
func TestIdentities_SameNameAcrossEnvNamespaces(t *testing.T) {
	rows := []appRow{
		identRow("project-infra", "dev", 7, "dev/app/project-infra", "repo.dev/koala/project-infra:x"),
		identRow("project-infra", "staging", 7, "staging/app/project-infra", "repo.dev/koala/project-infra:y"),
		identRow("project-infra", "qa", 7, "qa/app/project-infra", "repo.dev/koala/project-infra:z"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	envs := map[string]bool{}
	for i := range rows {
		f := rows[i].Identity
		if f == nil || f.Key != "project-infra" {
			t.Fatalf("row %d identity = %+v, want key=project-infra", i, f)
		}
		envs[f.Env] = true
	}
	if !envs["dev"] || !envs["staging"] || !envs["qa"] {
		t.Fatalf("envs = %v, want dev+staging+qa", envs)
	}
}

// Repo corroboration is mandatory for F2: a name-stem coincidence with no
// shared image repo must NOT group.
func TestIdentities_NoRepoOverlapRefuses(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "", "repo.dev/teamA/api:1"),
		identRow("api", "staging", 0, "", "repo.dev/teamB/other:1"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil || rows[1].Identity != nil {
		t.Fatalf("uncorroborated stem match grouped: %+v / %+v", rows[0].Identity, rows[1].Identity)
	}
}

// Same env twice is replicas/shards, not an identity group — distinct envs required.
func TestIdentities_SingleEnvRefuses(t *testing.T) {
	rows := []appRow{
		identRow("worker", "staging", 0, "staging/app/worker-a", "repo.dev/koala/worker:1"),
		identRow("worker", "staging", 0, "staging/app/worker-b", "repo.dev/koala/worker:1"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil {
		t.Fatalf("single-env group formed: %+v", rows[0].Identity)
	}
}

// Conflicting DECLARED path stems never merge through a shared name stem.
func TestIdentities_ConflictingDeclaredStemsRefuse(t *testing.T) {
	rows := []appRow{
		identRow("shop-staging", "staging", 3, "/Application/shop-staging", "repo.dev/a/shop:1"),
		identRow("shop-dev", "dev", 3, "/Application/shop-dev", "repo.dev/a/shop:2"),
	}
	resolveAppIdentities(rows, map[string]string{
		"shop-staging": "teamA/shop/overlays/staging",
		"shop-dev":     "teamB/legacy-shop/overlays/dev",
	}, nil, nil, nil)
	if rows[0].Identity != nil || rows[1].Identity != nil {
		t.Fatalf("conflicting declarations grouped: %+v / %+v", rows[0].Identity, rows[1].Identity)
	}
}

// A name affix outside the conservative set is not env evidence: "-test" apps
// don't identity via name, only via an env namespace.
func TestIdentities_GenericTokensNotNameEvidence(t *testing.T) {
	rows := []appRow{
		identRow("load-test", "apps", 0, "", "repo.dev/koala/load:1"),
		identRow("load", "dev", 0, "", "repo.dev/koala/load:1"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil {
		t.Fatalf("'-test' suffix treated as env: %+v", rows[0].Identity)
	}
}

// Synonyms canonicalize so "production"/"stage" land on the ladder tokens.
func TestIdentities_EnvSynonymsCanonicalize(t *testing.T) {
	rows := []appRow{
		identRow("pay-production", "payments", 0, "", "repo.dev/koala/pay:1"),
		identRow("pay-stage", "payments", 0, "", "repo.dev/koala/pay:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	a, b := rows[0].Identity, rows[1].Identity
	if a == nil || b == nil || a.Env != "prod" || b.Env != "staging" {
		t.Fatalf("synonyms = %+v / %+v, want prod / staging", a, b)
	}
}

// THE CONTRACT: identity is classification, never identity — a tagged row is
// byte-identical to the untagged row minus the identity block.
func TestIdentities_ClassificationNotIdentity(t *testing.T) {
	mk := func() []appRow {
		return []appRow{
			identRow("billing-staging", "staging", 3, "/Application/billing-staging", "repo.dev/koala/billing:1"),
			identRow("billing", "dev", 0, "", "repo.dev/koala/billing:2"),
		}
	}
	tagged := mk()
	resolveAppIdentities(tagged, nil, nil, nil, nil)
	for i := range tagged {
		// Classification emits two derived outputs: the Identity block and an
		// informational "name-stem:" match key (excluded from event matching).
		// Strip both — the contract is that classification only ADDS these derived
		// fields and never mutates the row's substantive data or exact match keys.
		tagged[i].Identity = nil
		kept := tagged[i].MatchKeys[:0]
		for _, k := range tagged[i].MatchKeys {
			if !strings.HasPrefix(k, "name-stem:") {
				kept = append(kept, k)
			}
		}
		tagged[i].MatchKeys = kept
	}
	want, _ := json.Marshal(mk())
	got, _ := json.Marshal(tagged)
	if string(want) != string(got) {
		t.Fatalf("identity tagging mutated row identity:\nwant %s\ngot  %s", want, got)
	}
}

// DISCOVERY: a custom token ("loadtest" — in no vocabulary anywhere) becomes
// an env when the cluster proves it: recurrence across ≥3 repo-corroborated
// stems, OR a name affix corroborated by a namespace of the same name. This
// is the point of dropping hardcoded token lists.
func TestIdentities_CustomTokenDiscoveredByRecurrence(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api", "repo.dev/koala/api:1"),
		identRow("api-loadtest", "team", 0, "", "repo.dev/koala/api:2"),
		identRow("web", "dev", 0, "dev/app/web", "repo.dev/koala/web:1"),
		identRow("web-loadtest", "team2", 0, "", "repo.dev/koala/web:2"),
		identRow("cron", "dev", 0, "dev/app/cron", "repo.dev/koala/cron:1"),
		identRow("cron-loadtest", "team3", 0, "", "repo.dev/koala/cron:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	a := identOf(t, rows, "api-loadtest")
	if a == nil || a.Key != "api" || a.Env != "loadtest" {
		t.Fatalf("api-loadtest identity = %+v, want key=api env=loadtest (recurrence-discovered)", a)
	}
}

// A name affix corroborated by a namespace of the same name qualifies even as
// a one-off ("api-loadtest" + a loadtest namespace in the cluster).
func TestIdentities_AffixCorroboratedByNamespace(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api", "repo.dev/koala/api:1"),
		identRow("api-loadtest", "loadtest", 0, "", "repo.dev/koala/api:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	a := identOf(t, rows, "api-loadtest")
	if a == nil || a.Env != "loadtest" {
		t.Fatalf("affix+namespace corroboration failed: %+v", a)
	}
}

// Parallel variant dimensions (-cos/-ubuntu nodepool images) recur across
// only a couple of stems and have no namespace — they must NOT become envs.
func TestIdentities_VariantSuffixesNotEnvs(t *testing.T) {
	rows := []appRow{
		identRow("nodepool-cos", "infra", 0, "", "repo.dev/koala/np:1"),
		identRow("nodepool-ubuntu", "infra", 0, "", "repo.dev/koala/np:2"),
		identRow("gpupool-cos", "infra", 0, "", "repo.dev/koala/gp:1"),
		identRow("gpupool-ubuntu", "infra", 0, "", "repo.dev/koala/gp:2"),
		identRow("mempool-cos", "infra", 0, "", "repo.dev/koala/mp:1"),
		identRow("mempool-ubuntu", "infra", 0, "", "repo.dev/koala/mp:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	for i := range rows {
		if rows[i].Identity != nil {
			t.Fatalf("variant suffix grouped as env: %s → %+v", rows[i].Name, rows[i].Identity)
		}
	}
}

// A multi-segment namespace name is not an env token — only its segments may
// qualify ("payments-staging" → staging; "skyhook-clients-frps" → nothing).
func TestIdentities_MultiSegmentNamespaceNotAToken(t *testing.T) {
	rows := []appRow{
		identRow("frps-a", "skyhook-clients-frps", 0, "", "repo.dev/koala/frps:1"),
		identRow("frps-a", "skyhook-clients-frps-staging", 0, "", "repo.dev/koala/frps:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	for i := range rows {
		f := rows[i].Identity
		if f != nil && f.Env != "staging" {
			t.Fatalf("namespace name leaked as env: %+v", f)
		}
	}
}

// A one-off token ("v2") never qualifies — no recurrence, no namespace,
// no label. Version forks don't become fake environments.
func TestIdentities_OneOffTokenNotDiscovered(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api", "repo.dev/koala/api:1"),
		identRow("api-v2", "dev", 0, "", "repo.dev/koala/api:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if f := identOf(t, rows, "api-v2"); f != nil {
		t.Fatalf("api-v2 grouped as env instance: %+v", f)
	}
}

// EXPLICIT LABELS: an environment label (workload or namespace) qualifies any
// token and beats every heuristic env reading. It is still local app-identity
// evidence unless a declared upstream identity is present.
func TestIdentities_ExplicitEnvLabels(t *testing.T) {
	a := identRow("payments", "team-a", 0, "team-a/app/payments", "repo.dev/koala/pay:1")
	b := identRow("payments", "team-b", 0, "team-b/app/payments", "repo.dev/koala/pay:2")
	a.Workloads[0].envLabel = "blue"
	rows := []appRow{a, b}
	resolveAppIdentities(rows, nil, nil, map[string]string{"team-b": "green"}, nil)
	fa, fb := rows[0].Identity, rows[1].Identity
	if fa == nil || fa.Env != "blue" || fa.Confidence != "medium" || fa.Portable || !strings.Contains(fa.Evidence, `environment label "blue"`) {
		t.Fatalf("workload-labeled instance = %+v, want env=blue medium/local with label evidence", fa)
	}
	if fb == nil || fb.Env != "green" || fb.Confidence != "medium" || fb.Portable {
		t.Fatalf("namespace-labeled instance = %+v, want env=green medium/local", fb)
	}
	if fa.Key != fb.Key || fa.Key != "payments" {
		t.Fatalf("label-only instances should share key payments: %q / %q", fa.Key, fb.Key)
	}
}

// Disagreeing workload labels within one row refuse the explicit tier rather
// than guessing.
func TestIdentities_DisagreeingWorkloadLabelsIgnored(t *testing.T) {
	a := identRow("api", "dev", 0, "dev/app/api", "repo.dev/koala/api:1")
	a.Workloads = append(a.Workloads, appWorkload{Kind: "Deployment", Namespace: "dev", Name: "api-2", Image: "repo.dev/koala/api:1", Health: "healthy"})
	a.Workloads[0].envLabel = "staging"
	a.Workloads[1].envLabel = "prod"
	b := identRow("api", "staging", 0, "staging/app/api", "repo.dev/koala/api:2")
	rows := []appRow{a, b}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	fa := rows[0].Identity
	// Falls back to the namespace reading (dev) instead of either label.
	if fa == nil || fa.Env != "dev" {
		t.Fatalf("disagreeing labels should fall back to ns reading: %+v", fa)
	}
}

func TestEnvLabelOf(t *testing.T) {
	if v := envLabelOf(map[string]string{"tags.datadoghq.com/env": "Prod"}); v != "prod" {
		t.Fatalf("datadog key = %q, want prod (lowercased)", v)
	}
	if v := envLabelOf(map[string]string{"environment": "qa", "env": "ignored"}); v != "qa" {
		t.Fatalf("priority = %q, want qa (environment beats env)", v)
	}
	if v := envLabelOf(map[string]string{"app.kubernetes.io/name": "x"}); v != "" {
		t.Fatalf("no env keys = %q, want empty", v)
	}
}

// ADOPTION (happy path): a single-token-namespace sibling that shares the
// core's repo joins a identity the core has already proven.
func TestIdentities_AdoptionJoinsProvenCore(t *testing.T) {
	rows := []appRow{
		identRow("project-infra", "dev", 7, "dev/app/project-infra", "repo.dev/koala/pi:x"),
		identRow("project-infra", "staging", 7, "staging/app/project-infra", "repo.dev/koala/pi:y"),
		identRow("project-infra", "sandboxx", 7, "sandboxx/app/project-infra", "repo.dev/koala/pi:z"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	f := identOf(t, rows, "project-infra")
	got := rows[2].Identity
	if f == nil || got == nil || got.Key != "project-infra" || got.Env != "sandboxx" {
		t.Fatalf("adoptee = %+v, want key=project-infra env=sandboxx", got)
	}
}

// Adoption never bootstraps: one qualified core member + adoptees must NOT
// form a identity (the core alone has a single env).
func TestIdentities_AdoptionNeverBootstraps(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api", "repo.dev/koala/api:1"),
		identRow("api", "teamspace", 0, "teamspace/app/api", "repo.dev/koala/api:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil || rows[1].Identity != nil {
		t.Fatalf("adoption bootstrapped a identity: %+v / %+v", rows[0].Identity, rows[1].Identity)
	}
}

// A coincidence-named row with a DIFFERENT repo in some single-token
// namespace must neither join NOR veto the proven core (the poisoning case:
// repo corroboration is computed over the core only).
func TestIdentities_AdopteeNeverVetoesCore(t *testing.T) {
	rows := []appRow{
		identRow("billing", "dev", 0, "dev/app/billing", "repo.dev/koala/billing:1"),
		identRow("billing", "staging", 0, "staging/app/billing", "repo.dev/koala/billing:2"),
		identRow("billing", "team", 0, "team/app/billing", "repo.dev/other/thing:1"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	dev, st, stranger := rows[0].Identity, rows[1].Identity, rows[2].Identity
	if dev == nil || st == nil || dev.Key != "billing" {
		t.Fatalf("coincidence row vetoed the proven core: %+v / %+v", dev, st)
	}
	if stranger != nil {
		t.Fatalf("different-repo stranger joined the identity: %+v", stranger)
	}
}

func TestIdentities_SameStemStrangerDoesNotVetoRepoCore(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api", "repo.dev/team/api:1"),
		identRow("api", "staging", 0, "staging/app/api", "repo.dev/team/api:2"),
		identRow("api", "qa", 0, "qa/app/api", "repo.dev/other/api:1"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	dev, st, stranger := rows[0].Identity, rows[1].Identity, rows[2].Identity
	if dev == nil || st == nil || dev.Key != "api" || st.Key != "api" {
		t.Fatalf("different-repo stranger vetoed valid api identity: %+v / %+v", dev, st)
	}
	if stranger != nil {
		t.Fatalf("different-repo same-stem stranger joined identity: %+v", stranger)
	}
}

func TestIdentities_EmptyRepoCandidateDoesNotHideLaterRepoCore(t *testing.T) {
	rows := []appRow{
		identRow("api", "dev", 0, "dev/app/api"),
		identRow("api", "staging", 0, "staging/app/api", "repo.dev/team/api:1"),
		identRow("api", "prod", 0, "prod/app/api", "repo.dev/team/api:2"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil {
		t.Fatalf("empty-repo candidate joined identity: %+v", rows[0].Identity)
	}
	st, prod := rows[1].Identity, rows[2].Identity
	if st == nil || prod == nil || st.Key != "api" || prod.Key != "api" {
		t.Fatalf("empty first repo candidate hid later repo core: %+v / %+v", st, prod)
	}
}

func TestPathStemEnv(t *testing.T) {
	cases := []struct{ path, stem, env string }{
		{"billing/deploy/overlays/staging", "billing/deploy", "staging"},
		{"deploy/environments/loadtest/api", "deploy/api", "loadtest"}, // declared dir names ANY env
		{"deploy/prod/billing", "deploy/billing", "prod"},              // trio segment without a convention dir
		{"apps/overlays", "", ""},                                      // trailing convention dir, no env
		{"charts/api", "", ""},                                         // no env signal at all
	}
	for _, c := range cases {
		stem, env := pathStemEnv(c.path)
		if stem != c.stem || env != c.env {
			t.Errorf("pathStemEnv(%q) = (%q, %q), want (%q, %q)", c.path, stem, env, c.stem, c.env)
		}
	}
}

type stubLister struct{ items []*unstructured.Unstructured }

func (s *stubLister) ListDynamicWithGroup(_ context.Context, kind, _, group string) ([]*unstructured.Unstructured, error) {
	switch {
	case kind == "Application" && group == "argoproj.io":
	case kind == "Kustomization" && group == "kustomize.toolkit.fluxcd.io":
	default:
		return nil, fmt.Errorf("unexpected list %s/%s", group, kind)
	}
	return s.items, nil
}

func argoApp(name string, spec map[string]any) *unstructured.Unstructured {
	return argoAppInNamespace("", name, spec)
}

func argoAppInNamespace(ns, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     spec,
	}}
}

type nilNamespaceLister struct{}

func (nilNamespaceLister) Namespaces() listerscorev1.NamespaceLister { return nil }

func TestNamespaceEnvLabels_NoNamespaceLister(t *testing.T) {
	if got := namespaceEnvLabels(nilNamespaceLister{}); len(got) != 0 {
		t.Fatalf("namespaceEnvLabels with nil lister = %v, want empty", got)
	}
}

// argoSourcePaths is the F1 feed — a silent shape mismatch here degrades
// every declared identity with no error anywhere, which is exactly why the
// lister is an interface.
func TestArgoSourcePaths(t *testing.T) {
	lister := &stubLister{items: []*unstructured.Unstructured{
		argoApp("billing-staging", map[string]any{"source": map[string]any{"path": "billing/deploy/overlays/staging"}}),
		// multi-source: the first env-bearing path wins, env-less ones skipped
		argoApp("multi", map[string]any{"sources": []any{
			map[string]any{"path": "charts/shared"},
			map[string]any{"path": "apps/overlays/dev"},
		}}),
		argoApp("no-env-path", map[string]any{"source": map[string]any{"path": "charts/api"}}),
		argoApp("malformed", map[string]any{"source": "not-a-map"}),
	}}
	got, _, _ := argoApplicationFacts(context.Background(), lister)
	want := map[string]string{
		"billing-staging": "billing/deploy/overlays/staging",
		"multi":           "apps/overlays/dev",
	}
	if len(got) != len(want) {
		t.Fatalf("argoSourcePaths = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("argoSourcePaths[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestArgoSourcePaths_AmbiguousSameNameRefuses(t *testing.T) {
	lister := &stubLister{items: []*unstructured.Unstructured{
		argoAppInNamespace("team-a", "billing-staging", map[string]any{"source": map[string]any{"path": "team-a/billing/overlays/staging"}}),
		argoAppInNamespace("team-b", "billing-staging", map[string]any{"source": map[string]any{"path": "team-b/billing/overlays/staging"}}),
	}}
	got, _, _ := argoApplicationFacts(context.Background(), lister)
	if got["billing-staging"] != "" {
		t.Fatalf("bare ambiguous argoSourcePaths[billing-staging] = %q, want empty", got["billing-staging"])
	}
	if got["team-a/billing-staging"] == "" || got["team-b/billing-staging"] == "" {
		t.Fatalf("namespaced argoSourcePaths should survive ambiguity: %v", got)
	}
}

func TestIdentities_ArgoSourcePathUsesNamespacedKey(t *testing.T) {
	rows := []appRow{
		identRow("billing-staging", "staging", 3, "argocd-a/Application/billing-staging", "repo.dev/koala/billing:a"),
		identRow("billing-dev", "dev", 3, "argocd-b/Application/billing-dev", "repo.dev/koala/billing:b"),
	}
	resolveAppIdentities(rows, map[string]string{
		"argocd-a/billing-staging": "apps/billing/overlays/staging",
		"argocd-b/billing-dev":     "apps/billing/overlays/dev",
	}, nil, nil, nil)
	a, b := rows[0].Identity, rows[1].Identity
	if a == nil || b == nil || a.Key != "billing" || b.Key != "billing" || !a.Portable || !b.Portable {
		t.Fatalf("namespaced Argo path identities = %+v / %+v, want portable billing", a, b)
	}
}

// withName stamps app.kubernetes.io/name on every workload of a row.
func withName(r appRow, nameLabel string) appRow {
	for i := range r.Workloads {
		r.Workloads[i].nameLabel = nameLabel
	}
	return r
}

// A single-instance row with an app.kubernetes.io/name label gets a clean,
// high-confidence identity keyed on the label — no in-cluster group needed. It
// is NOT portable on its own: cross-cluster folding is the hub's corroborated
// call (a bare label like "api" must not collapse unrelated apps globally).
func TestIdentities_LabelGivesCleanSingletonKey(t *testing.T) {
	rows := []appRow{
		withName(identRow("koala-backend-xyz", "prod", 0, "prod/app/koala-backend-xyz", "ghcr.io/k/koala-backend:1"), "koala-backend"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	id := rows[0].Identity
	if id == nil || id.Key != "koala-backend" || id.Confidence != "high" {
		t.Fatalf("label identity not set high-confidence: %+v", id)
	}
	if id.Portable {
		t.Errorf("bare label must not be standalone-portable: %+v", id)
	}
	if id.Env != "prod" {
		t.Errorf("env = %q, want prod", id.Env)
	}
}

// The label upgrades a weaker name-stem identity to a clean, high-confidence
// declared key (portability stays the hub's call).
func TestIdentities_LabelUpgradesNameStem(t *testing.T) {
	rows := []appRow{
		withName(identRow("widget", "dev", 0, "dev/app/widget", "ghcr.io/k/widget:1"), "widget"),
		withName(identRow("widget", "prod", 0, "prod/app/widget", "ghcr.io/k/widget:2"), "widget"),
	}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	for i := range rows {
		id := rows[i].Identity
		if id == nil || id.Key != "widget" || id.Confidence != "high" {
			t.Fatalf("expected high-confidence label key, got %+v", id)
		}
	}
}

// A declared Argo source-path identity (equally high confidence, more specific
// about the env overlay) is NOT overwritten by the name label.
func TestIdentities_LabelDoesNotOverrideArgoPath(t *testing.T) {
	rows := []appRow{
		withName(identRow("billing-staging", "staging", 4, "/Application/billing-staging", "ghcr.io/k/billing:1"), "billing"),
		identRow("billing-dev", "dev", 4, "/Application/billing-dev", "ghcr.io/k/billing:2"),
	}
	resolveAppIdentities(rows, map[string]string{
		"billing-staging": "billing/deploy/overlays/staging",
		"billing-dev":     "billing/deploy/overlays/dev",
	}, nil, nil, nil)
	// The declared Argo identity must survive — its evidence still cites the
	// source path, not the name label (which would mean the label clobbered it).
	id := identOf(t, rows, "billing-staging")
	if id == nil || !strings.Contains(id.Evidence, "Argo CD source path") {
		t.Fatalf("Argo path identity overridden by label: %+v", id)
	}
	if id.Env != "staging" {
		t.Errorf("Argo overlay env lost: %+v", id)
	}
}

// Workloads disagreeing on app.kubernetes.io/name are not one app — no label
// identity (and a lone row forms no group either).
func TestIdentities_DisagreeingNameLabelsIgnored(t *testing.T) {
	r := identRow("multi", "prod", 0, "prod/app/multi", "ghcr.io/k/multi:1", "ghcr.io/k/multi:1")
	r.Workloads[0].nameLabel = "alpha"
	r.Workloads[1].nameLabel = "beta"
	rows := []appRow{r}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if rows[0].Identity != nil {
		t.Fatalf("disagreeing name labels should not yield identity: %+v", rows[0].Identity)
	}
}

// The explicit app.skyhook.io/app annotation is authoritative + portable, and
// overrides even a declared Argo source-path identity (the user opted in).
func TestIdentities_ExplicitAnnotationIsAuthoritativePortable(t *testing.T) {
	r := identRow("billing-staging", "staging", 3, "/Application/billing-staging", "repo.dev/koala/billing:b1")
	r.Workloads[0].appAnnotation = "koala-billing"
	rows := []appRow{r}
	resolveAppIdentities(rows, map[string]string{"billing-staging": "billing/deploy/overlays/staging"}, nil, nil, nil)

	id := identOf(t, rows, "billing-staging")
	if id == nil || id.Key != "koala-billing" || id.Source != SourceExplicit || !id.Portable {
		t.Fatalf("explicit identity = %+v, want key=koala-billing source=explicit portable", id)
	}
	if id.Env != "staging" {
		t.Errorf("explicit tier should keep the env a stronger signal resolved: %+v", id)
	}
}

// A single unlabeled row with the explicit annotation still groups — it needs no
// in-cluster corroboration (the declaration stands alone).
func TestIdentities_ExplicitAnnotationStandsAlone(t *testing.T) {
	r := identRow("api", "dev", 0, "dev/app/api", "ghcr.io/acme/api:1")
	r.Workloads[0].appAnnotation = "acme-api"
	rows := []appRow{r}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	id := identOf(t, rows, "api")
	if id == nil || id.Key != "acme-api" || id.Source != SourceExplicit || !id.Portable {
		t.Fatalf("standalone explicit identity = %+v, want key=acme-api source=explicit portable", id)
	}
}

// Source provenance is stamped on every tier so the fleet consumer can gate
// cross-cluster promotion on the source, not on the human evidence string.
func TestIdentities_SourceProvenanceStamped(t *testing.T) {
	// Argo source path → argo-path, portable.
	argo := []appRow{
		identRow("billing-staging", "staging", 3, "/Application/billing-staging", "r.dev/b:1"),
		identRow("billing", "dev", 0, "", "r.dev/b:2"),
	}
	resolveAppIdentities(argo, map[string]string{"billing-staging": "billing/deploy/overlays/staging"}, nil, nil, nil)
	if id := identOf(t, argo, "billing-staging"); id == nil || id.Source != SourceArgoPath || !id.Portable {
		t.Fatalf("argo-path source = %+v, want argo-path portable", id)
	}

	// Bare app.kubernetes.io/name → label, NOT portable (collision-prone, the
	// fleet must corroborate before any cross-cluster fold).
	lbl := identRow("web", "prod", 0, "prod/app/web", "ghcr.io/x/web:1")
	lbl.Workloads[0].nameLabel = "web"
	lrows := []appRow{lbl}
	resolveAppIdentities(lrows, nil, nil, nil, nil)
	if id := identOf(t, lrows, "web"); id == nil || id.Source != SourceLabel || id.Portable {
		t.Fatalf("label source = %+v, want label NOT portable", id)
	}
}

// The ApplicationSet fan-out discriminator: only a SINGLE-APP env-fan-out (one
// app, children differ solely by a trio env token) is a valid set identity. A
// multi-app bundle, a per-env bundle, and a cluster/region fan-out all refuse.
func TestAppSetFanouts_SingleAppVsBundle(t *testing.T) {
	child := func(name string) argoChild { return argoChild{keys: []string{name}, name: name} }

	// Single-app env fan-out → folds to one stem, per-child env.
	fanout := appSetFanouts(map[string][]argoChild{
		"billing": {child("billing-dev"), child("billing-staging"), child("billing-prod")},
	})
	for _, c := range []struct{ key, env string }{{"billing-dev", "dev"}, {"billing-staging", "staging"}, {"billing-prod", "prod"}} {
		if f, ok := fanout[c.key]; !ok || f.stem != "billing" || f.env != c.env {
			t.Fatalf("fan-out child %s = %+v, want stem=billing env=%s", c.key, f, c.env)
		}
	}

	// Multi-app bundle (children are different apps) → no set identity.
	if got := appSetFanouts(map[string][]argoChild{
		"platform": {child("frontend"), child("backend"), child("database")},
	}); len(got) != 0 {
		t.Fatalf("multi-app bundle set must not yield identity: %+v", got)
	}

	// Per-env bundle (one env, many apps — differing token is the APP, not env)
	// → no set identity.
	if got := appSetFanouts(map[string][]argoChild{
		"prod-apps": {child("billing-prod"), child("shipping-prod")},
	}); len(got) != 0 {
		t.Fatalf("per-env bundle set must not yield identity: %+v", got)
	}

	// Cluster fan-out (differing token is a cluster name, not a trio env) → no
	// OSS identity; env/identity for that shape comes from the hub.
	if got := appSetFanouts(map[string][]argoChild{
		"billing": {child("billing-uswest"), child("billing-useast")},
	}); len(got) != 0 {
		t.Fatalf("cluster fan-out must not yield OSS identity: %+v", got)
	}

	// Trivial stem (children collapse to "api") → refused.
	if got := appSetFanouts(map[string][]argoChild{
		"api": {child("api-dev"), child("api-prod")},
	}); len(got) != 0 {
		t.Fatalf("trivial-stem fan-out must not yield identity: %+v", got)
	}
}

// An ApplicationSet fan-out makes its children portable (Source=argo-appset),
// overriding the weaker name-stem tier — so they fold across clusters.
func TestIdentities_AppSetFanoutIsPortable(t *testing.T) {
	rows := []appRow{
		identRow("billing-dev", "argocd", 3, "argocd/Application/billing-dev", "repo.dev/billing:1"),
		identRow("billing-prod", "argocd", 3, "argocd/Application/billing-prod", "repo.dev/billing:2"),
	}
	fan := appSetFanouts(map[string][]argoChild{
		"billing": {{keys: []string{"argocd/billing-dev", "billing-dev"}, name: "billing-dev"}, {keys: []string{"argocd/billing-prod", "billing-prod"}, name: "billing-prod"}},
	})
	resolveAppIdentities(rows, nil, fan, nil, nil)
	for _, name := range []string{"billing-dev", "billing-prod"} {
		id := identOf(t, rows, name)
		if id == nil || id.Key != "billing" || id.Source != SourceArgoAppSet || !id.Portable {
			t.Fatalf("%s appset identity = %+v, want key=billing source=argo-appset portable", name, id)
		}
	}
}

// collectArgoClaims emits a claim only for Applications with a DECLARED identity,
// carrying the destination + managed workloads so the hub can stamp it onto the
// member cluster's rows (hub-spoke). Path-less / undeclared Applications emit none.
func TestCollectArgoClaims(t *testing.T) {
	argoAppFull := func(name string, spec, status map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": name},
			"spec":     spec,
			"status":   status,
		}}
	}
	lister := &stubLister{items: []*unstructured.Unstructured{
		argoAppFull("billing-prod",
			map[string]any{
				"source":      map[string]any{"path": "apps/billing/overlays/prod"},
				"destination": map[string]any{"name": "prod-cluster", "namespace": "billing"},
			},
			map[string]any{"resources": []any{
				map[string]any{"kind": "Deployment", "namespace": "billing", "name": "billing-api"},
				map[string]any{"kind": "ConfigMap", "namespace": "billing", "name": "billing-cfg"}, // not a workload
			}}),
		// No declared identity (env-less path) → no claim.
		argoAppFull("infra", map[string]any{"source": map[string]any{"path": "charts/infra"}}, nil),
	}}
	sourcePaths, appSetChildren, argoItems := argoApplicationFacts(context.Background(), lister)
	claims := collectArgoClaims(argoItems, sourcePaths, appSetFanouts(appSetChildren), nil)

	if len(claims) != 1 {
		t.Fatalf("claims = %d, want 1 (only the declared app): %+v", len(claims), claims)
	}
	c := claims[0]
	if c.Identity == nil || c.Identity.Key != "apps/billing" || c.Identity.Source != SourceArgoPath || !c.Identity.Portable {
		t.Fatalf("claim identity = %+v, want apps/billing argo-path portable", c.Identity)
	}
	if c.DestName != "prod-cluster" || c.DestNamespace != "billing" {
		t.Fatalf("claim destination = %q/%q, want prod-cluster/billing", c.DestName, c.DestNamespace)
	}
	if len(c.Workloads) != 1 || c.Workloads[0].Kind != "Deployment" || c.Workloads[0].Name != "billing-api" {
		t.Fatalf("claim workloads = %+v, want only the Deployment", c.Workloads)
	}
}

// Fix: a raw/label workload that merely SHARES A NAME with an ApplicationSet
// child (but isn't Argo-managed) must not be promoted to a portable argo-appset
// identity via the bare-name key fallback.
func TestIdentities_AppSetFanoutSkipsNonArgoRows(t *testing.T) {
	// A plain Deployment named "billing-dev" (tier 0, not Argo-managed).
	rows := []appRow{identRow("billing-dev", "prod", 0, "prod/app/billing-dev", "repo.dev/x:1")}
	fan := appSetFanouts(map[string][]argoChild{
		"billing": {{keys: []string{"argocd/billing-dev", "billing-dev"}, name: "billing-dev"}, {keys: []string{"argocd/billing-prod", "billing-prod"}, name: "billing-prod"}},
	})
	resolveAppIdentities(rows, nil, fan, nil, nil)
	if id := rows[0].Identity; id != nil && id.Source == SourceArgoAppSet {
		t.Fatalf("non-Argo row falsely stamped argo-appset: %+v", id)
	}
}

// Fix: the explicit annotation is authoritative+portable, so a row where only
// SOME workloads carry it must NOT be promoted (partial annotation ≠ whole app).
func TestIdentities_ExplicitAnnotationRequiresAllWorkloads(t *testing.T) {
	r := identRow("svc", "dev", 0, "dev/app/svc", "ghcr.io/a:1", "ghcr.io/a:1")
	r.Workloads[0].appAnnotation = "acme-svc"
	// r.Workloads[1] has no annotation → not all agree.
	rows := []appRow{r}
	resolveAppIdentities(rows, nil, nil, nil, nil)
	if id := rows[0].Identity; id != nil && id.Source == SourceExplicit {
		t.Fatalf("partial annotation should not yield explicit identity: %+v", id)
	}

	// All workloads annotated (same value) → explicit identity applies.
	r2 := identRow("svc", "dev", 0, "dev/app/svc", "ghcr.io/a:1", "ghcr.io/a:1")
	r2.Workloads[0].appAnnotation = "acme-svc"
	r2.Workloads[1].appAnnotation = "acme-svc"
	rows2 := []appRow{r2}
	resolveAppIdentities(rows2, nil, nil, nil, nil)
	if id := rows2[0].Identity; id == nil || id.Source != SourceExplicit || id.Key != "acme-svc" {
		t.Fatalf("fully-annotated row should be explicit: %+v", id)
	}
}

// Fix: collectArgoClaims scopes by the MANAGED WORKLOAD namespace (an
// Application may live in argocd while its workloads run elsewhere), returns
// nothing for explicit no-access (non-nil empty), and everything for full access
// (nil). A scoped caller gets a claim only when it can see a managed workload.
func TestCollectArgoClaims_NamespaceScoped(t *testing.T) {
	// Both Applications live in argocd; their workloads run in app namespaces.
	app := func(name, path, wlNs, wlName string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": name},
			"spec":     map[string]any{"source": map[string]any{"path": path}},
			"status": map[string]any{"resources": []any{
				map[string]any{"kind": "Deployment", "namespace": wlNs, "name": wlName},
			}},
		}}
	}
	lister := &stubLister{items: []*unstructured.Unstructured{
		app("billing", "apps/billing/overlays/prod", "team-a", "billing-api"),
		app("shipping", "apps/shipping/overlays/prod", "team-b", "shipping-api"),
	}}
	sp, asc, argoItems := argoApplicationFacts(context.Background(), lister)
	asf := appSetFanouts(asc)

	// Scoped to team-a → only billing (its workload is visible), even though both
	// Applications live in argocd, which is NOT in the allowed set.
	claims := collectArgoClaims(argoItems, sp, asf, []string{"team-a"})
	if len(claims) != 1 || claims[0].Identity.Key != "apps/billing" {
		t.Fatalf("namespace-scoped claims = %+v, want only billing (team-a workload visible)", claims)
	}
	// Explicit no access (non-nil empty) → nothing.
	if none := collectArgoClaims(argoItems, sp, asf, []string{}); len(none) != 0 {
		t.Fatalf("no-access claims = %d, want 0", len(none))
	}
	// Full access (nil) → both.
	if all := collectArgoClaims(argoItems, sp, asf, nil); len(all) != 2 {
		t.Fatalf("unscoped claims = %d, want 2", len(all))
	}
}

// A claim's declared identity must use the same precedence as the row resolver:
// an env-bearing Argo source path outranks an ApplicationSet fan-out, so a
// co-located row (keyed by path) and a hub-spoke claim agree.
func TestCollectArgoClaims_PrefersPathOverAppSet(t *testing.T) {
	// An Application that is BOTH appset-owned AND has an env-overlay source path.
	item := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":            "billing-prod",
			"ownerReferences": []any{map[string]any{"kind": "ApplicationSet", "name": "billing"}},
		},
		"spec": map[string]any{
			"source":      map[string]any{"path": "apps/billing/overlays/prod"},
			"destination": map[string]any{"name": "prod", "namespace": "billing"},
		},
		"status": map[string]any{"resources": []any{
			map[string]any{"kind": "Deployment", "namespace": "billing", "name": "billing-api"},
		}},
	}}
	// A sibling so the appset is a recognized fan-out.
	sib := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":            "billing-dev",
			"ownerReferences": []any{map[string]any{"kind": "ApplicationSet", "name": "billing"}},
		},
		"spec": map[string]any{"source": map[string]any{"path": "apps/billing/overlays/dev"}},
	}}
	lister := &stubLister{items: []*unstructured.Unstructured{item, sib}}
	sp, asc, argoItems := argoApplicationFacts(context.Background(), lister)
	claims := collectArgoClaims(argoItems, sp, appSetFanouts(asc), nil)
	for _, c := range claims {
		if c.Identity != nil && c.Identity.Key == "billing-api" {
			continue
		}
		if c.Identity != nil && c.Identity.Source == SourceArgoAppSet {
			t.Fatalf("claim used appset over argo-path: %+v", c.Identity)
		}
	}
	// The billing-prod claim specifically must be argo-path (apps/billing), not appset (billing).
	var found *appIdentity
	for _, c := range claims {
		if len(c.Workloads) == 1 && c.Workloads[0].Name == "billing-api" {
			found = c.Identity
		}
	}
	if found == nil || found.Source != SourceArgoPath || found.Key != "apps/billing" {
		t.Fatalf("billing-prod claim = %+v, want argo-path key=apps/billing", found)
	}
}

// Flux Kustomization source paths yield a portable flux-source identity, the
// declared-origin parallel to argo-path. The row tier is 2 (TierFluxKustomize);
// the row key encodes the Kustomization (<ns>/Kustomization/<name>).
func TestIdentities_FluxSourcePathIsPortable(t *testing.T) {
	rows := []appRow{
		identRow("billing", "billing", 2, "flux-system/Kustomization/billing-staging", "repo.dev/billing:1"),
		identRow("billing", "billing", 2, "flux-system/Kustomization/billing-prod", "repo.dev/billing:2"),
	}
	flux := map[string]string{
		"flux-system/billing-staging": "apps/billing/overlays/staging",
		"flux-system/billing-prod":    "apps/billing/overlays/prod",
	}
	resolveAppIdentities(rows, nil, nil, nil, flux)
	for i := range rows {
		id := rows[i].Identity
		// Key is the name stem (the join key that unifies a declared-path instance
		// with a raw-but-corroborated sibling); the path is the declared SIGNAL that
		// makes it portable, cited in the evidence.
		if id == nil || id.Key != "billing" || id.Source != SourceFluxSource || !id.Portable || id.PathKey != "apps/billing" {
			t.Fatalf("row %d flux identity = %+v, want key=billing source=flux-source portable", i, id)
		}
		if !strings.Contains(id.Evidence, "Flux source path") {
			t.Errorf("flux evidence should cite Flux source path: %q", id.Evidence)
		}
	}
}

// fluxKustomizationFacts maps each Kustomization to its env-bearing path,
// emitting the namespaced key always and the bare name when unambiguous.
func TestFluxKustomizationFacts(t *testing.T) {
	ks := func(ns, name, path string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": ns, "name": name},
			"spec":     map[string]any{"path": path},
		}}
	}
	lister := &stubLister{items: []*unstructured.Unstructured{
		ks("flux-system", "billing-prod", "apps/billing/overlays/prod"),
		ks("flux-system", "infra", "clusters/base"), // no env segment → skipped
	}}
	got := fluxKustomizationFacts(context.Background(), lister)
	if got["flux-system/billing-prod"] != "apps/billing/overlays/prod" || got["billing-prod"] != "apps/billing/overlays/prod" {
		t.Fatalf("flux facts = %v, want billing-prod path under both keys", got)
	}
	if _, ok := got["flux-system/infra"]; ok {
		t.Fatalf("env-less path should be skipped: %v", got)
	}
}

func TestAddFluxKustomizationManagedSourceRefsParsesInventoryIDFromRight(t *testing.T) {
	ks := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "flux-system", "name": "platform"},
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "team_api_worker_apps_Deployment"},
		}}},
	}}
	got := map[string][]appSourceRef{}
	addFluxKustomizationManagedSourceRefs(context.Background(), &stubLister{items: []*unstructured.Unstructured{ks}}, got)

	refs := got[managedWorkloadKey("Deployment", "team", "api_worker")]
	if len(refs) != 1 {
		t.Fatalf("managed source refs = %#v, want one ref keyed by full workload name", got)
	}
	if refs[0].Name != "platform" || refs[0].Namespace != "flux-system" || refs[0].Tool != "fluxcd" {
		t.Fatalf("source ref = %+v, want flux-system/platform flux ref", refs[0])
	}
}
