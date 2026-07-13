package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/packages"
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
	// Source is the machine-readable provenance tier, so the fleet consumer can
	// decide cross-cluster promotion on the SOURCE, not on a human evidence
	// string. Declared GitOps ORIGINS (argo-path, argo-appset, flux-source) name a
	// shared upstream and are collision-free across clusters; NAMES (label,
	// name-stem, namespace) collide (two teams' "redis") and stay per-cluster.
	Source string `json:"source,omitempty"`
	// PathKey is the declared source-PATH stem (argo-path / flux-source only). The
	// display Key is the NAME stem (so a declared-path instance folds with a
	// raw-but-corroborated sibling WITHIN a cluster), but two different apps can
	// share a name while declaring different paths — so the fleet fold disambiguates
	// portable rows by PathKey, keeping same-name/different-path apps apart across
	// clusters. Empty for non-path tiers (their own Key is already collision-safe).
	PathKey string `json:"pathKey,omitempty"`
}

// App identity provenance tiers (appIdentity.Source). The declaredOrigin set is
// the only one a fleet consumer may auto-promote to cross-cluster portable.
const (
	SourceExplicit   = "explicit"    // app.skyhook.io/app annotation — user-declared, authoritative
	SourceArgoPath   = "argo-path"   // declared Argo Application source path (origin)
	SourceArgoAppSet = "argo-appset" // declared ApplicationSet fan-out (origin)
	SourceFluxSource = "flux-source" // declared Flux source ref (origin)
	SourceLabel      = "label"       // app.kubernetes.io/name — a NAME, collision-prone
	SourceNameStem   = "name-stem"   // inferred name-stem — a NAME
	SourceNamespace  = "namespace"   // namespace stem — a NAME
)

