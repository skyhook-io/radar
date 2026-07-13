import type { RightsizingScanResponse, RightsizingRow } from '../../api/client'

export type ScanClass = 'increase' | 'reduction' | 'review' | 'in_range' | 'need_data'
export type ScanSignal = 'hpa' | 'oom' | 'bursty' | 'throttling' | 'query_error' | 'scaled_zero'

export interface RightsizingImpact {
  replicas: number
  cpuChange: number
  memoryChange: number
}

export interface RightsizingScanRow {
  id: string
  kind: string
  namespace: string
  name: string
  container: string
  replicas: number
  cpu?: RightsizingRow
  memory?: RightsizingRow
  classification: ScanClass
  signals: Set<ScanSignal>
  impact: RightsizingImpact
  system: boolean
  scaledToZero: boolean
}

const CLASS_RANK: Record<ScanClass, number> = {
  reduction: 0,
  increase: 1,
  review: 2,
  need_data: 3,
  in_range: 4,
}

const MIN_CPU_REDUCTION = 0.05
const MIN_MEMORY_REDUCTION = 64 * 1024 * 1024

export function classifyRows(
  rows: RightsizingRow[],
  replicas = 1,
  scaledToZero = false,
): ScanClass {
  if (rows.some((row) => row.queryError || row.fit === 'insufficient_history')) return 'need_data'
  if (scaledToZero) return 'review'
  if (
    rows.some(
      (row) =>
        (row.fit === 'under_requested' || row.fit === 'missing_request') && row.recommendedRequest,
    )
  )
    return 'increase'
  if (rows.some(needsManualReview)) return 'review'
  const impact = calculateImpact(rows, replicas)
  if (-impact.cpuChange >= MIN_CPU_REDUCTION || -impact.memoryChange >= MIN_MEMORY_REDUCTION)
    return 'reduction'
  return 'in_range'
}

function needsManualReview(row: RightsizingRow): boolean {
  return (
    row.hpaManaged ||
    row.currentPodOOM === true ||
    row.windowOomEvidence === true ||
    row.limitConflict === true ||
    row.recommendationReason === 'hpa_evidence_unavailable' ||
    row.recommendationReason === 'oom_evidence_unavailable' ||
    (isReduction(row) && (row.bursty || (row.throttleRatio ?? 0) >= 0.1))
  )
}

function isReduction(row: RightsizingRow): boolean {
  return (
    row.recommendedRequestValue != null &&
    row.currentRequestValue != null &&
    row.recommendedRequestValue < row.currentRequestValue
  )
}

export function calculateImpact(rows: RightsizingRow[], replicas: number): RightsizingImpact {
  const count = Math.max(replicas, 0)
  let cpuChange = 0
  let memoryChange = 0
  for (const row of rows) {
    if (row.recommendedRequestValue == null) continue
    const change = (row.recommendedRequestValue - (row.currentRequestValue ?? 0)) * count
    if (row.resource === 'cpu') cpuChange += change
    else memoryChange += change
  }
  return { replicas: count, cpuChange, memoryChange }
}

export function flattenScanResults(scan: RightsizingScanResponse): RightsizingScanRow[] {
  const result: RightsizingScanRow[] = []
  for (const workload of scan.workloads ?? []) {
    const byContainer = new Map<string, RightsizingRow[]>()
    for (const row of workload.rows ?? []) {
      const rows = byContainer.get(row.container) ?? []
      rows.push(row)
      byContainer.set(row.container, rows)
    }
    for (const [container, rows] of byContainer) {
      const signals = new Set<ScanSignal>()
      if (rows.some((row) => row.hpaManaged)) signals.add('hpa')
      if (rows.some((row) => row.currentPodOOM || row.windowOomEvidence)) signals.add('oom')
      if (rows.some((row) => row.bursty)) signals.add('bursty')
      if (rows.some((row) => (row.throttleRatio ?? 0) >= 0.1)) signals.add('throttling')
      if (rows.some((row) => row.queryError)) signals.add('query_error')
      if (workload.scaledToZero) signals.add('scaled_zero')
      const replicas = workload.replicas ?? 1
      result.push({
        id: `${workload.kind}/${workload.namespace}/${workload.name}/${container}`,
        kind: workload.kind,
        namespace: workload.namespace,
        name: workload.name,
        container,
        replicas,
        cpu: rows.find((row) => row.resource === 'cpu'),
        memory: rows.find((row) => row.resource === 'memory'),
        classification: classifyRows(rows, replicas, workload.scaledToZero),
        signals,
        impact: calculateImpact(rows, replicas),
        system: isSystemNamespace(workload.namespace),
        scaledToZero: workload.scaledToZero,
      })
    }
  }
  return result.sort(
    (a, b) =>
      CLASS_RANK[a.classification] - CLASS_RANK[b.classification] ||
      impactScore(b) - impactScore(a) ||
      a.namespace.localeCompare(b.namespace) ||
      a.name.localeCompare(b.name) ||
      a.container.localeCompare(b.container),
  )
}

function impactScore(row: RightsizingScanRow): number {
  return Math.max(
    Math.abs(row.impact.cpuChange) / MIN_CPU_REDUCTION,
    Math.abs(row.impact.memoryChange) / MIN_MEMORY_REDUCTION,
  )
}

export function scanClassCounts(rows: RightsizingScanRow[]): Record<ScanClass, number> {
  const counts: Record<ScanClass, number> = {
    increase: 0,
    reduction: 0,
    review: 0,
    in_range: 0,
    need_data: 0,
  }
  for (const row of rows) counts[row.classification]++
  return counts
}

export function isActionableClass(value: ScanClass): boolean {
  return value === 'increase' || value === 'reduction' || value === 'review'
}

function isSystemNamespace(namespace: string): boolean {
  return (
    namespace === 'kube-system' ||
    namespace === 'kube-public' ||
    namespace === 'kube-node-lease' ||
    namespace.startsWith('gke-managed-')
  )
}
