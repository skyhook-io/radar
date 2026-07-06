// Shared model for the Applications surface — host-agnostic. The OSS single-
// cluster view and (eventually) the Cloud fleet view both build on these types
// and helpers. No React, no fetching.
//
// The wire shape mirrors radar OSS's GET /api/applications response
// (internal/server/applications.go). Field names match the Go json tags.

export type AppWorkloadClass = 'service' | 'worker' | 'job' | 'mixed' | 'unknown'
export type AppHealth = 'healthy' | 'degraded' | 'unhealthy' | 'neutral' | 'unknown'

export interface AppWorkload {
  kind: string
  namespace: string
  name: string
  workload_class?: AppWorkloadClass
  image?: string
  version?: string
  appVersion?: string
  health: string
  ready: number
  desired: number
  restarts: number
  reason?: string
}

export interface AppRelationships {
  services?: string[]
  ingresses?: string[]
  routes?: string[]
  configs?: number
  scalers?: number
  storage?: number
  pdbs?: number
}

export interface AppEvent {
  type: string
  reason: string
  message?: string
  count: number
  object: string
  firstSeen?: string
  lastSeen?: string
}

export interface AppSourceRef {
  type: 'gitops' | 'helm'
  tool?: 'argocd' | 'fluxcd' | 'helm' | string
  group?: string
  kind: string
  namespace: string
  name: string
}

export interface AppHistorySummary {
  state: 'change' | 'incident' | 'none'
  title: string
  detail?: string
  timestamp?: string
}

export interface AppHistoryAnchor {
  type: 'gitops' | 'helm' | string
  title: string
  status?: string
  revision?: string
  message?: string
  source?: string
  initiatedBy?: string
  timestamp?: string
}

export interface AppHistoryIncident {
  severity: 'warning' | 'error' | 'info' | string
  title: string
  object: string
  message?: string
  count?: number
  firstSeen?: string
  lastSeen?: string
}

export interface AppHistory {
  appKey: string
  sourceRef?: AppSourceRef
  summary?: AppHistorySummary
  anchors?: AppHistoryAnchor[]
  incidents?: AppHistoryIncident[]
  partialSources?: string[]
}

export interface AppIdentity {
  /** Shared display identity (the name stem). Derived classification — never
   *  an address; instance keys remain the only URLs. */
  key: string
  /** This instance's canonical env token (dev | staging | prod | …). */
  env: string
  /** high (declared source path) | medium (name stem + shared image repo). */
  confidence: string
  /** Human-readable why, for the app group chip tooltip. */
  evidence: string
  /** True when the key is backed by declared upstream identity and can group across clusters. */
  portable?: boolean
  /** Machine-readable provenance tier (radar applications_identity.go):
   *  explicit | argo-path | argo-appset | flux-source (declared origins, portable)
   *  · label | name-stem | namespace (NAMEs, per-cluster). */
  source?: string
  /** Declared source-path stem (argo-path / flux-source only). The display `key`
   *  is the name stem; this disambiguates same-name/different-path apps in the
   *  portable cross-cluster fold. */
  pathKey?: string
}

/** The user-set annotation that forces cross-cluster grouping — the canonical
 *  answer to "how do I make this fold?" for non-GitOps apps. */
export const APP_IDENTITY_ANNOTATION = 'app.skyhook.io/app'

/** Whether a Source is a declared cross-cluster origin (mirrors the hub's
 *  isDeclaredPortableSource). NAMEs collide across clusters and stay per-cluster. */
export function isDeclaredAppSource(source?: string): boolean {
  return source === 'explicit' || source === 'argo-path' || source === 'argo-appset' || source === 'flux-source'
}

/** A short label for how an app's identity was determined, by Source — the
 *  plain-language "grouped by …" phrase shown in the identity tooltip. */
export function appSourceLabel(source?: string): string {
  switch (source) {
    case 'explicit':
      return `the ${APP_IDENTITY_ANNOTATION} annotation`
    case 'argo-path':
      return 'its Argo CD source path'
    case 'argo-appset':
      return 'its ApplicationSet (env fan-out)'
    case 'flux-source':
      return 'its Flux source'
    case 'addon':
      return 'a shared add-on name + chart/image'
    case 'label':
      return 'the app.kubernetes.io/name label'
    case 'name-stem':
      return 'a shared name + image'
    case 'namespace':
      return 'a shared namespace + image'
    default:
      return 'its workload grouping'
  }
}

/** The explainer for "why isn't this grouped across clusters, and how do I fix
 *  it?" — drives the per-cluster row hint. `folds` is true when the identity is a
 *  declared origin (folds cross-cluster); otherwise `fix` says what to add. */
export function appGroupingExplainer(identity?: AppIdentity): { folds: boolean; how: string; fix?: string } {
  const folds = isDeclaredAppSource(identity?.source)
  return {
    folds,
    how: `Grouped by ${appSourceLabel(identity?.source)}.`,
    fix: folds
      ? undefined
      : `This app only has a per-cluster name, which can't prove two clusters run the same app. To fold it across clusters, set ${APP_IDENTITY_ANNOTATION} on its workloads in each cluster (same value), or deploy it via Argo CD / Flux with the environment in the source path.`,
  }
}

