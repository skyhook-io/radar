package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
)

// resourceLister is the slice of the resource cache the app identity resolver
// needs — an interface so tests can stub the Argo Application feed.
type resourceLister interface {
	ListDynamicWithGroup(ctx context.Context, kind, namespace, group string) ([]*unstructured.Unstructured, error)
}

// App identity classification — grouping per-env instances of one logical app
// (billing@dev, billing-staging, channel-koala-backend-…) into an app group.
//
// CONTRACT: app identity grouping is classification, never row identity. Rows
// keep their overlay key untouched; a tagged row is byte-identical minus the
// Identity block.
// This sits ABOVE the pkg/subject overlay (which decides workload→app), so it
// is not a competing resolver — it groups the resolver's outputs by env, with
// provenance + confidence on the wire so the UI can render the grouping
// honestly and degrade to flat instances.
//
// Evidence tiers:
//   F0 (env)    — an explicit environment label on the workload or its
//                 namespace (envLabelKeys) — the user's env word beats every
//                 heuristic, but does not make the app key portable.
//   F1 (high)   — a declared source path with an env segment: the in-cluster
//                 Argo Application's spec.source(.s).path, e.g.
//                 billing/deploy/overlays/staging → stem billing/deploy.
//                 (The ApplicationSet NAME is deliberately not a key — env-
//                 scoped sets like platform-apps-staging would over-merge.)
//   F2 (medium) — name-stem env normalization (suffix billing-staging, prefix
//                 channel-koala-…, or an env namespace) corroborated by a
//                 shared image repository across ALL members. The repo check
//                 is what makes this safe: same repo = same artifact.
//
// Guards: ≥2 instances, ≥2 distinct envs, strict repo intersection for F2,
// and conflicting F1 path stems never merge through a shared name stem.

// appIdentity is the per-row app grouping classification on the wire.
type appIdentity struct {
	// Key is the app group's display identity (the shared name stem). Derived
	// classification — never an address; instance keys remain the only URLs.
	Key string `json:"key"`
	// Env is this instance's canonical env token (dev|staging|prod|…).
	Env string `json:"env"`
	// Confidence: high (declared upstream/path) | medium (local name/repo evidence).
	Confidence string `json:"confidence"`
	// Evidence is the human-readable why, rendered in the app group chip tooltip.
	Evidence string `json:"evidence"`
	// Portable means the grouping key came from declared upstream identity and can
	// participate in cross-cluster grouping. Local name/repo evidence must be
	// scoped by the fleet consumer.
	Portable bool `json:"portable,omitempty"`
}

// The ONLY hardcoded env vocabulary: the universal trio, kept solely because
// promotion DIRECTION (dev < staging < prod) cannot be derived from a
// snapshot. Everything else — qa, loadtest, e2e, whatever a team
// calls its environments — is DISCOVERED per cluster: a token qualifies as an
// env when the cluster's own structure says so (resolveAppIdentities, phase 1).
// Discovered tokens group and label but never rank (wrong lag arrows are
// trust-fatal; ranking beyond the trio needs explicit config).
var envCanonical = map[string]string{
	"dev": "dev", "development": "dev",
	"staging": "staging", "stage": "staging", "stg": "staging",
	"prod": "prod", "production": "prod", "prd": "prod",
}

func trioEnv(tok string) (string, bool) {
	c, ok := envCanonical[strings.ToLower(tok)]
	return c, ok
}

// Explicit env label keys, strongest evidence first. There is no official
// k8s environment label — app.kubernetes.io/environment is the de-facto
// extension of the recommended-labels set, plain environment/env are the
// common shorthands, and tags.datadoghq.com/env is Datadog's Unified Service
// Tagging key (already deployed in many fleets). Set any of these on a
// workload or its namespace and Radar takes your word over every heuristic.
var envLabelKeys = []string{
	"app.kubernetes.io/environment",
	"environment",
	"env",
	"tags.datadoghq.com/env",
}

