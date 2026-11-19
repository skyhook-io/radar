import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { ReadGap } from '@skyhook-io/k8s-ui/types/read-coverage'
import type { ResourceReadCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import type { APIResource } from '../types'
import { ApiError, apiFetch, fetchJSON, useRadarFeature } from './client'
import { readActionFor } from '@skyhook-io/k8s-ui/utils/read-actions'
import { apiUrl, getApiBase } from './config'
import { httpStatusMessage, readErrorResponse } from './httpErrors'
import { shouldRetryRadarQuery } from './radarFeatures'

// Mirrors internal/vulnerabilities/types.go.

/** 'summary' is the totals and coverage without rows, for a page that shows counts. */
export type VulnerabilityView = 'image' | 'fix' | 'cve' | 'workload' | 'summary'
export type VulnerabilitySeverity = 'critical' | 'high' | 'medium' | 'low' | 'unknown'

export interface VulnerabilityCounts {
  critical: number
  high: number
  medium: number
  low: number
  unknown: number
}

export interface VulnerabilityWorkloadRef {
  kind: string
  group: string
  namespace: string
  name: string
  container?: string
}

export interface VulnerabilityFinding {
  id: string
  severity: VulnerabilitySeverity
  /** The scanner gave no severity; this is the CVSS rating of score. */
  severityFromScore?: boolean
  score?: number
  package: string
  installedVersion: string
  fixedVersion?: string
  /** The smallest fixed version above the installed one, when Radar can order the package's versions. */
  fixTarget?: string
  packageType?: string
  title?: string
  link?: string
}

export interface VulnerableImage {
  id: string
  image: string
  digest?: string
  os?: string
  scannedAt: string
  counts: VulnerabilityCounts
  fixable: VulnerabilityCounts
  matching: number
  /** Past the request's findings budget: counts unknown, not zero. */
  findingsUnread?: boolean
  workloads: VulnerabilityWorkloadRef[]
  workloadsTotal: number
  findings: VulnerabilityFinding[]
  findingsTotal: number
  report: { kind: string; group?: string; namespace?: string; name: string }
}

export interface VulnerabilityCVEImage {
  imageId: string
  image: string
  packages: { package: string; installedVersion: string; fixedVersion?: string; fixTarget?: string }[]
  workloads: VulnerabilityWorkloadRef[]
  workloadsTotal: number
}

export interface VulnerabilityCVE {
  id: string
  severity: VulnerabilitySeverity
  score?: number
  packages: string[]
  fixable: boolean
  /** Of `images`, the ones where every package the CVE is in has a fixed version. */
  fixableImages: number
  title?: string
  link?: string
  images: number
  workloads: number
  affected: VulnerabilityCVEImage[]
}

export interface VulnerabilityFix {
  package: string
  packageType?: string
  /** The lowest listed release that fixes every listed CVE; absent when none can be chosen from the scanner's versions. */
  to?: string
  /** Each distinct fixed-version text the scanner wrote, when there is no target. */
  fixedVersions?: string[]
  from: string[]
  severity: VulnerabilitySeverity
  counts: VulnerabilityCounts
  cves: string[]
  cvesTotal: number
  images: number
  workloads: number
  affected: { imageId: string; image: string; installedVersions: string[]; workloads: VulnerabilityWorkloadRef[]; workloadsTotal: number }[]
}

export interface VulnerabilityWorkloadContainer {
  name: string
  image: string
  imageId?: string
  scanned: boolean
  scannedAt?: string
  findingsUnread?: boolean
  counts?: VulnerabilityCounts
  fixable?: VulnerabilityCounts
}

export interface VulnerabilityWorkload {
  kind: string
  group: string
  namespace: string
  name: string
  counts: VulnerabilityCounts
  fixable: VulnerabilityCounts
  containers: VulnerabilityWorkloadContainer[]
}

export interface VulnerabilitySummary {
  imagesScanned: number
  imagesClean: number
  imagesUnscanned: number
  imagesWithCritical: number
  fixableCriticalHighCves: number
  workloadsWithCriticalOrHigh: number
  workloads: number
  counts: VulnerabilityCounts
  newestScan?: string
  oldestScan?: string
}

/** A shared read gap; `kinds` names what wasn't read, absent meaning the reports themselves. */
export type VulnerabilityGap = ReadGap

export interface VulnerabilitiesResponse {
  source: string
  view: VulnerabilityView
  summary: VulnerabilitySummary
  images?: VulnerableImage[]
  cves?: VulnerabilityCVE[]
  workloads?: VulnerabilityWorkload[]
  fixes?: VulnerabilityFix[]
  /** Scanned images with no findings, kept out of the rows unless searched for. */
  clean?: { id: string; image: string; scannedAt: string; workloads: VulnerabilityWorkloadRef[]; workloadsTotal: number; report: VulnerableImage['report'] }[]
  /** Workloads whose every container is scanned and clean. */
  cleanWorkloads?: VulnerabilityWorkloadRef[]
  /** otherBuildScanned: Trivy's report is of another build of this tag, as after the tag moved. */
  unscanned?: { image: string; otherBuildScanned?: boolean; workloads: VulnerabilityWorkloadRef[]; workloadsTotal: number }[]
  coverage: {
    complete: boolean
    gaps: VulnerabilityGap[]
    inactiveReports: number
    truncated?: boolean
    rowsOmitted?: number
    cleanOmitted?: number
    unscannedOmitted?: number
  }
}

export interface VulnerabilitiesParams {
  view: VulnerabilityView
  namespaces?: string[]
  severities?: VulnerabilitySeverity[]
  fixableOnly?: boolean
  query?: string
  workload?: { kind: string; group?: string; namespace: string; name: string }
}

export interface Java4ShellFinding {
  workload: VulnerabilityWorkloadRef
  container: string
  image: string
  digest: string
  version: string
  fixedIn?: string
  pods: number
  vbmiPods: number
  unknownCPUPods: number
  mitigationDeclaredPods: number
}

export const JAVA4SHELL_CVE = 'CVE-2026-114938'

export interface Java4ShellUnassessed {
  workload: VulnerabilityWorkloadRef
  container: string
  image: string
  digest?: string
  reason: string
  pods: number
}

export interface Java4ShellResult {
  findings: Java4ShellFinding[]
  containers: number
  matches: number
  findingGroups: number
  unassessed: Record<string, number>
  unassessedRows: Java4ShellUnassessed[]
  unassessedGroups: number
  unaffectedByMajor?: Record<string, number>
  coverage: ResourceReadCoverage
  workload?: VulnerabilityWorkloadRef
}

export function useJava4ShellCheck(namespaces: string[], context: string, workload?: VulnerabilityWorkloadRef) {
  const { guard } = useRadarFeature('runtimeAdvisoryChecks')
  const { guard: guardProgress, support: progressSupport } = useRadarFeature('runtimeAdvisoryProgress')
  const client = useQueryClient()
  const base = getApiBase()
  const query = new URLSearchParams()
  if (workload) {
    query.set('workloadKind', workload.kind)
    query.set('workloadGroup', workload.group)
    query.set('workloadNamespace', workload.namespace)
    query.set('workloadName', workload.name)
  } else if (namespaces.length > 0) {
    query.set('namespaces', [...namespaces].sort().join(','))
  }
  const suffix = query.size ? `?${query}` : ''
  const path = '/vulnerabilities/java4shell' + suffix
  const queryKey = ['java4shell', base, context, path]
  const progressKey = [...queryKey, 'progress']
  const activeKey = ['java4shell-active', base]
  const { data: progress } = useQuery<Java4ShellProgress | null>({ queryKey: progressKey, enabled: false })
  const { data: activeScan } = useQuery<AbortController | null>({ queryKey: activeKey, enabled: false, gcTime: 6 * 60 * 1000 })
  const check = useQuery({
    queryKey,
    queryFn: async (request) => {
      if (progressSupport !== 'supported') {
        return guard(() => fetchJSON<Java4ShellResult>(path, { signal: request.signal, cache: 'no-store' }))
      }
      if (client.getQueryData(activeKey)) throw new Error('A Java4Shell check is already running.')
      const scan = new AbortController()
      client.setQueryData(activeKey, scan)
      client.setQueryData(progressKey, null)
      // The streaming request can finish while the user visits another Radar view.
      try {
        return await guardProgress(() => streamJava4Shell(apiUrl('/vulnerabilities/java4shell/stream' + suffix, base), update => {
          client.setQueryData(progressKey, update)
        }, scan.signal))
      } finally {
        if (client.getQueryData(activeKey) === scan) client.setQueryData(activeKey, null)
      }
    },
    enabled: false,
    retry: false,
  })
  const cancel = activeScan ? () => activeScan.abort(new DOMException('Java4Shell check canceled.', 'AbortError')) : undefined
  return { ...check, progress, cancel }
}

interface Java4ShellProgress {
  done: number
  total: number
}

type Java4ShellStreamMessage =
  | { type: 'progress'; progress: Java4ShellProgress }
  | { type: 'result'; report: Java4ShellResult }
  | { type: 'error'; error: string }

export async function streamJava4Shell(url: string, onProgress: (progress: Java4ShellProgress) => void, signal?: AbortSignal): Promise<Java4ShellResult> {
  const response = await apiFetch(url, { cache: 'no-store', signal })
  if (!response.ok) {
    const { body, unknownRoute } = await readErrorResponse(response)
    throw new ApiError(body.error || httpStatusMessage(response.status, response.statusText), response.status, body, { unknownRoute })
  }
  if (!response.body) throw new Error('Radar did not return a Java4Shell scan.')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let pending = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      pending += decoder.decode(value, { stream: true })
      let index: number
      while ((index = pending.indexOf('\n')) >= 0) {
        const message = JSON.parse(pending.slice(0, index)) as Java4ShellStreamMessage
        pending = pending.slice(index + 1)
        if (message.type === 'result') return message.report
        if (message.type === 'error') throw new Error(message.error)
        if (message.type === 'progress') onProgress(message.progress)
      }
    }
    throw new Error('The Java4Shell scan ended before Radar returned a result. Run the check again.')
  } finally {
    await reader.cancel().catch(() => {})
  }
}