export interface AppRow {
  key: string
  name: string
  /** The single namespace the app's WORKLOADS run in; absent/empty when they
   *  span several (use `namespaces`). Residence, not the GitOps manager's home. */
  namespace?: string
  /** All distinct workload namespaces, sorted. */
  namespaces?: string[]
  /** App identity grouping evidence — instances of one logical app across
   *  environments share identity.key. See applications_identity.go. */
  identity?: AppIdentity
  /** pkg/subject overlay tier (0 = raw, no signal); 1-9. */
  tier?: number
  /** high | medium | low */
  confidence?: string
  /** app | addon | mixed — classification hint, never identity. */
  category?: string
  addonReason?: string
  workload_class?: AppWorkloadClass
  /** worst-of across workloads: healthy | degraded | unhealthy | unknown. */
  health: string
  /** distinct image tags. */
  versions?: string[]
  /** True when the SAME image runs different tags across workloads — real
   *  drift. Multiple components on different images is normal, not skew. */
  versionSkew?: boolean
  /** Single upstream version (app.kubernetes.io/version) when all workloads
   *  agree — the app's "main version". Empty for multi-chart umbrellas. */
  appVersion?: string
  /** Exact source-system object when the grouping signal names one. Label/name
   *  inferred apps intentionally omit this instead of guessing. */
  sourceRef?: AppSourceRef
  workloads: AppWorkload[]
  events?: AppEvent[]
  relationships?: AppRelationships
}

// -----------------------------------------------------------------------------
// Environment ladder. Higher rank = more-promoted; prod is the top. Unranked
// envs sort trailing.
// -----------------------------------------------------------------------------

export const ENV_RANK: Record<string, number> = { dev: 0, staging: 1, prod: 2 }

/** Rank for an environment label, or null when it isn't on the ladder. */
export function envRank(env: string | undefined): number | null {
  if (!env) return null
  const r = ENV_RANK[env.toLowerCase()]
  return r === undefined ? null : r
}

// Namespace-name token → canonical env. Matched on the whole name first, then
// on `-`/`_`-delimited segments (so `myapp-prod`, `staging-svc` resolve), which
// avoids substring false-hits like `prod` inside `product`.
// Only the universal trio is hardcoded (same trio-only policy as
// applications_identity.go; this synonym list is slightly more permissive) —
// every other env token is DISCOVERED by the server's app identity resolver and
// arrives on the wire as identity.env; callers pass those through extraTokens,
// so a "loadtest" namespace labels once the cluster itself proves the token is
// an env. Zero local vocabulary beyond the trio.
const ENV_NS_TOKENS: Record<string, string> = {
  dev: 'dev', devel: 'dev', develop: 'dev', development: 'dev',
  stg: 'staging', stage: 'staging', staging: 'staging',
  prd: 'prod', prod: 'prod', production: 'prod', live: 'prod',
}

/** Infer a canonical environment from a namespace name, or null when nothing
 *  recognizable is present (conservative — `kube-system`, `billing` → null). */
export function envFromNamespace(namespace: string | undefined, extraTokens?: ReadonlySet<string>): string | null {
  if (!namespace) return null
  const lower = namespace.toLowerCase()
  const hit = (tok: string): string | null => ENV_NS_TOKENS[tok] ?? (extraTokens?.has(tok) ? tok : null)
  const whole = hit(lower)
  if (whole) return whole
  for (const seg of lower.split(/[-_]/).filter(Boolean)) {
    const h = hit(seg)
    if (h) return h
  }
  return null
}

export interface ResolvedAppEnv {
  /** Canonical env token (lowercased), or '' when unlabeled. */
  env: string
  /** True when derived from the namespace heuristic (not an explicit label). */
  inferred: boolean
}

/** Resolve an environment via the precedence cascade: an explicit env wins;
 *  otherwise the namespace heuristic (tagged inferred); otherwise unlabeled. */
export function resolveEnv(explicitEnv: string | undefined, namespace: string | undefined, extraTokens?: ReadonlySet<string>): ResolvedAppEnv {
  const explicit = (explicitEnv || '').trim()
  if (explicit) return { env: explicit.toLowerCase(), inferred: false }
  const inferred = envFromNamespace(namespace, extraTokens)
  if (inferred) return { env: inferred, inferred: true }
  return { env: '', inferred: false }
}

export function identityEnvInferred(identity: AppIdentity | undefined): boolean {
	if (!identity) return false
	return identity.evidence.startsWith('namespace stem ')
}

// -----------------------------------------------------------------------------
// System namespaces — cluster plumbing hidden by default on the app surface.
// -----------------------------------------------------------------------------

const SYSTEM_NAMESPACES = new Set(['kube-system', 'kube-public', 'kube-node-lease', 'kube-flannel', 'local-path-storage'])

/** True for cluster-plumbing namespaces (kube-*, *-system operators) the app
 *  list hides by default. The `-system` suffix catches operator namespaces like
 *  `cert-manager`'s `gatekeeper-system`, `kourier-system`, etc.; `gke-managed-`
 *  is Google's documented prefix for GKE-managed component namespaces. */
