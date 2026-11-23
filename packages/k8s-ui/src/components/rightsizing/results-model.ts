import type { DatadogSizingWorkload, RightsizingRow } from '../../types/rightsizing'
import { datadogRowReview, explainRightsizingReasons, isDatadogRequestActionable } from '../../utils/rightsizing-reasons'

export type ScanClass = 'increase' | 'reduction' | 'review' | 'in_range' | 'need_data'

export interface RightsizingImpact {
  replicas: number
  cpuChange: number
  memoryChange: number
}

interface ScanWorkload {
  kind: string
  namespace: string
  name: string
  replicas: number
  scaledToZero: boolean
  rows: RightsizingRow[]
}

export interface RightsizingResultRow extends Omit<ScanWorkload, 'rows'> {
  id: string
  container: string
  clusterId?: string
  clusterName?: string
  source?: 'datadog' | 'prometheus'
  observedAt?: string
  cpu?: RightsizingRow
  memory?: RightsizingRow
  classification: ScanClass
  impact: RightsizingImpact
  workloadReason?: string
  workloadImpact?: Pick<RightsizingImpact, 'cpuChange' | 'memoryChange'>
  review?: Partial<Record<RightsizingRow['resource'], string>>
}

const CLASS_RANK: Record<ScanClass, number> = {
  increase: 1,
  reduction: 2,
  review: 3,
  need_data: 4,
  in_range: 5,
}

const MIN_CPU_REDUCTION_MILLI = 50
const MIN_MEMORY_REDUCTION = 64 * 1024 * 1024

function isMaterialReduction(impact: RightsizingImpact): boolean {
  return (
    Math.round(-impact.cpuChange * 1000) >= MIN_CPU_REDUCTION_MILLI ||
    -impact.memoryChange >= MIN_MEMORY_REDUCTION
  )
}

export function rightsizingPolicyCoverage(rows: RightsizingRow[]): 'empty' | 'legacy' | 'current' | 'incomplete' {
  if (!rows.length) return 'empty'
  if (rows.every((row) => row.decision === undefined)) return 'legacy'
  const complete = rows.every((row) => {
    const decision = row.decision
    if (!decision) return false
    if (decision.status === 'review') return Array.isArray(decision.reasons) && decision.reasons.length > 0 &&
      decision.reasons.every((reason) => typeof reason === 'string')
    if (decision.reasons !== undefined && (!Array.isArray(decision.reasons) || decision.reasons.length > 0)) return false
    if (decision.status === 'actionable') return row.recommendedRequest != null && Number.isFinite(row.recommendedRequestValue)
    return decision.status === 'none' && row.recommendedRequest == null && row.recommendedRequestValue == null
  })
  return complete ? 'current' : 'incomplete'
}

export function classifyRows(rows: RightsizingRow[], replicas = 1, scaledToZero = false): ScanClass {
  const evidenced = rows.filter((row) => !row.queryError && row.fit !== 'insufficient_history')
  if (evidenced.length === 0) return 'need_data'
  if (scaledToZero) return 'review'
  if (
    evidenced.some(
      (row) => (row.fit === 'under_requested' || row.fit === 'missing_request') && row.recommendedRequest &&
        (!row.decision || row.decision.status === 'actionable'),
    )
  )
    return 'increase'
  if (evidenced.some(needsManualReview)) return 'review'
  if (isMaterialReduction(calculateImpact(evidenced, replicas))) return 'reduction'
  return evidenced.length === rows.length ? 'in_range' : 'need_data'
}

export function needsManualReview(row: RightsizingRow): boolean {
  if (row.decision) return row.decision.status === 'review'
  return (
    row.hpaManaged ||
    needsSafetyReview(row) ||
    (isReduction(row) && row.bursty === true) ||
    row.recommendationReason === 'hpa_evidence_unavailable' ||
    row.recommendationReason === 'oom_evidence_unavailable' ||
    // Mirrors ManualReviewReasons: Radar does not size a pod-level request.
    row.fit === 'pod_level_request'
  )
}

function needsSafetyReview(row: RightsizingRow): boolean {
  if (row.decision) return row.decision.reasons?.some((reason) =>
    ['current_pod_oom', 'oom_in_window', 'limit_conflict', 'throttled_reduction'].includes(reason)) ?? false
  return (
    row.currentPodOOM === true ||
    row.windowOomEvidence === true ||
    row.limitConflict === true ||
    (isReduction(row) && (row.throttleRatio ?? 0) >= 0.1)
  )
}

