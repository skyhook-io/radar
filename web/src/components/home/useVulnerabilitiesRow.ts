import { useNavigate } from 'react-router-dom'
import type { AuditCardVulnerabilities } from '@skyhook-io/k8s-ui'
import { useAPIResources } from '../../api/apiResources'
import { useRadarFeature } from '../../api/client'
import { hasVulnerabilityReports, useVulnerabilities } from '../../api/vulnerabilities'
import { findingsCertainty } from '../vulnerabilities/parts'

// The Vulnerabilities row of the Home Checks card: present only where a
// scanner writes VulnerabilityReports and this Radar serves the rollup.
export function useVulnerabilitiesRow(namespaces: string[]): AuditCardVulnerabilities | undefined {
  const navigate = useNavigate()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  // Only the totals: the rows would be the whole tab's payload, every minute.
  const { data } = useVulnerabilities({ view: 'summary', namespaces }, available)
  if (!available || !data) return undefined
  const s = data.summary
  if (s.imagesScanned === 0 && s.imagesUnscanned === 0) return undefined
  return {
    imagesWithCritical: s.imagesWithCritical,
    fixableCriticalHigh: s.fixableCriticalHighCves,
    certainty: findingsCertainty(data),
    onNavigate: () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
      navigate({ pathname: '/checks/vulnerabilities', search: params.toString() })
    },
  }
}