export function isSystemNamespace(ns: string | undefined): boolean {
  if (!ns) return false
  const lower = ns.toLowerCase()
  return SYSTEM_NAMESPACES.has(lower) || lower.endsWith('-system') || lower.startsWith('gke-managed-')
}

// -----------------------------------------------------------------------------
// Category — the app/add-on/mixed classification hint (never identity).
// -----------------------------------------------------------------------------

export type AppCategory = 'app' | 'addon' | 'mixed'

export const CATEGORY_ORDER: AppCategory[] = ['app', 'addon', 'mixed']

export const CATEGORY_META: Record<AppCategory, { label: string; tooltip: string }> = {
  app: { label: 'App', tooltip: 'Software you deploy and run — services, workers, jobs.' },
  addon: { label: 'Add-on', tooltip: 'Platform machinery (controllers, operators, system charts), classified by chart/label evidence. Shown for completeness.' },
  mixed: { label: 'Mixed', tooltip: 'Has both app and add-on evidence. Kept visible — classification is informational, not identity.' },
}

/** The category bucket for a row — apps with no category default to 'app'. */
export function categoryOf(category: string | undefined): AppCategory {
  return category === 'addon' || category === 'mixed' ? category : 'app'
}

// -----------------------------------------------------------------------------
// Version comparison. Conservative semver-ish: compares only clean numeric
// versions (optional leading `v`). Anything non-numeric — a range, a branch, a
// git SHA — returns null so callers render "no lag" rather than guessing.
// -----------------------------------------------------------------------------

function parseVersion(v: string | undefined): number[] | null {
  if (!v) return null
  const t = v.trim().replace(/^v/i, '')
  if (!/^\d+(\.\d+)*$/.test(t)) return null
  return t.split('.').map((n) => parseInt(n, 10))
}

/** Date-stamped CI tags ("main_2026-03-26_05", "billing_main_2026-05-18_00"):
 *  same prefix + extractable date (+ optional sequence) gives a total order.
 *  Different prefixes or no date → not comparable; never guess. */
const DATE_TAG = /^(.*?)[-_](\d{4})[-_.](\d{2})[-_.](\d{2})(?:[-_.](\d+))?$/

function parseDateTag(v: string): { prefix: string; ord: number } | null {
  const m = DATE_TAG.exec(v)
  if (!m) return null
  const [, prefix, y, mo, d, seq] = m
  const ord = Number(y) * 1e8 + Number(mo) * 1e6 + Number(d) * 1e4 + Number(seq ?? 0)
  return { prefix, ord }
}

/** -1 if a<b, 1 if a>b, 0 if equal, null if either isn't a comparable version. */
export function compareVersions(a: string | undefined, b: string | undefined): number | null {
  // Date-stamped pipeline tags first — semver parsing would misread them.
  if (a && b) {
    const da = parseDateTag(a)
    const db = parseDateTag(b)
    if (da && db) {
      if (da.prefix !== db.prefix) return null
      return da.ord === db.ord ? 0 : da.ord < db.ord ? -1 : 1
    }
    if (da || db) return null
  }
  const pa = parseVersion(a)
  const pb = parseVersion(b)
  if (!pa || !pb) return null
  const len = Math.max(pa.length, pb.length)
  for (let i = 0; i < len; i++) {
    const x = pa[i] ?? 0
    const y = pb[i] ?? 0
    if (x !== y) return x < y ? -1 : 1
  }
  return 0
}

// -----------------------------------------------------------------------------
// Provenance — overlay tier → everything tier-derived, mirroring pkg/subject's
// Tier constants (1-9). ONE table: the badge label, the Source facet bucket,
// and the tooltip phrase all read from TIER_META, so a new tier added in
// pkg/subject has exactly one place to land here.
// -----------------------------------------------------------------------------

/** Coarse provenance bucket for the Source facet. Stable ids — display labels
 *  live in SOURCE_META (the house meta-map pattern), so they can be re-worded
 *  without breaking facet state or future URL serialization. */
export type AppSource = 'argocd' | 'flux' | 'helm' | 'label' | 'ungrouped'

export const SOURCE_ORDER: AppSource[] = ['argocd', 'flux', 'helm', 'label', 'ungrouped']

export const SOURCE_META: Record<AppSource, { label: string }> = {
  argocd: { label: 'Argo CD' },
  flux: { label: 'Flux' },
  helm: { label: 'Helm' },
  label: { label: 'Label' },
  ungrouped: { label: 'Ungrouped' },
}

interface TierMeta {
  source: AppSource
  /** Tooltip phrase pieces: "Grouped by {lead} `{code(name)}` {trail}". */
  lead: string
  code: (name: string) => string
  trail?: string
}