func envLabelOf(lbls map[string]string) string {
	for _, k := range envLabelKeys {
		if v := strings.TrimSpace(lbls[k]); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// namespaceLister is the slice of the resource cache the resolver needs for
// namespace env labels.
type namespaceLister interface {
	Namespaces() listerscorev1.NamespaceLister
}

// namespaceEnvLabels maps namespace → explicit env label (envLabelKeys).
func namespaceEnvLabels(cache namespaceLister) map[string]string {
	out := map[string]string{}
	lister := cache.Namespaces()
	if lister == nil {
		return out
	}
	nss, err := lister.List(labels.Everything())
	if err != nil {
		return out
	}
	for _, ns := range nss {
		if v := envLabelOf(ns.Labels); v != "" {
			out[ns.Name] = v
		}
	}
	return out
}

// ── Env-token discovery ──────────────────────────────────────────────────────
//
// No hardcoded env vocabulary beyond the trio. A token qualifies as an env
// when the CLUSTER's structure says so:
//   - it's the trio (dev/staging/prod + synonyms) — always an env; or
//   - a declared GitOps path names it as the overlay segment; or
//   - it recurs across ≥3 viable stem-groups whose members spread over ≥2
//     namespaces (a stem-group = same stem, ≥2 rows, distinct tokens, shared
//     image repo); or
//   - it appears as a NAME AFFIX in a viable stem-group AND is itself a
//     namespace in the cluster (env-per-namespace corroboration).
// "billing-staging"/"billing" qualifies staging; a cluster-local token qualifies on
// recurrence + namespace existence; a customer's "loadtest"/"e2e" qualifies on
// THEIR cluster the same way — zero code, zero config. One-off oddities
// ("myapp-v2", a lone "load-test") never qualify.

// reading is one way a row could split into (stem, env token).
type reading struct {
	stem  string
	token string // raw lowercased token (canonicalized later)
	prio  int    // lower wins: 0 suffix, 1 prefix, 2 namespace, 3 ns segment
}

func readingsOf(name, ns string) []reading {
	var out []reading
	if i := strings.LastIndex(name, "-"); i > 0 && i < len(name)-1 {
		out = append(out, reading{stem: name[:i], token: strings.ToLower(name[i+1:]), prio: 0})
	}
	if i := strings.Index(name, "-"); i > 0 && i < len(name)-1 {
		out = append(out, reading{stem: name[i+1:], token: strings.ToLower(name[:i]), prio: 1})
	}
	if ns != "" {
		// A full namespace name is an env candidate only when it's a single
		// token ("dev", "loadtest") — "skyhook-clients-frps" is a
		// name, not an environment; only its segments may qualify.
		if !strings.ContainsAny(ns, "-_") {
			out = append(out, reading{stem: name, token: strings.ToLower(ns), prio: 2})
		}
		for _, seg := range strings.FieldsFunc(ns, func(r rune) bool { return r == '-' || r == '_' }) {
			if !strings.EqualFold(seg, ns) {
				out = append(out, reading{stem: name, token: strings.ToLower(seg), prio: 3})
			}
		}
	}
	return out
}

// canonicalEnvToken maps a qualified token to its display form: trio synonyms
// canonicalize; discovered tokens pass through as-is.
func canonicalEnvToken(tok string) string {
	if c, ok := trioEnv(tok); ok {
		return c
	}
	return tok
}

// pathStemEnv extracts (stem, env) from a declared GitOps source path using
// STRUCTURE, not vocabulary: the segment after an "overlays"/"envs"/
// "environments" directory is the env (kustomize convention); else a trio
// segment. The stem drops the
// env segment and the convention directory.
func pathStemEnv(path string) (stem, env string) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	kept := make([]string, 0, len(segs))
	prevWasOverlayDir := false
	for _, s := range segs {
		lower := strings.ToLower(s)
		if env == "" && prevWasOverlayDir {
			env = lower
			prevWasOverlayDir = false
			continue
		}
		if lower == "overlays" || lower == "envs" || lower == "environments" {
			prevWasOverlayDir = true
			continue
		}
		prevWasOverlayDir = false
		if env == "" {
			if c, ok := trioEnv(s); ok {
				env = c
				continue
			}
		}
		kept = append(kept, s)
	}
	if env == "" {
		return "", ""
	}
	return strings.Join(kept, "/"), env
}

// identityCandidate is one row's chosen env-instance reading, post-qualification.
type identityCandidate struct {
	row      *appRow
	stem     string
	env      string // canonical display token
	pathStem string // F1 declared stem ("" when none)
	labelEnv string // explicit env label ("" when none)
	repos    map[string]bool
	prio     int
	// adopted: this member's env token never qualified on its own — it joins
	// only because the stem-group's CORE proved itself (≥2 qualified envs),
	// and it never counts toward that proof. Single-token namespaces only;
	// adopting name affixes would re-admit variant suffixes (-cos/-ubuntu).
	adopted bool
}

// resolveAppIdentities tags app identity classifications onto rows, in place.
// Two phases: discover which tokens are envs from the cluster's structure,
// then form app groups using only qualified tokens. argoSourcePaths maps an
// in-cluster Argo Application name → its source path.
func resolveAppIdentities(rows []appRow, argoSourcePaths map[string]string, nsEnvLabels map[string]string) {
	type rowInfo struct {
		row      *appRow
		readings []reading
		repos    map[string]bool
		pathStem string
		pathEnv  string
		labelEnv string // explicit env label (workload, else namespace)
	}
	infos := make([]rowInfo, 0, len(rows))
	clusterNamespaces := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		ns := r.Namespace
		if ns == "" && len(r.Namespaces) == 1 {
			ns = r.Namespaces[0]
		}
		for _, n := range r.Namespaces {
			clusterNamespaces[strings.ToLower(n)] = true
		}
		if ns != "" {
			clusterNamespaces[strings.ToLower(ns)] = true
		}
		info := rowInfo{row: r, readings: readingsOf(r.Name, ns), repos: map[string]bool{}}
		wlEnv, wlEnvAgree := "", true
		for _, w := range r.Workloads {
			if repo := imageRepo(w.Image); repo != "" {
				info.repos[repo] = true
			}
			if w.envLabel != "" {
				if wlEnv == "" {
					wlEnv = w.envLabel
				} else if wlEnv != w.envLabel {
					wlEnvAgree = false // disagreeing labels — refuse the tier
				}
			}
		}
		if wlEnv != "" && wlEnvAgree {
			info.labelEnv = wlEnv
		} else if v := nsEnvLabels[ns]; v != "" {
			info.labelEnv = v
		}
		if r.Tier == 3 || r.Tier == 4 {
			for _, key := range argoSourcePathKeys(r.Key) {
				if p, ok := argoSourcePaths[key]; ok {
					if ps, pe := pathStemEnv(p); ps != "" {
						info.pathStem, info.pathEnv = ps, pe
					}
					break
				}
			}
		}
		infos = append(infos, info)
	}
	reposByRow := make(map[*appRow]map[string]bool, len(infos))
	for i := range infos {
		reposByRow[infos[i].row] = infos[i].repos
	}

	// Phase 1 — qualify tokens. Viable stem-group: ≥2 rows sharing a stem
	// under SOME reading, with ≥2 distinct tokens and a shared image repo.
	type groupStat struct {
		members map[*appRow]reading
		tokens  map[string]bool
	}
	rowNs := func(r *appRow) string {
		if r.Namespace != "" {
			return r.Namespace
		}
		if len(r.Namespaces) == 1 {
			return r.Namespaces[0]
		}
		return ""
	}
	stems := map[string]*groupStat{}
	for i := range infos {
		seen := map[string]bool{}
		for _, rd := range infos[i].readings {
			if rd.stem == "" || rd.token == "" || seen[rd.stem+"\x00"+rd.token] {
				continue
			}
			seen[rd.stem+"\x00"+rd.token] = true
			g := stems[rd.stem]
			if g == nil {
				g = &groupStat{members: map[*appRow]reading{}, tokens: map[string]bool{}}
				stems[rd.stem] = g
			}
			if prev, ok := g.members[infos[i].row]; !ok || rd.prio < prev.prio {
				g.members[infos[i].row] = rd
			}
			g.tokens[rd.token] = true
		}
	}
	tokenViableGroups := map[string]int{}
	tokenNsSpread := map[string]bool{}
	affixTokens := map[string]bool{}
	for i := range infos {
		for _, rd := range infos[i].readings {
			if rd.prio <= 1 {
				affixTokens[rd.token] = true
			}
		}
	}
	for _, g := range stems {
		if len(g.members) < 2 || len(g.tokens) < 2 {
			continue
		}
		// Viability is judged per repo-corroborated SUBSET: some repo must be
		// shared by ≥2 members carrying ≥2 distinct tokens. Computing one
		// intersection across ALL members would let a coincidence-named row
		// with a foreign repo suppress the whole group — and with it both the
		// trio tokens and any recurrence counting.
		repoRows := map[string][]*appRow{}
		for row := range g.members {
			for repo := range reposByRow[row] {
				repoRows[repo] = append(repoRows[repo], row)
			}
		}
		tokens := map[string]bool{}
		nss := map[string]bool{}
		for _, members := range repoRows {
			if len(members) < 2 {
				continue
			}
			sub := map[string]bool{}
			for _, row := range members {
				sub[g.members[row].token] = true
			}
			if len(sub) < 2 {
				continue
			}
			for _, row := range members {
				tokens[g.members[row].token] = true
				nss[rowNs(row)] = true
			}
		}
		if len(tokens) < 2 {
			continue
		}
		for tok := range tokens {
			tokenViableGroups[tok]++
			if len(nss) >= 2 {
				tokenNsSpread[tok] = true
			}
		}
	}
	qualified := map[string]bool{}
	for tok, n := range tokenViableGroups {
		_, isTrio := trioEnv(tok)
		switch {
		case isTrio:
			qualified[tok] = true
		case n >= 3 && tokenNsSpread[tok]:
			// Real env tokens recur across many apps AND their instances
			// isolate by namespace. Parallel variant dimensions (-cos/-ubuntu
			// gpu-plugin flavors all in kube-system) fail one or both.
			qualified[tok] = true
		case affixTokens[tok] && clusterNamespaces[tok]:
			// A name affix that is ALSO a namespace ("api-loadtest" + a
			// loadtest namespace) — the cluster corroborates the token.
			qualified[tok] = true
		}
	}
	// Declared overlay segments and explicit env labels always qualify.
	for i := range infos {
		if infos[i].pathEnv != "" {
			qualified[strings.ToLower(infos[i].pathEnv)] = true
		}
		if infos[i].labelEnv != "" {
			qualified[infos[i].labelEnv] = true
		}
	}

	// Phase 2 — each row's best QUALIFIED reading, then app-group formation with
	// the original validation (≥2 instances, ≥2 distinct envs, shared repo,
	// declared-stem conflicts refuse).
	byStem := map[string][]*identityCandidate{}
	for i := range infos {
		info := &infos[i]
		var chosen *reading
		better := func(a, b *reading) bool {
			_, aTrio := trioEnv(a.token)
			_, bTrio := trioEnv(b.token)
			if aTrio != bTrio {
				return aTrio // the universal ladder outranks discovered tokens
			}
			return a.prio < b.prio
		}
		for j := range info.readings {
			rd := info.readings[j]
			// The trio is universally recognized — its qualification must not
			// depend on any stem-group existing (formation still requires the
			// core to share a repo, so this loosens recognition, not proof).
			if _, isTrio := trioEnv(rd.token); !isTrio && !qualified[rd.token] {
				continue
			}
			if chosen == nil || better(&rd, chosen) {
				cp := rd
				chosen = &cp
			}
		}
		if chosen == nil && info.pathEnv == "" && info.labelEnv == "" {
			// Adoption candidate: a single-token namespace reading that didn't
			// qualify. Joins an app group only if the stem's core proves itself.
			for _, rd := range info.readings {
				if rd.prio == 2 {
					c := &identityCandidate{row: info.row, repos: info.repos, stem: rd.stem, env: canonicalEnvToken(rd.token), prio: rd.prio, adopted: true}
					byStem[c.stem] = append(byStem[c.stem], c)
					break
				}
			}
			continue
		}
		c := &identityCandidate{row: info.row, repos: info.repos, pathStem: info.pathStem, labelEnv: info.labelEnv}
		if chosen != nil {
			c.stem = chosen.stem
			c.env = canonicalEnvToken(chosen.token)
			c.prio = chosen.prio
		} else {
			c.stem = info.row.Name
			c.env = canonicalEnvToken(info.pathEnv)
		}
		// Env precedence: explicit label > declared path > structural reading.
		if info.pathEnv != "" {
			c.env = canonicalEnvToken(info.pathEnv)
		}
		if info.labelEnv != "" {
			c.env = canonicalEnvToken(info.labelEnv)
		}
		byStem[c.stem] = append(byStem[c.stem], c)
	}
	for stem, cands := range byStem {
		declared := map[string]bool{}
		for _, c := range cands {
			if c.pathStem != "" {
				declared[c.pathStem] = true
			}
		}
		if len(declared) > 1 {
			continue // ambiguous declarations — refuse to group by name stem
		}
		envs := map[string]bool{}
		coreCands := make([]*identityCandidate, 0, len(cands))
		for _, c := range cands {
			if !c.adopted {
				envs[c.env] = true
				coreCands = append(coreCands, c)
			}
		}
		if len(coreCands) < 2 || len(envs) < 2 {
			continue // the CORE must prove the app group; adoption never bootstraps
		}
		// Proof is computed over the core ONLY: an adoptee can join a proven
		// app group or stay out of it, but a coincidence-named row in some
		// single-token namespace must never veto the core's corroboration.
		shared := sharedRepo(coreCands)
		anyDeclared := len(declared) == 1
		if shared == "" && !anyDeclared {
			shared, coreCands = uniqueRepoCore(coreCands)
			if shared == "" {
				continue // uncorroborated or ambiguous name coincidence — refuse
			}
		}
		members := coreCands
		for _, c := range cands {
			if c.adopted && shared != "" && c.repos[shared] {
				members = append(members, c)
			}
		}
		for _, c := range members {
			conf, why, portable := "medium", "", false
			switch {
			case c.pathStem != "":
				conf = "high"
				portable = true
				why = "Argo CD source path " + c.pathStem + " (env overlay " + c.env + ")"
			case c.labelEnv != "":
				why = fmt.Sprintf("environment label %q + name/repo evidence", c.labelEnv)
			case c.prio >= 2:
				why = fmt.Sprintf("namespace stem %q + shared image repo %s", stem, shared)
			case shared != "":
				why = fmt.Sprintf("name stem %q + shared image repo %s", stem, shared)
			default:
				continue
			}
			c.row.Identity = &appIdentity{Key: stem, Env: c.env, Confidence: conf, Evidence: why, Portable: portable}
		}
	}
}

