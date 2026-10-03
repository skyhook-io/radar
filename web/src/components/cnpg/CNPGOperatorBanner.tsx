import { useNavigate } from 'react-router-dom'
import { AlertBanner } from '@skyhook-io/k8s-ui'
import { useCNPGOperatorStatus } from '../../api/cnpg'
import { cnpgScreenPath } from './routes'
import { cnpgOperatorBannerModel } from './operatorStatus'

/**
 * Shown above CNPG status while the operator that watches these namespaces is
 * observed not leading, or its admission webhook rejects writes: the phase and
 * ready counts below are then what the operator last wrote.
 */
export function CNPGOperatorBanner({ namespaces, className }: { namespaces: string[]; className?: string }) {
  const navigate = useNavigate()
  const q = useCNPGOperatorStatus(namespaces)
  const model = cnpgOperatorBannerModel(q.data?.namespaces, namespaces)
  if (!model) return null
  const { stale, rejects } = model
  const title = stale
    ? 'The CloudNativePG operator is not reconciling: status below may be stale'
    : 'CloudNativePG writes are being rejected'
  const scope = stale && stale.namespaces.length < new Set(namespaces).size ? `Clusters in ${stale.namespaces.join(', ')}.` : ''
  return (
    <AlertBanner
      variant="warning"
      title={title}
      message={[scope, ...(stale?.reasons ?? []), rejects ?? ''].filter(Boolean).join(' ')}
      className={className}
    >
      <button type="button" onClick={() => navigate(cnpgScreenPath('operator'))} className="mt-1.5 text-xs font-medium text-accent-text hover:underline">
        Open Operator
      </button>
    </AlertBanner>
  )
}