const TIER_META: Record<number, TierMeta> = {
  1: { source: 'flux', lead: 'its Flux HelmRelease', code: (n) => n },
  2: { source: 'flux', lead: 'its Flux Kustomization', code: (n) => n },
  3: { source: 'argocd', lead: 'its Argo CD Application', code: (n) => n },
  4: { source: 'argocd', lead: 'its Argo CD Application', code: (n) => n },
  5: { source: 'helm', lead: 'its Helm release', code: (n) => n },
  6: { source: 'label', lead: 'the', code: () => 'app.kubernetes.io/instance', trail: 'label' },
  7: { source: 'label', lead: 'the', code: () => 'app.kubernetes.io/part-of', trail: 'label' },
  8: { source: 'label', lead: 'the', code: () => 'app.kubernetes.io/name', trail: 'label' },
  9: { source: 'label', lead: 'the', code: () => 'app', trail: 'label' },
}

export function sourceOf(tier: number | undefined): AppSource {
  if (!tier) return 'ungrouped'
  return TIER_META[tier]?.source ?? 'label'
}

/** Short badge label for an app's provenance tier (which tool/source grouped it). */
export function overlayProvenance(tier: number | undefined): string {
  return SOURCE_META[sourceOf(tier)].label
}

function appNameFromKey(key: string): string {
  const slash = key.lastIndexOf('/')
  return slash >= 0 && slash < key.length - 1 ? key.slice(slash + 1) : key
}

// How an app was grouped, decomposed so the tooltip can render the source
// resource / label key in an inline-code chip rather than a run-on sentence.
// `lead` + `code` + `trail` reads as a phrase: "its Flux HelmRelease `argocd`"
// or "the `app.kubernetes.io/part-of` label". `code` empty → no chip.
export interface ProvenanceSource {
  lead: string
  code: string
  trail?: string
}

export function provenanceSource(tier: number | undefined, key: string): ProvenanceSource {
  const meta = tier ? TIER_META[tier] : undefined
  if (!meta) return { lead: 'cluster-native evidence', code: '' }
  return { lead: meta.lead, code: meta.code(appNameFromKey(key)), trail: meta.trail }
}


/** The distinct namespaces an app's workloads run in, sorted. Prefers the
 *  server's `namespaces` field, deriving from workloads for older payloads. */
export function namespacesOf(app: AppRow): string[] {
  if (app.namespaces && app.namespaces.length > 0) return app.namespaces
  const nss = Array.from(new Set((app.workloads || []).map((w) => w.namespace).filter(Boolean))).sort()
  if (nss.length > 0) return nss
  return app.namespace ? [app.namespace] : []
}

/** An app's single namespace, or '' when it spans several — callers must not
 *  pick an arbitrary one (env inference and the system-namespace filter both
 *  key off this; a wrong pick misleads). Use namespacesOf for the full list. */
export function namespaceOf(app: AppRow): string {
  const nss = namespacesOf(app)
  return nss.length === 1 ? nss[0] : ''
}

// -----------------------------------------------------------------------------
// App identity groups — client helpers over the wire `identity` block. Folding
// rows into groups is presentation; these helpers keep the semantics (ladder
// order, lag, affix stripping) in one place for the list, the detail band,
// and later the hub.
// -----------------------------------------------------------------------------

/** Client-only token set for matching "the same workload" across an app group's
 *  env instances during an env switch. Deliberately broader than what the
 *  server discovers — a miss only means the switch lands on the instance
 *  overview. Callers extend it with the group's own (discovered) env tokens
 *  via the extraTokens parameter. */
const NAME_ENV_TOKENS = new Set([
  'dev', 'development', 'staging', 'stage', 'stg', 'prod', 'production', 'prd',
  'qa', 'uat', 'preprod', 'preview', 'canary',
])

/** Strip a recognized env affix from a workload/app name —
 *  "billing-staging" → "billing", "qa-koala-backend" → "koala-backend".
 *  Used to match "the same workload" across app group env instances. */
export function stripEnvAffix(name: string, extraTokens?: ReadonlySet<string>): string {
  const isEnv = (tok: string) => NAME_ENV_TOKENS.has(tok) || (extraTokens?.has(tok) ?? false)
  const i = name.lastIndexOf('-')
  if (i > 0 && isEnv(name.slice(i + 1).toLowerCase())) return name.slice(0, i)
  const j = name.indexOf('-')
  if (j > 0 && isEnv(name.slice(0, j).toLowerCase())) return name.slice(j + 1)
  return name
}

/** Find "the same workload" in a sibling env instance: exact kind+name first,
 *  then the env-affix-stripped stem (billing-staging ↔ billing). extraTokens
 *  should carry the app group's env tokens so discovered envs (loadtest, …)
 *  strip too. Null = no counterpart — the switch shows the instance overview. */
export function matchWorkloadAcrossInstances(
  workloadKey: string,
  targetWorkloads: Pick<AppWorkload, 'kind' | 'namespace' | 'name'>[] | undefined,
  extraTokens?: ReadonlySet<string>,
): Pick<AppWorkload, 'kind' | 'namespace' | 'name'> | null {
  const [kind, namespace, name] = workloadKey.split('/')
  if (!kind || !name) return null
  const ws = targetWorkloads ?? []
  return (
    ws.find((w) => w.kind === kind && w.namespace === namespace && w.name === name) ??
    uniqueMatch(ws, (w) => w.kind === kind && w.name === name) ??
    uniqueMatch(ws, (w) => w.kind === kind && stripEnvAffix(w.name, extraTokens) === stripEnvAffix(name, extraTokens)) ??
    null
  )
}