// sharedRepo returns one image repository present in EVERY candidate's repo
// set ("" when none). Deterministic pick (lexicographic) for stable evidence.
func sharedRepo(cands []*identityCandidate) string {
	if len(cands) == 0 {
		return ""
	}
	repos := map[string]bool{}
	for _, c := range cands {
		for repo := range c.repos {
			repos[repo] = true
		}
	}
	common := []string{}
	for repo := range repos {
		ok := true
		for _, c := range cands {
			if !c.repos[repo] {
				ok = false
				break
			}
		}
		if ok {
			common = append(common, repo)
		}
	}
	if len(common) == 0 {
		return ""
	}
	sort.Strings(common)
	return common[0]
}

func uniqueRepoCore(cands []*identityCandidate) (string, []*identityCandidate) {
	byRepo := map[string][]*identityCandidate{}
	for _, c := range cands {
		for repo := range c.repos {
			byRepo[repo] = append(byRepo[repo], c)
		}
	}
	var pickedRepo string
	var picked []*identityCandidate
	for repo, group := range byRepo {
		if len(group) < 2 || distinctEnvCount(group) < 2 {
			continue
		}
		if pickedRepo != "" {
			return "", nil
		}
		pickedRepo = repo
		picked = group
	}
	return pickedRepo, picked
}

