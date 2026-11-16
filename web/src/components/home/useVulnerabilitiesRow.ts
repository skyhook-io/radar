import { useNavigate } from 'react-router-dom'
import type { AuditCardVulnerabilities } from '@skyhook-io/k8s-ui'
import { readActionFor } from '@skyhook-io/k8s-ui/utils/read-actions'
import { useAPIResources } from '../../api/apiResources'
import { ApiError, useRadarFeature } from '../../api/client'
import { isRadarFeatureUnsupported } from '../../api/radarFeatures'
import { hasVulnerabilityReports, isKindUnknown, useVulnerabilities } from '../../api/vulnerabilities'
import { findingsCertainty } from '../vulnerabilities/parts'

// The Vulnerabilities row of the Home Checks card: present only where a
// scanner writes VulnerabilityReports and this Radar serves the rollup.
export function useVulnerabilitiesRow(namespaces: string[]): AuditCardVulnerabilities | undefined {
  const navigate = useNavigate()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  // Only the totals: the rows would be the whole tab's payload, every minute.
  const { data, error } = useVulnerabilities({ view: 'summary', namespaces }, available)
  if (!available) return undefined
  const onNavigate = () => {
    const params = new URLSearchParams()
    if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
    navigate({ pathname: '/checks/vulnerabilities', search: params.toString() })
  }
  // The query keeps its last answer through a failed poll; a failure that
  // persists decides the row over it.
  const failure = error ? rowOnError(error) : 'keep'
  if (failure === 'failed') return { imagesWithCritical: 0, fixableCriticalHigh: 0, certainty: 'unknown', readFailed: true, onNavigate }
  if (failure === 'hide') return undefined
  if (!data) return undefined
  const s = data.summary
  if (s.imagesScanned === 0 && s.imagesUnscanned === 0) return undefined
  return {
    imagesWithCritical: s.imagesWithCritical,
    fixableCriticalHigh: s.fixableCriticalHighCves,
    certainty: findingsCertainty(data),
    onNavigate,
  }
}

// What a failed read does to the row: keep the last answer through one that
// settles by itself (Radar still loading, its connection just changed), hide
// the row when it has nothing to say (an older Radar, Trivy removed, no
// access to the reports), and otherwise say the read failed. A 503 naming
// something to fix, such as Radar's own identity denied the reports, doesn't
// settle by itself.
export function rowOnError(error: unknown): 'keep' | 'hide' | 'failed' {
  if (isRadarFeatureUnsupported(error) || isKindUnknown(error)) return 'hide'
  if (!(error instanceof ApiError)) return 'failed'
  const code = error.data?.error_code
  if (error.status === 409 || (error.status === 503 && (code === undefined || readActionFor(code) === 'retry'))) return 'keep'
  return error.status === 403 ? 'hide' : 'failed'
}