// What an answer is about, apart from its filters: the workload and the
// namespaces. An answer about another one is never shown as a placeholder.
function subjectOfPath(path: unknown): string {
  if (typeof path !== 'string') return ''
  const q = new URLSearchParams(path.split('?')[1] ?? '')
  return [q.get('workloadKind'), q.get('workloadNamespace'), q.get('workloadName'), q.get('namespaces')].join('/')
}

/** The cluster doesn't serve the report kind: no scanner, not a failure. */
export function isKindUnknown(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && error.data?.error_code === 'kind_unknown'
}

/** A workload as the tab's `workload` URL filter writes it: Kind/namespace/name. */
export function workloadParam(w: { kind: string; namespace: string; name: string }): string {
  return `${w.kind}/${w.namespace}/${w.name}`
}

export function parseWorkloadParam(value: string | null): { kind: string; namespace: string; name: string } | undefined {
  const parts = (value ?? '').split('/')
  if (parts.length !== 3 || parts.some((p) => p === '')) return undefined
  return { kind: parts[0], namespace: parts[1], name: parts[2] }
}

// Trivy Operator's VulnerabilityReport is the one source the rollup reads;
// the Vulnerabilities tab, Home tile and drawer section show only when the
// cluster serves it.
export function hasVulnerabilityReports(resources: APIResource[] | undefined): boolean {
  return !!resources?.some((r) => r.group === 'aquasecurity.github.io' && r.name === 'vulnerabilityreports')
}