// appIdentityAnnotation is the explicit, user-declared cross-cluster app key. Set
// the SAME value on an app's workloads in every cluster and Radar folds them — the
// canonical answer to "how do I force grouping?" for non-GitOps apps. Deliberate
// (zero-collision), so it is authoritative over every inferred/declared tier.
const appIdentityAnnotation = "app.skyhook.io/app"

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
	row        *appRow
	stem       string
	env        string // canonical display token
	pathStem   string // F1 declared stem ("" when none)
	pathSource string // SourceArgoPath | SourceFluxSource for the declared path stem
	labelEnv   string // explicit env label ("" when none)
	repos      map[string]bool
	prio       int
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
func resolveAppIdentities(rows []appRow, argoSourcePaths map[string]string, appSetByKey map[string]appSetFanout, nsEnvLabels map[string]string, fluxPaths map[string]string) {
	type rowInfo struct {
		row        *appRow
		readings   []reading
		repos      map[string]bool
		pathStem   string
		pathEnv    string
		pathSource string // SourceArgoPath | SourceFluxSource for the declared path stem
		labelEnv   string // explicit env label (workload, else namespace)
		nameLabel  string // app.kubernetes.io/name, when the row's workloads agree
		appAnno    string // app.skyhook.io/app explicit declaration, when workloads agree
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
		wlName, wlNameAgree := "", true
		wlApp, wlAppAgree, wlAppCount := "", true, 0
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
			if w.nameLabel != "" {
				if wlName == "" {
					wlName = w.nameLabel
				} else if wlName != w.nameLabel {
					wlNameAgree = false // disagreeing app names — not one app
				}
			}
			if w.appAnnotation != "" {
				wlAppCount++
				if wlApp == "" {
					wlApp = w.appAnnotation
				} else if wlApp != w.appAnnotation {
					wlAppAgree = false // disagreeing explicit keys — not one app
				}
			}
		}
		if wlEnv != "" && wlEnvAgree {
			info.labelEnv = wlEnv
		} else if v := nsEnvLabels[ns]; v != "" {
			info.labelEnv = v
		}
		if wlName != "" && wlNameAgree {
			info.nameLabel = wlName
		}
		// Explicit identity is authoritative + portable, so demand EVERY workload
		// in the row carry the same value — a partial annotation must not promote
		// the whole app (including unannotated components) to a portable identity.
		if wlApp != "" && wlAppAgree && wlAppCount == len(r.Workloads) {
			info.appAnno = wlApp
		}
		if r.Tier == 3 || r.Tier == 4 {
			for _, key := range argoSourcePathKeys(r.Key) {
				if p, ok := argoSourcePaths[key]; ok {
					if ps, pe := pathStemEnv(p); ps != "" {
						info.pathStem, info.pathEnv, info.pathSource = ps, pe, SourceArgoPath
					}
					break
				}
			}
		} else if r.Tier == 2 { // Flux Kustomization — its source path is a declared origin, like Argo's.
			for _, key := range argoSourcePathKeys(r.Key) {
				if p, ok := fluxPaths[key]; ok {
					if ps, pe := pathStemEnv(p); ps != "" {
						info.pathStem, info.pathEnv, info.pathSource = ps, pe, SourceFluxSource
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
		c := &identityCandidate{row: info.row, repos: info.repos, pathStem: info.pathStem, pathSource: info.pathSource, labelEnv: info.labelEnv}
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
			conf, why, portable, source, pathKey := "medium", "", false, SourceNameStem, ""
			switch {
			case c.pathStem != "":
				conf = "high"
				portable = true
				source = c.pathSource
				pathKey = c.pathStem
				origin := "Argo CD source path"
				if c.pathSource == SourceFluxSource {
					origin = "Flux source path"
				}
				why = origin + " " + c.pathStem + " (env overlay " + c.env + ")"
			case c.labelEnv != "":
				why = fmt.Sprintf("environment label %q + name/repo evidence", c.labelEnv)
			case c.prio >= 2:
				source = SourceNamespace
				why = fmt.Sprintf("namespace stem %q + shared image repo %s", stem, shared)
			case shared != "":
				why = fmt.Sprintf("name stem %q + shared image repo %s", stem, shared)
			default:
				continue
			}
			c.row.Identity = &appIdentity{Key: stem, Env: c.env, Confidence: conf, Evidence: why, Portable: portable, Source: source, PathKey: pathKey}
		}
	}

	// Env for the standalone label/explicit tiers: an env already resolved by a
	// stronger signal wins, else the explicit env label, else a declared overlay
	// env, else the row's best structural reading (trio outranks discovered tokens,
	// same precedence as phase 2's better()).
	resolveStandaloneEnv := func(info *rowInfo) string {
		switch {
		case info.row.Identity != nil && info.row.Identity.Env != "":
			return info.row.Identity.Env
		case info.labelEnv != "":
			return canonicalEnvToken(info.labelEnv)
		case info.pathEnv != "":
			return canonicalEnvToken(info.pathEnv)
		}
		for _, rd := range info.readings {
			if _, isTrio := trioEnv(rd.token); isTrio {
				return canonicalEnvToken(rd.token)
			}
		}
		for _, rd := range info.readings {
			if qualified[rd.token] {
				return canonicalEnvToken(rd.token)
			}
		}
		return ""
	}

	// Label tier — app.kubernetes.io/name is an explicit, cluster-agnostic app
	// identity the chart/author declared. Unlike a name stem it needs no
	// affix-stripping and no in-cluster group to corroborate, so it stands alone
	// (single-instance rows included) and gives a clean, cluster-agnostic key.
	//
	// It is NOT portable on its own, though: a bare label is a per-cluster
	// identity, not a cross-cluster guarantee — a generic name ("api", "web") or
	// a reused chart name would otherwise collapse unrelated apps across clusters.
	// Cross-cluster folding is the fleet consumer's corroborated decision (it has
	// the cluster names + cross-cluster repo/env signal the per-cluster resolver
	// lacks). The label still upgrades the weaker name-stem / namespace tiers and
	// does NOT override a declared Argo source-path identity.
	for i := range infos {
		info := &infos[i]
		if info.nameLabel == "" {
			continue
		}
		if cur := info.row.Identity; cur != nil && cur.Confidence == "high" {
			continue
		}
		info.row.Identity = &appIdentity{
			Key:        info.nameLabel,
			Env:        resolveStandaloneEnv(info),
			Confidence: "high",
			Evidence:   fmt.Sprintf("app.kubernetes.io/name=%q", info.nameLabel),
			Portable:   false,
			Source:     SourceLabel,
		}
	}

	// ApplicationSet fan-out tier — a single-app fan-out set DECLARES that its
	// children are env-variants of one app, so its stem is a portable identity.
	// The join is the set ownership (declared); appSetFanouts already confirmed the
	// shape. Overrides the weaker name/label tiers but NOT a declared Argo source
	// path (an app keeps one GitOps key across clusters) or the explicit tier.
	if len(appSetByKey) > 0 {
		for i := range infos {
			info := &infos[i]
			// Argo rows only (tier 3 tracking-id / 4 instance, as in the F1 path
			// gate above). A raw or label-only workload that merely shares a name
			// with an ApplicationSet child must NOT match the set via the bare-name
			// key fallback — that would stamp a false portable identity onto an app
			// the set doesn't manage.
			if info.row.Tier != 3 && info.row.Tier != 4 {
				continue
			}
			if cur := info.row.Identity; cur != nil && (cur.Source == SourceArgoPath || cur.Source == SourceExplicit) {
				continue
			}
			var fan *appSetFanout
			for _, key := range argoSourcePathKeys(info.row.Key) {
				if f, ok := appSetByKey[key]; ok {
					fan = &f
					break
				}
			}
			if fan == nil {
				continue
			}
			env := fan.env
			if env == "" {
				env = resolveStandaloneEnv(info)
			}
			info.row.Identity = &appIdentity{
				Key:        fan.stem,
				Env:        env,
				Confidence: "high",
				Evidence:   fmt.Sprintf("ApplicationSet fan-out %q (env %s)", fan.stem, env),
				Portable:   true,
				Source:     SourceArgoAppSet,
			}
		}
	}

	// Explicit tier — app.skyhook.io/app is the user's deliberate cross-cluster
	// declaration: authoritative (overrides every inferred/declared tier) and
	// portable (the user opted in, so it is collision-free by construction).
	for i := range infos {
		info := &infos[i]
		if info.appAnno == "" {
			continue
		}
		info.row.Identity = &appIdentity{
			Key:        info.appAnno,
			Env:        resolveStandaloneEnv(info),
			Confidence: "high",
			Evidence:   fmt.Sprintf("%s=%q", appIdentityAnnotation, info.appAnno),
			Portable:   true,
			Source:     SourceExplicit,
		}
	}

	// Informational name-stem match key. Ships alongside the exact evidence keys
	// so the surface can show/debug how a name-stem/namespace app was grouped;
	// the client EXCLUDES it from event-evidence matching, because a bare name
	// stem is not a label an event carries.
	for i := range rows {
		r := &rows[i]
		if id := r.Identity; id != nil && (id.Source == SourceNameStem || id.Source == SourceNamespace) && id.Key != "" {
			r.MatchKeys = append(r.MatchKeys, "name-stem:"+id.Key)
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

// fluxKustomizationFacts maps each Flux Kustomization to its env-bearing source
// path — the flux-source identity feed, mirroring the Argo source-path map. A
// Kustomization's spec.path is a declared origin just like an Argo Application's.
// Namespaced key always emitted; bare-name fallback only when unambiguous.
func fluxKustomizationFacts(ctx context.Context, cache resourceLister) map[string]string {
	out := map[string]string{}
	byName := map[string]string{}
	ambiguous := map[string]bool{}
	items, err := cache.ListDynamicWithGroup(ctx, "Kustomization", "", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		return out
	}
	for _, item := range items {
		spec, _ := item.Object["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		p, _ := spec["path"].(string)
		if p == "" {
			continue
		}
		if stem, _ := pathStemEnv(p); stem == "" {
			continue
		}
		name := item.GetName()
		nsKey := name
		if ns := item.GetNamespace(); ns != "" {
			nsKey = ns + "/" + name
		}
		out[nsKey] = p
		if prev, exists := byName[name]; exists && prev != p {
			delete(byName, name)
			ambiguous[name] = true
		} else if !ambiguous[name] {
			byName[name] = p
		}
	}
	for name, path := range byName {
		out[name] = path
	}
	return out
}

// nameSegments splits a resource name on - and _ into its tokens.
func nameSegments(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
}

// trivialStems recur across unrelated apps, so they must never anchor a grouping.
var trivialStems = map[string]bool{
	"api": true, "app": true, "web": true, "ui": true, "svc": true,
	"service": true, "server": true, "worker": true, "frontend": true,
	"backend": true, "gateway": true, "proxy": true,
}

func isTrivialStem(stem string) bool {
	return len(stem) < 3 || trivialStems[strings.ToLower(stem)]
}

// argoChild is one ApplicationSet-generated Application — the appKey forms it
// maps to (ns/name + bare name) plus the bare Application name used to test the
// set's fan-out shape.
type argoChild struct {
	keys []string
	name string
}

// appSetOwnerName returns the ApplicationSet that generated this Application:
// the controller ownerReference (survives the cache transform), falling back to
// the argocd.argoproj.io/application-set-name label when a GC/preserve policy
// drops the ownerRef. "" when the Application is not set-generated.
func appSetOwnerName(item *unstructured.Unstructured) string {
	for _, ref := range item.GetOwnerReferences() {
		if ref.Kind == "ApplicationSet" && ref.Name != "" {
			return ref.Name
		}
	}
	return item.GetLabels()["argocd.argoproj.io/application-set-name"]
}

// argoApplicationFacts lists in-cluster Argo Applications once and returns the F1
// source-path feed (appKey → env-bearing path) plus the ApplicationSet → children
// grouping that drives the declared argo-appset fan-out tier. Missing CRD / no
// Argo → empty maps (the name/label tiers still work).
func argoApplicationFacts(ctx context.Context, cache resourceLister) (sourcePaths map[string]string, appSetChildren map[string][]argoChild, items []*unstructured.Unstructured) {
	sourcePaths = map[string]string{}
	appSetChildren = map[string][]argoChild{}
	byName := map[string]string{}
	ambiguous := map[string]bool{}
	items, err := cache.ListDynamicWithGroup(ctx, "Application", "", "argoproj.io")
	if err != nil {
		return sourcePaths, appSetChildren, nil
	}
	for _, item := range items {
		name := item.GetName()
		nsKey := name
		if ns := item.GetNamespace(); ns != "" {
			nsKey = ns + "/" + name
		}
		if set := appSetOwnerName(item); set != "" {
			appSetChildren[set] = append(appSetChildren[set], argoChild{keys: []string{nsKey, name}, name: name})
		}

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
				sourcePaths[nsKey] = p
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
		sourcePaths[name] = path
	}
	return sourcePaths, appSetChildren, items
}

// argoWorkloadKinds are the status.resources kinds the hub matches against a
// destination cluster's app rows. Config/Service/RBAC resources are not workloads
// and would mis-target rows, so only true workloads carry a claim.
var argoWorkloadKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true,
	"ReplicaSet": true, "CronJob": true, "Job": true, "Rollout": true,
}

// collectArgoClaims emits one claim per Argo Application that carries a declared
// identity (an Argo source-path stem, or a validated ApplicationSet env fan-out),
// so the hub can stamp that identity onto the destination cluster's workload rows
// in a hub-spoke topology. Applications with no declared identity are skipped —
// name/label identity never propagates across clusters.
func collectArgoClaims(items []*unstructured.Unstructured, sourcePaths map[string]string, appSetByKey map[string]appSetFanout, namespaces []string) []argoClaim {
	// Scope claims to the caller's visibility. namespaces is nil for full access
	// (no-auth local / the fleet hub), a non-nil EMPTY slice for explicit no
	// access (noNamespaceAccess), and a non-nil non-empty set when scoped. A claim
	// exposes an Application's destination + managed workloads, so a no-access
	// caller gets nothing, and a scoped caller gets a claim only when it can see at
	// least one of the workloads the Application MANAGES — filtering by the
	// workload namespaces, not the Application's (which often lives in argocd).
	if noNamespaceAccess(namespaces) {
		return nil
	}
	var allowed map[string]bool
	if namespaces != nil {
		allowed = make(map[string]bool, len(namespaces))
		for _, ns := range namespaces {
			allowed[ns] = true
		}
	}
	var claims []argoClaim
	for _, item := range items {
		name := item.GetName()
		nsKey := name
		if ns := item.GetNamespace(); ns != "" {
			nsKey = ns + "/" + name
		}

		// Declared identity: an env-bearing source path FIRST, else an
		// ApplicationSet fan-out. This MUST match resolveAppIdentities' row-level
		// precedence (argo-path outranks argo-appset) — otherwise a co-located row
		// keyed by its path and the claim keyed by its set would disagree and fail
		// to fold.
		var id *appIdentity
		if path, ok := sourcePaths[nsKey]; ok {
			if stem, env := pathStemEnv(path); stem != "" {
				id = &appIdentity{Key: stem, Env: env, Confidence: "high", Portable: true, Source: SourceArgoPath,
					Evidence: "Argo CD source path " + stem + " (env overlay " + env + ")"}
			}
		}
		if id == nil {
			if fan, ok := appSetByKey[nsKey]; ok {
				id = &appIdentity{Key: fan.stem, Env: fan.env, Confidence: "high", Portable: true, Source: SourceArgoAppSet,
					Evidence: fmt.Sprintf("ApplicationSet fan-out %q (env %s)", fan.stem, fan.env)}
			}
		}
		if id == nil {
			continue
		}

		spec, _ := item.Object["spec"].(map[string]any)
		dest, _ := spec["destination"].(map[string]any)
		claim := argoClaim{Identity: id}
		if dest != nil {
			claim.DestServer, _ = dest["server"].(string)
			claim.DestName, _ = dest["name"].(string)
			claim.DestNamespace, _ = dest["namespace"].(string)
		}
		claim.Workloads = argoManagedWorkloads(item)
		if allowed != nil && !managedWorkloadVisible(claim.Workloads, allowed) {
			continue // scoped caller can't see any workload this Application manages
		}
		claims = append(claims, claim)
	}
	return claims
}

// managedWorkloadVisible reports whether the caller (allowed namespace set) can
// see at least one of the workloads an Argo claim manages.
func managedWorkloadVisible(workloads []workloadRef, allowed map[string]bool) bool {
	for _, w := range workloads {
		if allowed[w.Namespace] {
			return true
		}
	}
	return false
}

// argoManagedWorkloads extracts the workload resources an Argo Application
// manages from status.resources (the hub matches these against destination rows).
func argoManagedWorkloads(item *unstructured.Unstructured) []workloadRef {
	resources, _, _ := unstructured.NestedSlice(item.Object, "status", "resources")
	var out []workloadRef
	for _, r := range resources {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		kind, _ := m["kind"].(string)
		if !argoWorkloadKinds[kind] {
			continue
		}
		ns, _ := m["namespace"].(string)
		nm, _ := m["name"].(string)
		if nm == "" {
			continue
		}
		out = append(out, workloadRef{Kind: kind, Namespace: ns, Name: nm})
	}
	return out
}

func setStrictSourceRef(r *appRow, ins []appWorkloadInput) {
	if len(ins) == 0 {
		return
	}
	var ref *appSourceRef
	for _, in := range ins {
		if in.source == nil {
			return
		}
		if ref == nil {
			cp := *in.source
			ref = &cp
			continue
		}
		if !sameSourceRef(ref, in.source) {
			r.SourceConflict = true
			return
		}
	}
	r.SourceRef = ref
	r.sourceStrict = true
}

func enrichRowsWithManagedSourceRefs(ctx context.Context, cache resourceLister, rows []appRow, argoItems []*unstructured.Unstructured) {
	sources := map[string][]appSourceRef{}
	addArgoManagedSourceRefs(sources, argoItems)
	addFluxKustomizationManagedSourceRefs(ctx, cache, sources)
	if len(sources) == 0 {
		return
	}
	for i := range rows {
		ref := commonManagedSourceRef(rows[i].Workloads, sources)
		if ref == nil {
			continue
		}
		mergeSourceRef(&rows[i], ref)
	}
}

func enrichRowsWithArgoStatus(rows []appRow, items []*unstructured.Unstructured) {
	byKey := make(map[string]*unstructured.Unstructured, len(items))
	for _, item := range items {
		if item != nil && item.GetNamespace() != "" && item.GetName() != "" {
			byKey[item.GetNamespace()+"/"+item.GetName()] = item
		}
	}
	for i := range rows {
		ref := rows[i].SourceRef
		if ref == nil || ref.Tool != "argocd" || ref.Kind != "Application" {
			continue
		}
		item := byKey[ref.Namespace+"/"+ref.Name]
		if item == nil {
			continue
		}
		syncStatus, _, _ := unstructured.NestedString(item.Object, "status", "sync", "status")
		healthStatus, _, _ := unstructured.NestedString(item.Object, "status", "health", "status")
		if syncStatus == "" && healthStatus == "" {
			continue
		}
		rows[i].SourceStatus = &appSourceStatus{Sync: syncStatus, Health: healthStatus}
		rows[i].Health = string(packages.WorseHealth(packages.Health(rows[i].RuntimeHealth), argoDeliveryHealth(syncStatus, healthStatus)))
	}
}

func argoDeliveryHealth(syncStatus, healthStatus string) packages.Health {
	health := packages.HealthNeutral
	switch strings.ToLower(healthStatus) {
	case "healthy":
		health = packages.HealthHealthy
	case "progressing":
		health = packages.HealthNeutral
	case "degraded":
		health = packages.HealthDegraded
	case "missing":
		health = packages.HealthUnhealthy
	case "suspended":
		health = packages.HealthNeutral
	case "unknown":
		health = packages.HealthNeutral
	}
	if strings.EqualFold(syncStatus, "OutOfSync") {
		health = packages.WorseHealth(health, packages.HealthDegraded)
	}
	return health
}

func addArgoManagedSourceRefs(out map[string][]appSourceRef, items []*unstructured.Unstructured) {
	for _, item := range items {
		if item == nil || item.GetName() == "" || item.GetNamespace() == "" {
			continue
		}
		ref := appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: item.GetNamespace(), Name: item.GetName()}
		destNamespace := ""
		if spec, _ := item.Object["spec"].(map[string]any); spec != nil {
			if dest, _ := spec["destination"].(map[string]any); dest != nil {
				destNamespace, _ = dest["namespace"].(string)
			}
		}
		resources, _, _ := unstructured.NestedSlice(item.Object, "status", "resources")
		for _, res := range resources {
			resMap, _ := res.(map[string]any)
			if resMap == nil {
				continue
			}
			kind, _ := resMap["kind"].(string)
			if !argoWorkloadKinds[kind] {
				continue
			}
			name, _ := resMap["name"].(string)
			ns, _ := resMap["namespace"].(string)
			if ns == "" {
				ns = destNamespace
			}
			addManagedSourceRef(out, kind, ns, name, ref)
		}
	}
}

func addFluxKustomizationManagedSourceRefs(ctx context.Context, cache resourceLister, out map[string][]appSourceRef) {
	items, err := cache.ListDynamicWithGroup(ctx, "Kustomization", "", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		return
	}
	for _, item := range items {
		if item == nil || item.GetName() == "" || item.GetNamespace() == "" {
			continue
		}
		ref := appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: item.GetNamespace(), Name: item.GetName()}
		entries, _, _ := unstructured.NestedSlice(item.Object, "status", "inventory", "entries")
		for _, entry := range entries {
			entryMap, _ := entry.(map[string]any)
			if entryMap == nil {
				continue
			}
			id, _ := entryMap["id"].(string)
			kind, namespace, name, ok := parseFluxInventoryWorkloadID(id)
			if !ok {
				continue
			}
			if !argoWorkloadKinds[kind] {
				continue
			}
			addManagedSourceRef(out, kind, namespace, name, ref)
		}
	}
}

func parseFluxInventoryWorkloadID(id string) (kind, namespace, name string, ok bool) {
	parts := strings.Split(id, "_")
	if len(parts) < 4 {
		return "", "", "", false
	}
	kind = parts[len(parts)-1]
	namespace = parts[0]
	name = strings.Join(parts[1:len(parts)-2], "_")
	if kind == "" || namespace == "" || name == "" {
		return "", "", "", false
	}
	return kind, namespace, name, true
}

func addManagedSourceRef(out map[string][]appSourceRef, kind, namespace, name string, ref appSourceRef) {
	if kind == "" || namespace == "" || name == "" {
		return
	}
	key := managedWorkloadKey(kind, namespace, name)
	for _, existing := range out[key] {
		if sameSourceRef(&existing, &ref) {
			return
		}
	}
	out[key] = append(out[key], ref)
}

func commonManagedSourceRef(workloads []appWorkload, sources map[string][]appSourceRef) *appSourceRef {
	if len(workloads) == 0 {
		return nil
	}
	var common []appSourceRef
	for i, w := range workloads {
		refs := sources[managedWorkloadKey(w.Kind, w.Namespace, w.Name)]
		if len(refs) == 0 {
			return nil
		}
		if i == 0 {
			common = append(common, refs...)
			continue
		}
		next := make([]appSourceRef, 0, len(common))
		for _, a := range common {
			for _, b := range refs {
				if sameSourceRef(&a, &b) {
					next = append(next, a)
					break
				}
			}
		}
		common = next
		if len(common) == 0 {
			return nil
		}
	}
	if len(common) != 1 {
		return nil
	}
	cp := common[0]
	return &cp
}

func managedWorkloadKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// appSetFanout is one child's declared identity within a single-app fan-out set.
type appSetFanout struct {
	stem string
	env  string // canonical trio env
}

// appSetFanouts decides, per ApplicationSet, whether the set is a SINGLE-APP
// fan-out (one app across envs) — the only shape whose set name is a valid app
// identity — and returns the declared stem+env for each child appKey. A set is a
// fan-out only when its children are identical except for ONE token position
// whose values are ALL trio envs (dev/staging/prod). A multi-app BUNDLE set
// (children with differing app stems) or a cluster/region fan-out (differing
// tokens that aren't trio envs — those get their env from the hub) yields
// nothing: the set name would over-merge unrelated apps.
//
// This is bounded to one declared sibling set, so it is NOT a global name-stem
// match — the join is the ApplicationSet ownership; the token check only confirms
// the set's shape.
func appSetFanouts(appSetChildren map[string][]argoChild) map[string]appSetFanout {
	out := map[string]appSetFanout{}
	for _, children := range appSetChildren {
		if len(children) < 2 {
			continue
		}
		segsByChild := make([][]string, len(children))
		width := -1
		uniform := true
		for i, c := range children {
			segsByChild[i] = nameSegments(c.name)
			if width == -1 {
				width = len(segsByChild[i])
			} else if len(segsByChild[i]) != width {
				uniform = false
			}
		}
		if !uniform || width < 2 {
			continue // children must share the SAME shape to be one app's env-fan-out
		}
		// Find the single token position that varies; every other must be constant.
		varyPos := -1
		ok := true
		for pos := 0; pos < width && ok; pos++ {
			differs := false
			for i := 1; i < len(segsByChild); i++ {
				if !strings.EqualFold(segsByChild[i][pos], segsByChild[0][pos]) {
					differs = true
					break
				}
			}
			if differs {
				if varyPos != -1 {
					ok = false // more than one varying position — not a clean env-fan-out
				}
				varyPos = pos
			}
		}
		if !ok || varyPos == -1 {
			continue
		}
		// The varying tokens must ALL be trio envs (declared single-app fan-out);
		// otherwise the set varies by app or by cluster, not by env.
		allEnv := true
		for i := range children {
			if _, isTrio := trioEnv(segsByChild[i][varyPos]); !isTrio {
				allEnv = false
				break
			}
		}
		if !allEnv {
			continue
		}
		stemSegs := append([]string{}, segsByChild[0][:varyPos]...)
		stemSegs = append(stemSegs, segsByChild[0][varyPos+1:]...)
		stem := strings.Join(stemSegs, "-")
		if isTrivialStem(stem) {
			continue
		}
		for i, c := range children {
			env, _ := trioEnv(segsByChild[i][varyPos])
			for _, k := range c.keys {
				out[k] = appSetFanout{stem: stem, env: env}
			}
		}
	}
	return out
}
