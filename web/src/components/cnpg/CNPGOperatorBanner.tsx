import { clsx } from 'clsx'
import { AlertBanner } from '@skyhook-io/k8s-ui'
import { useCNPGOperatorStatus } from '../../api/cnpg'
import { cnpgScreenPath } from './routes'
import { cnpgOperatorBannerModel } from './operatorStatus'
import { useCNPGNavigate } from './useCNPGNavigate'

/**
 * Shown above CNPG status while the operator that watches these namespaces is
 * observed not leading, or its admission webhook rejects writes: the phase and
 * ready counts below are then what the operator last wrote.
 */
export function CNPGOperatorBanner({ namespaces, className }: { namespaces: string[]; className?: string }) {
  const navigate = useCNPGNavigate()
  const q = useCNPGOperatorStatus(namespaces)
  const model = cnpgOperatorBannerModel(q.data?.namespaces, namespaces)
  if (!model) return null
  const { stale, rejects } = model
  const title = stale
    ? 'The CloudNativePG operator is not reconciling: status below may be stale'
    : 'CloudNativePG writes are being rejected'
  const scope = stale && stale.namespaces.length < new Set(namespaces).size ? `Clusters in ${stale.namespaces.join(', ')}.` : ''
  // Tighter than the default banner, never smaller: the title, icon, colours
  // and every reason stay; the link moves onto the title line. A host that
  // passes no className keeps the default bottom margin.
  return (
    <AlertBanner
      variant="warning"
      title={title}
      message={[scope, ...(stale?.reasons ?? []), rejects ?? ''].filter(Boolean).join(' ') || undefined}
      action={
        <button type="button" onClick={() => navigate(cnpgScreenPath('operator'))} className="text-xs font-medium text-accent-text hover:underline">
          Open Operator →
        </button>
      }
      className={clsx('px-3 py-2', className ?? 'mb-4')}
    />
  )
}