func distinctEnvCount(cands []*identityCandidate) int {
	seen := map[string]bool{}
	for _, c := range cands {
		seen[c.env] = true
	}
	return len(seen)
}

func argoSourcePathKeys(rowKey string) []string {
	name := appNameFromKey(rowKey)
	ns := namespaceFromKey(rowKey)
	if ns == "" {
		return []string{name}
	}
	return []string{ns + "/" + name, name}
}

// argoSourcePaths maps each in-cluster Argo Application to a source path
// carrying an env segment — the F1 evidence feed. Namespaced keys are always
// emitted; bare-name fallback is emitted only when the name is unambiguous.
// Multi-source apps pick the first env-bearing path. Missing CRD / no Argo →
// empty map (F2 still works).
func argoSourcePaths(ctx context.Context, cache resourceLister) map[string]string {
	out := map[string]string{}
	byName := map[string]string{}
	ambiguous := map[string]bool{}
	items, err := cache.ListDynamicWithGroup(ctx, "Application", "", "argoproj.io")
	if err != nil {
		return out
	}
	for _, item := range items {
		spec, _ := item.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		paths := []string{}
		if src, _ := spec["source"].(map[string]any); src != nil {
			if p, _ := src["path"].(string); p != "" {
				paths = append(paths, p)
			}
		}
		if srcs, _ := spec["sources"].([]any); srcs != nil {
			for _, s := range srcs {
				if m, _ := s.(map[string]any); m != nil {
					if p, _ := m["path"].(string); p != "" {
						paths = append(paths, p)
					}
				}
			}
		}
		for _, p := range paths {
			if stem, _ := pathStemEnv(p); stem != "" {
				name := item.GetName()
				nsKey := name
				if ns := item.GetNamespace(); ns != "" {
					nsKey = ns + "/" + name
				}
				out[nsKey] = p
				if prev, exists := byName[name]; exists && prev != p {
					delete(byName, name)
					ambiguous[name] = true
					break
				}
				if !ambiguous[name] {
					byName[name] = p
				}
				break
			}
		}
	}
	for name, path := range byName {
		out[name] = path
	}
	return out
}