function scanPriority(row: RightsizingResultRow): number {
  if (
    !row.scaledToZero &&
    [row.cpu, row.memory].some(
      (resource) =>
        resource &&
        !resource.queryError &&
        resource.fit !== 'insufficient_history' &&
        needsSafetyReview(resource),
    )
  )
    return 0
  if (!row.scaledToZero && [row.cpu, row.memory].some(isReviewedIncrease)) return CLASS_RANK.increase
  return CLASS_RANK[row.classification]
}

export function isReviewedIncrease(row?: RightsizingRow): boolean {
  return row?.decision?.status === 'review' && row.recommendedRequest != null &&
    (row.fit === 'under_requested' || row.fit === 'missing_request')
}

function isReduction(row: RightsizingRow): boolean {
  return (
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  )
}

export function calculateImpact(
  rows: RightsizingRow[],
  replicas: number,
  actionable: (row: RightsizingRow) => boolean = (row) => row.decision ? row.decision.status === 'actionable' : !needsManualReview(row),
): RightsizingImpact {
  const count = Math.max(replicas, 0)
  let cpuChange = 0
  let memoryChange = 0
  for (const row of rows) {
    if (row.recommendedRequestValue == null || !actionable(row)) continue
    const change = (row.recommendedRequestValue - (row.currentRequestValue ?? 0)) * count
    if (row.resource === 'cpu') cpuChange += change
    else memoryChange += change
  }
  return { replicas: count, cpuChange, memoryChange }
}

export function groupRightsizingRows(measurements: RightsizingRow[]): Map<string, RightsizingRow[]> {
  const byContainer = new Map<string, RightsizingRow[]>()
  for (const row of measurements) {
    const rows = byContainer.get(row.container) ?? []
    rows.push(row)
    byContainer.set(row.container, rows)
  }
  return byContainer
}

interface Cluster {
  id: string
  name: string
  observedAt?: string
}

export function workloadResultRows(
  workload: DatadogSizingWorkload | ScanWorkload,
  cluster: Cluster,
  workloadImpact?: RightsizingResultRow['workloadImpact'],
): (RightsizingResultRow & { clusterId: string })[]
export function workloadResultRows(workload: ScanWorkload): RightsizingResultRow[]
export function workloadResultRows(
  workload: ScanWorkload | DatadogSizingWorkload,
  cluster?: Cluster,
  workloadImpact?: RightsizingResultRow['workloadImpact'],
): RightsizingResultRow[] {
  const byContainer = groupRightsizingRows(workload.rows)
  const { kind, namespace, name, replicas, scaledToZero } = workload
  return [...byContainer].map(([container, rows], index) => {
    const id = [cluster?.id, kind, namespace, name, container].filter(Boolean).join('/')
    const result = {
      id,
      clusterId: cluster?.id,
      clusterName: cluster?.name,
      observedAt: cluster?.observedAt,
      workloadImpact: byContainer.size > 1 && index === 0 ? workloadImpact : undefined,
      kind,
      namespace,
      name,
      container,
      replicas,
      scaledToZero,
      cpu: rows.find((row) => row.resource === 'cpu'),
      memory: rows.find((row) => row.resource === 'memory'),
    }
    const datadog = 'source' in workload && workload.source === 'datadog' ? workload : undefined
    if (datadog && !rows[0]?.decision) {
      const actionable = (row: RightsizingRow) => isDatadogRequestActionable(datadog, row)
      const impact = calculateImpact(rows, replicas, actionable)
      const review: RightsizingResultRow['review'] = {}
      for (const row of rows) {
        if (actionable(row) || row.queryError || row.fit === 'insufficient_history') continue
        if (!row.recommendedRequest && !datadog.reviewReasons?.[container]?.[row.resource]?.length) continue
        const reason = datadogRowReview(datadog, row)
        if (reason) review[row.resource] = reason
      }
      let classification: ScanClass = 'in_range'
      if (impact.cpuChange > 0 || impact.memoryChange > 0) classification = 'increase'
      else if (review.cpu || review.memory) classification = 'review'
      else if (isMaterialReduction(impact)) classification = 'reduction'
      else if (rows.some((row) => row.queryError || row.fit === 'insufficient_history'))
        classification = 'need_data'
      return {
        ...result,
        source: datadog.source,
        classification,
        impact,
        review,
        workloadReason: index === 0 ? datadog.reason : undefined,
      }
    }
    return {
      ...result,
      source: datadog ? datadog.source : cluster ? 'prometheus' : undefined,
      review: Object.fromEntries(rows.flatMap((row) => row.decision?.status === 'review'
        ? [[row.resource, explainRightsizingReasons(row.decision.reasons ?? [], row.resource)]] : [])),
      workloadReason: index === 0 ? datadog?.reason : undefined,
      classification: classifyRows(rows, replicas, scaledToZero),
      impact: calculateImpact(rows, replicas),
    }
  })
}

