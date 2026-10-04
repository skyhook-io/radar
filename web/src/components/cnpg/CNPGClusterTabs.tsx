import { useSearchParams } from 'react-router-dom'
import { CNPGClusterCertificates, CNPGConnectSection, CNPGDimensionChips, refToSelectedResource, type CNPGDimension, type NavigateToRef } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useCNPGRuntime } from '../../api/cnpg'
import { CNPGStorage } from './CNPGStorage'
import { CNPGProtection } from './CNPGProtection'
import { CNPGRestoreValidation } from './recovery/CNPGRestoreValidation'
import { CNPGRestoreButton } from './recovery/CNPGRestoreButton'
import { useCNPGRestoreCapability } from '../../api/cnpg-recovery'
import { CNPGScreenGate } from './shared'
import { useCNPGClusterAssessment } from './useCNPGClusterAssessment'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'

/**
 * The four health chips under the Cluster's title, on every tab: the same
 * assessment the Overview explains, each opening the tab that holds its detail.
 */
export function CNPGClusterHeaderChips({ namespace, name, onSelect }: { namespace: string; name: string; onSelect: (id: CNPGDimension['id']) => void }) {
  const { dimensions } = useCNPGClusterAssessment(namespace, name)
  if (!dimensions) return null
  return <CNPGDimensionChips dimensions={dimensions} onSelect={onSelect} />
}

/** Storage: volumes, what holds WAL, and resize, with the history of both one click away. */
export function CNPGStorageTab({ namespace, name, onOpenHistory }: { namespace: string; name: string; onOpenHistory?: () => void }) {
  const runtime = useCNPGRuntime(namespace, name)
  const { fleet } = useCNPGFleet([namespace])
  const clusterObject = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)?.cluster
  const primary = runtime.data?.permission.proxy === 'denied' ? undefined : runtime.data?.instances.find((i) => i.role === 'primary')
  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-sm font-semibold text-theme-text-primary">Volumes and WAL</h2>
        {onOpenHistory && (
          <button type="button" onClick={onOpenHistory} className="ml-auto text-xs text-accent-text hover:underline">
            Storage history →
          </button>
        )}
      </div>
      <CNPGStorage namespace={namespace} name={name} primary={primary} clusterObject={clusterObject} />
    </div>
  )
}

/** Backups: this cluster's recovery evidence, runs, schedules and destination, plus restore validation once it was restored. */
export function CNPGBackupsTab({ namespace, name, onInspect }: { namespace: string; name: string; onInspect: (r: SelectedResource) => void }) {
  const { query, fleet } = useCNPGFleet([namespace])
  const [searchParams] = useSearchParams()
  const restore = useCNPGRestoreCapability(namespace)
  const restoreBlocked = restore.data ? (restore.data.allowed ? undefined : restore.data.reason ?? 'Not allowed') : restore.isLoading ? 'Checking whether you can create a Cluster here…' : undefined
  return (
    <CNPGScreenGate query={query} fleet={fleet}>
      {(data, readyFleet) => (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex flex-wrap items-center gap-2 px-5 pt-3 xl:px-7">
            <CNPGRestoreButton namespace={namespace} entry={{ kind: 'cluster', name }} disabledReason={restoreBlocked} />
            <span className="text-xs text-theme-text-tertiary">Restores into a new Cluster beside this one; this cluster is not changed.</span>
          </div>
          {readyFleet.rows.find((r) => r.namespace === namespace && r.name === name)?.cluster?.spec?.bootstrap?.recovery && (
            <CNPGRestoreValidation namespace={namespace} name={name} />
          )}
          <CNPGProtection
            data={data}
            fleet={readyFleet}
            namespaces={[namespace]}
            searchParams={searchParams}
            onSetParams={() => {}}
            onInspect={onInspect}
            inspected={null}
            onClearNamespaces={() => {}}
            scopeCluster={{ namespace, name }}
          />
        </div>
      )}
    </CNPGScreenGate>
  )
}

/** Above the declared settings on the Configuration tab: how to connect, and the certificates that secure it. */
export function CNPGConfigurationLead({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate: (r: SelectedResource) => void }) {
  const { row, ha } = useCNPGClusterAssessment(namespace, name)
  const go: NavigateToRef = (ref) => onNavigate(refToSelectedResource(ref))
  if (!row?.cluster) return null
  return (
    <div className="mb-4 space-y-2 rounded-xl border border-theme-border bg-theme-surface px-4 pb-3 shadow-theme-sm">
      <CNPGConnectSection cluster={row.cluster} poolers={row.poolerObjects} poolersKnown={row.poolersKnown} onNavigate={go} />
      {ha.data && <CNPGClusterCertificates ha={ha.data} onNavigate={go} />}
    </div>
  )
}
