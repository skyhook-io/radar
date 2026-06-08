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
	resolveAppIdentities(rows, map[string]string{"billing-staging": "billing/deploy/overlays/staging"}, nil)

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
	resolveAppIdentities(rows, nil, nil)

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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	}, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(tagged, nil, nil)
	for i := range tagged {
		tagged[i].Identity = nil
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, map[string]string{"team-b": "green"})
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	resolveAppIdentities(rows, nil, nil)
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
	if kind != "Application" || group != "argoproj.io" {
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
	got := argoSourcePaths(context.Background(), lister)
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
	got := argoSourcePaths(context.Background(), lister)
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
	}, nil)
	a, b := rows[0].Identity, rows[1].Identity
	if a == nil || b == nil || a.Key != "billing" || b.Key != "billing" || !a.Portable || !b.Portable {
		t.Fatalf("namespaced Argo path identities = %+v / %+v, want portable billing", a, b)
	}
}