function uniqueMatch<T>(items: T[], pred: (item: T) => boolean): T | null {
  let found: T | null = null
  for (const item of items) {
    if (!pred(item)) continue
    if (found) return null
    found = item
  }
  return found
}

/** Ladder order: ranked envs by rank (dev → staging → prod), then
 *  recognized-but-unranked alphabetically (qa, …). */
export function orderEnvs(envs: string[]): string[] {
  return [...envs].sort((a, b) => {
    const ra = envRank(a)
    const rb = envRank(b)
    if (ra !== null && rb !== null) return ra - rb
    if (ra !== null) return -1
    if (rb !== null) return 1
    return a.localeCompare(b)
  })
}

/** Promotion lag across an app group's env cells: fires only between RANKED envs,
 *  when a strictly-lower env runs a strictly-newer comparable version.
 *  Returns the human message ("staging is behind dev") or null. */
export function appGroupLagMessage(cells: { env: string; version?: string }[]): string | null {
  const ranked = cells
    .map((c) => ({ ...c, rank: envRank(c.env) }))
    .filter((c): c is { env: string; version?: string; rank: number } => c.rank !== null && !!c.version)
    .sort((a, b) => a.rank - b.rank)
  for (let i = 0; i < ranked.length; i++) {
    for (let j = i + 1; j < ranked.length; j++) {
      // Strict rank inequality: two instances of the SAME env are siblings,
      // not a promotion pair — without this, "prod is behind prod" can fire.
      if (ranked[i].rank < ranked[j].rank && compareVersions(ranked[i].version, ranked[j].version) === 1) {
        return `${ranked[j].env} is behind ${ranked[i].env}`
      }
    }
  }
  return null
}

// -----------------------------------------------------------------------------
// App group folding — turns a filtered+sorted entry list into the rows the list
// renders: one ladder row per app group (emitted at its first member's position,
// so the active sort still governs placement) with instances nested under it.
// Pure so the collapse experiment's safety rails (search auto-expansion,
// orphans rendering flat, per-env aggregation) stay pinned by tests.
// -----------------------------------------------------------------------------

/** The slice of a list entry the fold needs. */
export interface AppGroupFoldEntry {
  row: { key: string; name: string; identity?: AppIdentity; appVersion?: string }
  health: AppHealth
  versions: string[]
  ready: number
  desired: number
  kinds: Record<string, number>
  classComposition: { cls: AppWorkloadClass; count: number }[]
}

export interface AppGroupEnvCell {
  env: string
  health: AppHealth
  version?: string
  count: number
  firstKey: string
}

export interface FoldedAppGroupRow<T extends AppGroupFoldEntry> {
  kind: 'group'
  key: string
  label: string
  members: T[]
  expanded: boolean
  cells: AppGroupEnvCell[]
  lag: string | null
  health: AppHealth
  ready: number
  desired: number
  kinds: Record<string, number>
  classComposition: { cls: AppWorkloadClass; count: number }[]
  workloadClass: AppWorkloadClass
  confidence: string
}

export type FoldedRow<T extends AppGroupFoldEntry> = FoldedAppGroupRow<T> | { kind: 'instance'; entry: T; child?: boolean }

export interface FoldAppGroupsOptions<T extends AppGroupFoldEntry> {
  /** Scope for non-portable identities. OSS leaves this empty; fleet callers
   *  should include the cluster id so local name/repo evidence cannot merge
   *  unrelated clusters. Portable identities ignore the scope. */
  localScope?: (entry: T) => string | undefined
  /** Per-member env slices for the env ladder. A fleet member spans several
   *  per-cluster envs, so the host supplies them (cluster-coverage derived,
   *  authoritative) rather than the single `identity.env` — which the hub can
   *  stale when it joins the same overlay key across clusters. Defaults to the
   *  member's single identity env. */
  envsOf?: (entry: T) => Array<{ env: string; health: AppHealth }>
}