export function compareScanResults(a: RightsizingResultRow, b: RightsizingResultRow): number {
  return (
    scanPriority(a) - scanPriority(b) ||
    impactScore(b) - impactScore(a) ||
    a.namespace.localeCompare(b.namespace) ||
    a.name.localeCompare(b.name) ||
    a.container.localeCompare(b.container)
  )
}

function impactScore(row: RightsizingResultRow): number {
  return Math.max(
    Math.abs(row.impact.cpuChange * 1000) / MIN_CPU_REDUCTION_MILLI,
    Math.abs(row.impact.memoryChange) / MIN_MEMORY_REDUCTION,
  )
}

export type ClassFilter = ScanClass | 'actions' | 'all'

export const resultFilterKeys = ['rfClass', 'rfQ', 'rfKind', 'rfNs', 'rfScope'] as const

export function readResultFilters(params: URLSearchParams, defaultClass: ClassFilter) {
  const value = params.get('rfClass')
  const classFilter = value === 'all' || value === 'actions' || (value && Object.hasOwn(CLASS_RANK, value))
    ? value as ClassFilter : defaultClass
  return {
    classFilter,
    search: params.get('rfQ') ?? '',
    kindFilter: params.get('rfKind') ?? '',
    namespaceFilter: params.get('rfNs') ?? '',
    includeSystem: params.get('rfScope') === 'all',
  }
}

export function matchesResultIdentity(
  row: Pick<RightsizingResultRow, 'namespace' | 'name' | 'kind'> & Partial<Pick<RightsizingResultRow, 'container' | 'clusterName'>>,
  filters: ReturnType<typeof readResultFilters>,
): boolean {
  if (filters.kindFilter && row.kind !== filters.kindFilter) return false
  if (filters.namespaceFilter && row.namespace !== filters.namespaceFilter) return false
  if (!filters.search.trim()) return true
  const fields = [row.namespace, row.name, row.container, row.kind, row.clusterName].map((field) => field?.toLowerCase())
  return filters.search.toLowerCase().trim().split(/[\s/]+/).every((word) => fields.some((field) => field?.includes(word)))
}

export function matchesResultClass(row: RightsizingResultRow, filter: ClassFilter): boolean {
  if (filter === 'all' || row.classification === filter) return true
  if (filter === 'actions') return isActionableClass(row.classification)
  if (filter === 'increase') return !row.scaledToZero && [row.cpu, row.memory].some(isReviewedIncrease)
  if (filter === 'reduction') return isMaterialReduction(row.impact)
  if (filter === 'review') {
    if (row.review?.cpu || row.review?.memory) return true
    return row.source !== 'datadog' && [row.cpu, row.memory].some((resource) => resource &&
      !resource.queryError && resource.fit !== 'insufficient_history' && needsManualReview(resource))
  }
  return false
}

export function scanClassCounts(rows: RightsizingResultRow[]): Record<ScanClass, number> {
  const counts = { increase: 0, reduction: 0, review: 0, in_range: 0, need_data: 0 }
  const classes = Object.keys(counts) as ScanClass[]
  for (const row of rows) {
    for (const key of classes) if (matchesResultClass(row, key)) counts[key]++
  }
  return counts
}

function isActionableClass(value: ScanClass): boolean {
  return value === 'increase' || value === 'reduction' || value === 'review'
}

export function isSizingSystemNamespace(namespace: string): boolean {
  return (
    namespace === 'kube-system' ||
    namespace === 'kube-public' ||
    namespace === 'kube-node-lease' ||
    namespace.startsWith('gke-managed-')
  )
}
