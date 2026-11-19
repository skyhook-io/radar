// Per-resource policy findings, from GET /api/policy/resource/{kind}/{ns}/{name}.
//
// The coverage fields are not decoration. An empty `findings` array means one of
// several very different things — nothing violated, no engine installed, the
// caller may not read the reports, the index is still warming — and a section
// that renders all of them as blank space tells an operator they are compliant
// in the cases where we simply do not know.

export type PolicyResult = 'pass' | 'fail' | 'warn' | 'error' | 'skip' | string

export interface PolicyResourceFinding {
  policy: string
  rule?: string
  result: PolicyResult
  severity?: string
  category?: string
  message?: string
  engine?: string
}

export interface PolicyResourceCounts {
  pass: number
  fail: number
  warn: number
  error: number
  skip: number
}

/** One resource a policy recorded an outcome for, from
 *  GET /api/policy/policies/{policy}. */
export interface PolicyCoverageSubject {
  group?: string
  kind: string
  namespace?: string
  name: string
  result: PolicyResult
  message?: string
  /** Matched through a rule Kyverno synthesised from an authored Pod rule,
   *  not through a rule the user wrote. */
  autogen?: boolean
}

export interface PolicyCoverageRule {
  /** Empty for the modern CEL families, which have no named rules. */
  rule: string
  counts: PolicyResourceCounts
  subjects: PolicyCoverageSubject[]
  total: number
  truncated?: boolean
  /** How many of `total` the namespace view filter held back. `counts` still
   *  describe every outcome, so the two must be read together. */
  hiddenByFilter?: number
  /** The non-passing part of `hiddenByFilter`. Only non-passing subjects are
   *  listed, so this is what separates a row the server cap dropped from one
   *  the view filter held back. */
  hiddenNotable?: number
}

/** What one policy decided about the cluster — the inverse of
 *  PolicyResourceResponse.
 *
 *  Carries one partiality the per-resource view never needed: a resource may be
 *  missing because the caller cannot read its namespace, so an absent subject is
 *  not evidence of absence. `withheldNamespaces` is how the UI says so. */
export interface PolicyCoverageResponse {
  evaluated: boolean
  status: string
  reasonCode?: string
  deniedGroups?: string[]
  liveUpdates: boolean
  /** The report-side name that matched — namespaced policies report as
   *  "namespace/name". */
  policy: string
  counts: PolicyResourceCounts
  scopeNamespaces: number
  withheldNamespaces: number
  clusterScoped?: boolean
  /** Separate from withheldNamespaces — a cluster-scoped subject has no
   *  namespace to name. */
  withheldClusterScoped?: boolean
  /** Outcomes dropped because they came only from a report family this caller
   *  cannot read. The server drops them before counting, so the note can state
   *  how many were left out rather than hedging that some might have been. */
  withheldByFamily?: number
  /** Report families the CALLER cannot read. Distinct from deniedGroups, which
   *  are the ones radar's own probe could not read. */
  unreadableFamilies?: string[]
  /** How many distinct resources these outcomes describe, and how many of those
   *  have at least one non-passing outcome. One resource matched by two rules
   *  produces two outcomes, so any sentence saying "resources" needs these —
   *  `counts` and `examined` are checks. Not derivable client-side: the per-rule
   *  subject lists are capped. */
  subjects?: number
  subjectsFailing?: number
  /** How many outcomes back these counts. */
  examined: number
  /** How many subjects the namespace view filter held back, across all rules. */
  hiddenByFilter?: number
  engines?: string[]
  rules: PolicyCoverageRule[]
}

export interface PolicyResourceResponse {
  /** False whenever the answer is "we could not check". Never treat an empty
   *  findings array as "clean" without consulting this first. */
  evaluated: boolean
  status: string
  reasonCode?: string
  /** Report families RADAR's own probe could not read, so nobody's answer
   *  carries them — not the same as `withheldByFamily`, which is what THIS
   *  caller may not see. Present even when evaluated: the answer is real but
   *  incomplete. */
  deniedGroups?: string[]
  /** False when the index is frozen at its initial contents. */
  liveUpdates: boolean
  counts: PolicyResourceCounts
  findings: PolicyResourceFinding[]
  /** Findings dropped because they came only from a report family this caller
   *  cannot read. `counts` excludes them too, so an all-passing count with this
   *  set is not an all-clear — it is the part of the answer they may see. */
  withheldByFamily?: number
}

/** Background work Kyverno currently has in flight for one policy, from
 *  GET /api/policy/policies/{policy}/queued. Computed server-side: the requests
 *  live in the engine's namespace, not the policy's. */
export interface PolicyQueuedResponse {
  /** Zero is a real answer — Kyverno deletes these seconds after they complete. */
  requests: number
  byState?: Record<string, number>
  /** Separates healthy churn from a stopped controller. */
  oldestPending?: string
  /** The only diagnosis these objects carry. */
  messages?: string[]
}

/** One Cluster pinned to a CloudNativePG image catalog. */
export interface CNPGCatalogUser {
  namespace: string
  name: string
  /** Absent when the reference carries no major — the screen must not render
   *  "asks for PostgreSQL 0". */
  major?: number
  /** A catalog-pinned Cluster has no spec.imageName, so this is the only place
   *  the image it actually resolved appears. */
  image?: string
}

/** Clusters referencing one image catalog, from the CNPG catalog-users endpoint. */
export interface CNPGCatalogUsersResponse {
  clusters: CNPGCatalogUser[]
}

/** What one Velero BackupStorageLocation holds, from the stored-backups lookup. */
export interface VeleroStoredBackupsResponse {
  backups: Array<{
    namespace: string
    name: string
    phase?: string
    expiration?: string
    completed?: string
    /** Created by Radar's Back up now from a schedule; absent from a Radar before it was reported. */
    manual?: boolean
  }>
  /** How many reached Completed and are still inside their retention — the rest
   *  are stored here but are not something to restore from. Counted over the
   *  whole location, like `stored`: recomputing it from the capped list above
   *  undercounts every location big enough to be capped. */
  restorable: number
  /** How many reached Completed but have aged out. Same scope as `restorable`. */
  expired?: number
  /** How many the location holds in total, counted before the list was capped. */
  stored?: number
  /** The list above is a page, not the whole location. */
  truncated?: boolean
}

/** One warning or error behind a Velero run's counts, with the scope Velero
 *  filed it under: "velero", "cluster", or a namespace name. */
export interface VeleroMessage {
  scope: string
  message: string
}

/** What a Backup or Restore actually reported, fetched on demand.
 *
 *  The counts live on the CR; these messages do not — they are in a results
 *  file in object storage that only Velero's controller can hand out a link to.
 *  So this is never present until someone asks for it. */
export interface VeleroRunMessagesResponse {
  warnings: VeleroMessage[]
  errors: VeleroMessage[]
  /** Set when the results file held more than were returned. */
  truncated?: boolean
}