export function foldAppGroups<T extends AppGroupFoldEntry>(
  entries: T[],
  expandedKeys: ReadonlySet<string>,
  autoExpand: boolean,
  options: FoldAppGroupsOptions<T> = {},
): FoldedRow<T>[] {
  const newest = (e: T): string | undefined =>
    e.versions.reduce<string | undefined>((best, v) => (!best || compareVersions(v, best) === 1 ? v : best), undefined) ?? e.row.appVersion
  const groupKey = (e: T): string | null => {
    const id = e.row.identity
    if (!id) return null
    const scope = options.localScope?.(e)
    if (!scope) return id.key
    // A declared-PATH identity displays its name stem as the key but carries the
    // path stem in pathKey; two different apps can share a name while declaring
    // different paths, so disambiguate the portable cross-cluster fold by pathKey.
    return id.portable ? `portable:${id.key}${id.pathKey ? `:${id.pathKey}` : ''}` : `local:${scope}:${id.key}`
  }
  const byGroup = new Map<string, T[]>()
  for (const e of entries) {
    const k = groupKey(e)
    if (k) byGroup.set(k, [...(byGroup.get(k) ?? []), e])
  }
  const emitted = new Set<string>()
  const out: FoldedRow<T>[] = []
  for (const e of entries) {
    const id = e.row.identity
    const k = groupKey(e)
    // A group needs ≥2 SURVIVING members — filters can orphan one, which
    // then renders as the plain instance it is.
    if (!id || !k || (byGroup.get(k)?.length ?? 0) < 2) {
      out.push({ kind: 'instance', entry: e })
      continue
    }
    if (emitted.has(k)) continue
    emitted.add(k)
    const members = byGroup.get(k)!

    const cellMap = new Map<string, AppGroupEnvCell>()
    const kinds: Record<string, number> = {}
    const compMap = new Map<AppWorkloadClass, number>()
    let ready = 0
    let desired = 0
    // Seed with the most-benign tier (rank 0) so the max-fold below has a valid
    // identity now that `unknown` ranks above healthy — see worstHealth.
    let health: AppHealth = 'neutral'
    for (const m of members) {
      const v = newest(m)
      // A fleet member spans several per-cluster envs; the host supplies them so
      // the ladder reflects every env, not just the member's (possibly stale)
      // single identity env. Default: the one identity env.
      const slices = options.envsOf?.(m) ?? [{ env: m.row.identity!.env, health: m.health }]
      for (const slice of slices) {
        const cur = cellMap.get(slice.env)
        if (!cur) {
          cellMap.set(slice.env, { env: slice.env, health: slice.health, version: v, count: 1, firstKey: m.row.key })
        } else {
          cur.count++
          if ((HEALTH_RANK[slice.health] ?? 0) > (HEALTH_RANK[cur.health] ?? 0)) cur.health = slice.health
          if (v && (!cur.version || compareVersions(v, cur.version) === 1)) cur.version = v
        }
      }
      if ((HEALTH_RANK[m.health] ?? 0) > (HEALTH_RANK[health] ?? 0)) health = m.health
      ready += m.ready
      desired += m.desired
      for (const [k, n] of Object.entries(m.kinds)) kinds[k] = (kinds[k] ?? 0) + n
      for (const c of m.classComposition) compMap.set(c.cls, (compMap.get(c.cls) ?? 0) + c.count)
    }
    const cells = orderEnvs([...cellMap.keys()]).map((env) => cellMap.get(env)!)
    const classComposition = CLASS_ORDER.filter((c) => compMap.has(c)).map((c) => ({ cls: c, count: compMap.get(c)! }))
    const known = classComposition.map((c) => c.cls).filter((c) => c !== 'unknown')
    const workloadClass: AppWorkloadClass =
      known.length === 0 ? 'unknown'
      : known.includes('service') && !known.includes('job') ? 'service'
      : known.length === 1 ? known[0]
      : 'mixed'
    const expanded = autoExpand || expandedKeys.has(k)
    out.push({
      kind: 'group',
      key: k,
      label: id.key,
      members,
      expanded,
      cells,
      lag: appGroupLagMessage(cells),
      health,
      ready,
      desired,
      kinds,
      classComposition,
      workloadClass,
      confidence: members.some((m) => m.row.identity!.confidence === 'high') ? 'high' : 'medium',
    })
    if (expanded) {
      for (const m of members) out.push({ kind: 'instance', entry: m, child: true })
    }
  }
  return out
}

/** Normalize a wire health string to the AppHealth union (the health twin of
 *  workloadClassOf — keeps `as AppHealth` casts out of components). */
export function healthOf(value: string | undefined): AppHealth {
  return value === 'unhealthy' || value === 'degraded' || value === 'healthy' || value === 'neutral'
    ? value
    : 'unknown'
}

// -----------------------------------------------------------------------------
// Health + class meta. Health uses theme tokens for the unknown/neutral end and
// pale-pastel pills (which have no theme token) for the colored tiers.
// -----------------------------------------------------------------------------

// Worst-first display + aggregation order. Mirrors the backend's
// `pkg/health` Rank ordering (unhealthy > degraded > unknown > healthy ≈ neutral)
// so the app rollup agrees with every other surface. `unknown` ranks ABOVE
// healthy (a node-lost workload is worse than a running one); `neutral`
// (intentional/idle) is the most-benign tier, so an all-idle app rolls up to
// "Idle" while a mixed healthy+idle app still reads Healthy (healthy out-ranks
// neutral in the max). NOTE: the previous map inverted this — it ranked
// `unknown: 0` below `healthy`, disagreeing with the backend rollup.
export const HEALTH_ORDER: AppHealth[] = ['unhealthy', 'degraded', 'unknown', 'healthy', 'neutral']
export const HEALTH_RANK: Record<string, number> = { unhealthy: 4, degraded: 3, unknown: 2, healthy: 1, neutral: 0 }

export interface HealthMeta {
  label: string
  bar: string
  text: string
  pill: string
}

