import { useNavigate } from 'react-router-dom'
import type { AuditCardVulnerabilities } from '@skyhook-io/k8s-ui'
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
  if (error && !transient(error)) {
    return readFailed(error)
      ? { imagesWithCritical: 0, fixableCriticalHigh: 0, certainty: 'unknown', readFailed: true, onNavigate }
      : undefined
  }
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

// A failure that settles by itself: Radar still loading, or its connection
// just changed. The last answer stands meanwhile.
function transient(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 409 || error.status === 503)
}

// A failure worth showing on Home. Not one that means the row has nothing
// to say: an older Radar, Trivy removed, or no access to the reports.
function readFailed(error: unknown): boolean {
  if (isRadarFeatureUnsupported(error) || isKindUnknown(error)) return false
  return !(error instanceof ApiError && error.status === 403)
}