export function vulnerabilitiesPath(p: VulnerabilitiesParams): string {
  const params = new URLSearchParams()
  params.set('view', p.view)
  if (p.namespaces && p.namespaces.length > 0) params.set('namespaces', p.namespaces.join(','))
  if (p.severities && p.severities.length > 0) params.set('severity', p.severities.join(','))
  if (p.fixableOnly) params.set('fixable', '1')
  if (p.query?.trim()) params.set('q', p.query.trim())
  if (p.workload) {
    params.set('workloadKind', p.workload.kind)
    params.set('workloadNamespace', p.workload.namespace)
    params.set('workloadName', p.workload.name)
    if (p.workload.group) params.set('workloadGroup', p.workload.group)
  }
  return `/vulnerabilities?${params.toString()}`
}

// GET /api/vulnerabilities. Filters run server-side so the summary always
// describes the unfiltered scope and every surface (REST, MCP) filters alike.
export function useVulnerabilities(p: VulnerabilitiesParams, enabled = true) {
  const { guard, gatedKey } = useRadarFeature('vulnerabilities')
  const path = vulnerabilitiesPath(p)
  return useQuery<VulnerabilitiesResponse>({
    queryKey: ['vulnerabilities', path, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<VulnerabilitiesResponse>(path, signal)),
    enabled,
    // A read that settles by itself (the kind still syncing on a cold start,
    // the connection just changed) keeps retrying, so callers show their
    // loading state rather than an error.
    retry: (failures, error) => (readSettles(error) ? failures < 8 : shouldRetryRadarQuery(failures, error)),
    staleTime: 30_000,
    // The tab's tables can run to megabytes and scans change hourly, so they
    // refresh every five minutes; Home's totals and one workload's rows are
    // small and stay at a minute.
    refetchInterval: p.view === 'summary' || p.workload ? 60_000 : 300_000,
    refetchIntervalInBackground: false,
    // Keep showing the last answer, dimmed, while the tab's filters or view
    // change (the summary is the same in every view; a caller shows rows
    // only once data.view matches), but never carry one workload's or
    // namespace scope's answer to another.
    placeholderData: (prev, prevQuery) => (subjectOfPath(prevQuery?.queryKey[1]) === subjectOfPath(path) ? prev : undefined),
  })
}

// Whether a failed read settles by itself: Radar still loading the kind, or
// its connection just changed. A 503 naming something to fix doesn't.
export function readSettles(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  const code = error.data?.error_code
  return error.status === 409 || (error.status === 503 && (code === undefined || readActionFor(code) === 'retry'))
}