// ─── Chip dialect ────────────────────────────────────────────────────────────
// The Applications surface renders dense metadata as pale pastel chips —
// deliberately lighter than <Badge>'s severity palette, which is sized for
// standalone status pills. A local dialect, but defined ONCE here: call sites
// compose `CHIP` (chrome) + a `CHIP_TONE` (color), never inline the strings.
// Literal class strings are required for Tailwind's content scanner.
export const CHIP = 'inline-flex items-center rounded-sm px-1.5 py-px text-[10px] font-medium ring-1 ring-inset'
export const CHIP_TONE = {
  rose: 'bg-rose-50 text-rose-700 ring-rose-200 dark:bg-rose-950/40 dark:text-rose-300 dark:ring-rose-900',
  amber: 'bg-amber-50 text-amber-700 ring-amber-200 dark:bg-amber-950/40 dark:text-amber-300 dark:ring-amber-900',
  emerald: 'bg-emerald-50 text-emerald-700 ring-emerald-200 dark:bg-emerald-950/40 dark:text-emerald-300 dark:ring-emerald-900',
  blue: 'bg-blue-50 text-blue-700 ring-blue-200 dark:bg-blue-950/40 dark:text-blue-300 dark:ring-blue-900',
  sky: 'bg-sky-50 text-sky-700 ring-sky-200 dark:bg-sky-950/40 dark:text-sky-300 dark:ring-sky-900',
  violet: 'bg-violet-50 text-violet-700 ring-violet-200 dark:bg-violet-950/40 dark:text-violet-300 dark:ring-violet-900',
  neutral: 'bg-theme-hover text-theme-text-secondary ring-theme-border',
  muted: 'bg-theme-hover text-theme-text-tertiary ring-theme-border',
} as const

