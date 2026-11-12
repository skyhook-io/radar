import { useNavigate } from 'react-router-dom'
import { FluxReportCard } from '@skyhook-io/k8s-ui'
import { pickFluxReport, summarizeFluxReport } from '@skyhook-io/k8s-ui/utils/flux-report'
import { useResources } from '../../api/client'
import { relatedResourcePath } from '../../utils/navigation'

const GROUP = 'fluxcd.controlplane.io'

// FluxOperatorCard shows Flux Operator's FluxReport above the GitOps table,
// read as the caller. No readable report means no card: never zeros for
// "couldn't read".
export function FluxOperatorCard({ available, refetchInterval }: { available: boolean; refetchInterval?: number }) {
  const navigate = useNavigate()
  const reports = useResources<any>('fluxreports', undefined, GROUP, { enabled: available, refetchInterval })
  const picked = reports.isSuccess && !reports.error ? pickFluxReport(reports.data ?? []) : null
  const namespace = picked?.report.metadata?.namespace
  const instances = useResources<any>('fluxinstances', namespace, GROUP, { enabled: available && !!namespace })
  if (!picked) return null
  const instance = instances.isSuccess ? instances.data?.[0] : undefined
  return (
    <FluxReportCard
      summary={summarizeFluxReport(picked.report, picked.total)}
      instance={instance ? { namespace: instance.metadata?.namespace, name: instance.metadata?.name, version: instance.spec?.distribution?.version } : null}
      onOpenResource={(ref) => navigate(relatedResourcePath(ref))}
    />
  )
}