export const HEALTH_META: Record<AppHealth, HealthMeta> = {
  unhealthy: { label: 'Down', bar: 'bg-rose-500', text: 'text-rose-600 dark:text-rose-400', pill: CHIP_TONE.rose },
  degraded: { label: 'Degraded', bar: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400', pill: CHIP_TONE.amber },
  healthy: { label: 'Healthy', bar: 'bg-emerald-500', text: 'text-emerald-600 dark:text-emerald-400', pill: CHIP_TONE.emerald },
  // neutral = intentionally idle/off (every workload suspended or scaled to 0).
  // Sky, calm — not "Healthy" (it isn't serving) and not a problem to act on.
  neutral: { label: 'Idle', bar: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400', pill: CHIP_TONE.sky },
  unknown: { label: 'Unknown', bar: 'bg-slate-400', text: 'text-theme-text-tertiary', pill: CHIP_TONE.muted },
}

export const CLASS_ORDER: AppWorkloadClass[] = ['service', 'worker', 'job', 'unknown']

export const CLASS_META: Record<AppWorkloadClass, { label: string; pill: string; tooltip: string }> = {
  service: { label: 'Service', pill: CHIP_TONE.blue, tooltip: 'Long-running, request-serving (a Deployment/StatefulSet behind a Service/Ingress/route). Inferred from the workload shape + routing.' },
  worker: { label: 'Worker', pill: CHIP_TONE.violet, tooltip: 'Long-running background processor (no serving edge). Inferred from the workload shape.' },
  job: { label: 'Job', pill: CHIP_TONE.amber, tooltip: 'Finite or scheduled work (Job/CronJob).' },
  mixed: { label: 'Mixed', pill: CHIP_TONE.neutral, tooltip: 'Contains workloads of more than one class (e.g. a service plus its scheduled jobs).' },
  unknown: { label: 'Unknown', pill: CHIP_TONE.muted, tooltip: "Couldn't infer a runtime class from the workload." },
}

/** Per-class workload counts for an app, in CLASS_ORDER — the composition
 *  behind a "Mixed" badge and the inclusive Class facet (filtering "Service"
 *  matches mixed apps that contain a service). */
export function classCompositionOf(app: AppRow): { cls: AppWorkloadClass; count: number }[] {
  const counts = new Map<AppWorkloadClass, number>()
  for (const w of app.workloads || []) {
    const c = workloadClassOf(w.workload_class)
    counts.set(c, (counts.get(c) ?? 0) + 1)
  }
  return CLASS_ORDER.filter((c) => counts.has(c)).map((c) => ({ cls: c, count: counts.get(c)! }))
}

/** The distinct KNOWN classes an app contains — the facet-matching set. Falls
 *  back to the app-level class when there are no classifiable workloads. */
export function classSetOf(app: AppRow): AppWorkloadClass[] {
  const known = classCompositionOf(app)
    .map((c) => c.cls)
    .filter((c) => c === 'service' || c === 'worker' || c === 'job')
  if (known.length > 0) return known
  return [workloadClassOf(app.workload_class)]
}

export function workloadClassOf(value?: AppWorkloadClass): AppWorkloadClass {
  switch (value) {
    case 'service':
    case 'worker':
    case 'job':
    case 'mixed':
      return value
    default:
      return 'unknown'
  }
}

/** Worst health across a set of raw health strings. Seeds with `neutral` — the
 *  most-benign tier (rank 0) — so it acts as the max-fold identity: any real
 *  value out-ranks it, an all-neutral set stays neutral, and healthy+neutral
 *  resolves to healthy. (Seeding with `unknown` would be wrong now that `unknown`
 *  ranks ABOVE healthy — an all-healthy set would never beat it and return
 *  `unknown`. Mirrors pkg/health.WorseOf.) */
export function worstHealth(hs: string[]): AppHealth {
  let w: AppHealth = 'neutral'
  for (const h of hs) if ((HEALTH_RANK[h] ?? 0) > (HEALTH_RANK[w] ?? 0)) w = h as AppHealth
  return w
}

export function newestTag(versions: string[]): string | undefined {
  let best: string | undefined
  for (const v of versions) {
    const t = v?.trim()
    if (!t) continue
    if (best === undefined) best = t
    else if (compareVersions(t, best) === 1) best = t
  }
  return best
}

// -----------------------------------------------------------------------------
// Applications list entry model — the per-row shape the shared list core renders.
// Discriminated on `variant` so the OSS single-cluster row and the Cloud fleet
// row (one row spanning several clusters) carry their own fields with no loose
// optional-superset overlap. The base carries everything the facet rail, fold,
// counts, and sort read; the variant arms carry only what their instance-row
// renderer needs. Both arms satisfy AppGroupFoldEntry, so foldAppGroups is shared.
// -----------------------------------------------------------------------------

export interface AppEntryBase {
  row: AppRow
  health: AppHealth
  versions: string[]
  kinds: Record<string, number>
  workloadClass: AppWorkloadClass
  /** Distinct contained classes — the inclusive facet-matching set. */
  classSet: AppWorkloadClass[]
  classComposition: { cls: AppWorkloadClass; count: number }[]
  category: AppCategory
  ready: number
  desired: number
  /** ready/desired as a fraction for sorting; -1 when nothing is desired. */
  readyRatio: number
  source: AppSource
}

export interface SingleAppEntry extends AppEntryBase {
  variant: 'single'
  namespace: string
  namespaces: string[]
  env: string
  envInferred: boolean
}

/** One environment instance within a fleet row (env name + worst health across
 *  its clusters + whether the env was inferred from the namespace). */
export interface EnvSlice {
  env: string
  health: AppHealth
  inferred: boolean
}

/** A cluster this fleet row's app runs in. */
export interface AppClusterRef {
  id: string
  name: string
  health: AppHealth
}

export interface FleetAppEntry extends AppEntryBase {
  variant: 'fleet'
  envs: EnvSlice[]
  clusters: AppClusterRef[]
  /** True when the app runs different versions across its clusters/envs. */
  versionSkew: boolean
}

export type AppEntry = SingleAppEntry | FleetAppEntry

/** Build a single-cluster list entry from a wire row. The env is resolved via
 *  the server's identity classification (authoritative) with a namespace
 *  heuristic fallback for plain rows. */
export function buildSingleAppEntry(row: AppRow, discoveredEnvs?: ReadonlySet<string>): SingleAppEntry {
  const kinds: Record<string, number> = {}
  let ready = 0
  let desired = 0
  for (const wl of row.workloads || []) {
    kinds[wl.kind] = (kinds[wl.kind] ?? 0) + 1
    ready += wl.ready ?? 0
    desired += wl.desired ?? 0
  }
  const namespace = namespaceOf(row)
  // The server's identity classification carries the authoritative env (label/
  // declared/discovered); plain rows fall back to the trio + discovered-token
  // namespace heuristic.
  const resolved = resolveEnv(undefined, namespace, discoveredEnvs)
  const env = row.identity?.env ?? resolved.env
  const inferred = row.identity ? identityEnvInferred(row.identity) : resolved.inferred
  return {
    variant: 'single',
    row,
    health: healthOf(row.health),
    versions: Array.from(new Set((row.versions || []).filter(Boolean))),
    namespace,
    namespaces: namespacesOf(row),
    env,
    envInferred: inferred,
    kinds,
    workloadClass: workloadClassOf(row.workload_class),
    classSet: classSetOf(row),
    classComposition: classCompositionOf(row),
    category: categoryOf(row.category),
    ready,
    desired,
    readyRatio: desired > 0 ? ready / desired : -1,
    source: sourceOf(row.tier),
  }
}

/** The text an entry matches free-text search against — name, key, namespace,
 *  source/class/category labels, versions, env, workload kinds + identity. Pure
 *  and exported so the list core and tests share one definition. */
export function searchTextForEntry(e: AppEntry): string {
  const workloadText = (e.row.workloads || []).flatMap((wl) => [wl.kind, wl.namespace, wl.name, wl.version])
  const envParts = e.variant === 'single' ? [e.env || 'unlabeled'] : e.envs.map((s) => s.env || 'unlabeled')
  const nsParts = e.variant === 'single' ? [e.namespace] : e.clusters.map((c) => c.name)
  return [
    e.row.name,
    e.row.key,
    ...nsParts,
    SOURCE_META[e.source].label,
    CLASS_META[e.workloadClass].label,
    ...e.classSet.map((c) => CLASS_META[c].label),
    CATEGORY_META[e.category].label,
    ...e.versions,
    ...envParts,
    ...Object.keys(e.kinds),
    ...workloadText,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}
