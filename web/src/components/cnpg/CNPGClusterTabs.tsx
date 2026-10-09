import { useRef, type ReactNode } from 'react'
import type { RenderDiagnoseAction } from '../../context/DiagnoseCustomization'
import { useSearchParams } from 'react-router-dom'
import { CNPGDimensionMark, CNPGDimensionVerdict, CNPGServingStatus, coverageReadable, toneTextClass, type CNPGDimension, type CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { useCNPGRuntime } from '../../api/cnpg'
import { CNPGStorage } from './CNPGStorage'
import { CNPGArchivingRepair } from './CNPGArchivingRepair'
import { CNPGProtection } from './CNPGProtection'
import { CNPGRestoreValidation } from './recovery/CNPGRestoreValidation'
import { CNPGRestoreButton } from './recovery/CNPGRestoreButton'
import { assessRestoreSources } from './recovery/restoreModel'
import { useCNPGRestoreCapability } from '../../api/cnpg-recovery'
import { CNPGScreenGate } from './shared'
import { useCNPGClusterAssessment } from './useCNPGClusterAssessment'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { CNPGProtectionSetup } from './protection/CNPGProtectionSetup'

/** The health mark beside a tab: the same assessment the Overview explains, for the dimension that tab holds. */
export function CNPGTabMark({ namespace, name, id }: { namespace: string; name: string; id: CNPGDimension['id'] }) {
  const { dimensions } = useCNPGClusterAssessment(namespace, name)
  const dimension = dimensions?.find((d) => d.id === id)
  return dimension ? <CNPGDimensionMark dimension={dimension} /> : null
}

/** Why a tab carries its mark, as the tab's first line; nothing when the dimension is fine. */
export function CNPGTabVerdict({ namespace, name, id, className, alwaysShow }: { namespace: string; name: string; id: CNPGDimension['id']; className?: string; alwaysShow?: boolean }) {
  const { dimensions } = useCNPGClusterAssessment(namespace, name)
  const dimension = dimensions?.find((d) => d.id === id)
  return dimension ? <CNPGDimensionVerdict dimension={dimension} className={className} alwaysShow={alwaysShow} /> : null
}

/** Whether the cluster serves writes, on its title line, opening the tab that explains it. */
export function CNPGClusterServing({ namespace, name, onSelect }: { namespace: string; name: string; onSelect: () => void }) {
  const { dimensions } = useCNPGClusterAssessment(namespace, name)
  const dimension = dimensions?.find((d) => d.id === 'serving')
  return dimension ? <CNPGServingStatus dimension={dimension} onSelect={onSelect} /> : null
}

/** Storage: volumes, what holds WAL, and resize, with the history of both one click away. */
export function CNPGStorageTab({
  namespace,
  name,
  onOpenHistory,
  onOpenReplication,
}: {
  namespace: string
  name: string
  onOpenHistory?: () => void
  onOpenReplication?: () => void
}) {
  const runtime = useCNPGRuntime(namespace, name)
  const { row, query } = useCNPGClusterAssessment(namespace, name)
  const clusterObject = row?.cluster
  const primary = runtime.data?.instances.find((i) => i.role === 'primary')
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
      <CNPGTabVerdict namespace={namespace} name={name} id="storage" />
      {row && <SlotRelief row={row} onOpenReplication={onOpenReplication} />}
      <CNPGStorage namespace={namespace} name={name} primary={primary} runtime={runtime} clusterObject={clusterObject} restoreState={assessRestoreSources(query.data, namespace, clusterObject).recoveryState} walArchiving={row?.protection.walArchiving} />
    </div>
  )
}

/**
 * What to do about WAL an inactive slot holds: the standby it is kept for,
 * whether that standby receives anything, and the two ways the WAL is freed.
 */
function SlotRelief({ row, onOpenReplication }: { row: CNPGFleetRow; onOpenReplication?: () => void }) {
  const slots = row.problems.filter((p) => p.reason === 'CNPGInactiveSlot')
  if (slots.length === 0) return null
  const gaps = new Set(row.problems.filter((p) => p.reason === 'CNPGStandbyNotReceiving').map((p) => p.subject.name))
  return (
    <section className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      {slots.map((p) => {
        const standby = p.instance
        return (
          <div key={p.id} className="space-y-1 text-sm">
            <div className={toneTextClass('degraded')}>{p.title}</div>
            <p className="text-theme-text-secondary">
              The primary keeps that WAL until {standby ?? 'the slot’s consumer'} catches up or the slot is dropped.
              {standby && gaps.has(standby) ? ` ${standby} is not streaming from the primary, and the slot advances only while it does — even if ${standby} replays WAL from the archive meanwhile.` : ''}
            </p>
            {standby && (
              <p className="text-theme-text-secondary">
                To free it, get {standby} streaming again — restart it, after checking its logs for why it stopped — or destroy it so CloudNativePG recreates it from
                a fresh copy of the primary; the operator drops the HA slot of an instance that no longer exists.
              </p>
            )}
            {onOpenReplication && (
              <button type="button" onClick={onOpenReplication} className="text-xs text-accent-text hover:underline">
                {standby ? `${standby} on Replication →` : 'Replication →'}
              </button>
            )}
          </div>
        )
      })}
    </section>
  )
}

/** Backups: this cluster's recovery evidence, runs, schedules and destination, plus restore validation once it was restored. */
export function CNPGBackupsTab({
  namespace,
  name,
  onInspect,
  onOpenLogs,
  onOpenOperator,
  onOpenYaml,
}: {
  namespace: string
  name: string
  onInspect: (r: SelectedResource) => void
  onOpenLogs?: (pod: string, container: string) => void
  onOpenOperator?: () => void
  onOpenYaml?: () => void
}) {
  const { query, fleet } = useCNPGFleet([namespace])
  const { row, runtime } = useCNPGClusterAssessment(namespace, name)
  const primary = runtime.data?.instances.find((i) => i.role === 'primary')
  const [searchParams] = useSearchParams()
  const archivingRepairRef = useRef<HTMLDivElement>(null)
  const restore = useCNPGRestoreCapability(namespace)
  const restoreBlocked = restore.data ? (restore.data.allowed ? undefined : restore.data.reason ?? 'Not allowed') : restore.isLoading ? 'Checking whether you can create a Cluster here…' : undefined
  return (
    <CNPGScreenGate query={query} fleet={fleet}>
      {(data, readyFleet) => {
        const cluster = readyFleet.rows.find((r) => r.namespace === namespace && r.name === name)?.cluster
        const nothing = assessRestoreSources(data, namespace, cluster).disabledReason
        return (
        <div className="flex min-h-0 flex-1 flex-col">
          <CNPGTabVerdict namespace={namespace} name={name} id="protection" className="px-4 pt-4" />
          <div className="flex flex-wrap items-center gap-2 px-4 pt-3">
            <CNPGProtectionSetup key={`${namespace}/${name}`} namespace={namespace} name={name} onInspect={onInspect} onOpenYaml={onOpenYaml} onOpenOperator={onOpenOperator} onOpenArchivingRepair={() => archivingRepairRef.current?.scrollIntoView({ block: 'start' })} />
            <CNPGRestoreButton namespace={namespace} entry={{ kind: 'cluster', name }} disabledReason={restoreBlocked ?? nothing} />
            <span className="text-xs text-theme-text-tertiary">{restoreBlocked ?? nothing ?? 'Restores into a new Cluster beside this one; this cluster is not changed.'}</span>
          </div>
          {row && (
            <div ref={archivingRepairRef} className="px-4 pt-4">
              <CNPGArchivingRepair
                row={row}
                primary={primary}
                objectStores={data.objects.objectStores ?? []}
                backups={coverageReadable(data.coverage?.backups ?? { state: 'notInstalled' }, namespace) ? (data.objects.backups ?? []) : null}
                onOpenLogs={onOpenLogs}
                onOpenOperator={onOpenOperator}
                onInspect={onInspect}
              />
            </div>
          )}
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
        )
      }}
    </CNPGScreenGate>
  )
}

export function CNPGInvestigationAction({ namespace, name, render, context }: {
  namespace: string; name: string; render: RenderDiagnoseAction; context: Parameters<RenderDiagnoseAction>[0]
}): ReactNode {
  const { row, dimensions } = useCNPGClusterAssessment(namespace, name)
  const problem = !!row?.problems.length || dimensions?.some((d) => ['degraded', 'alert', 'unhealthy'].includes(d.tone))
  const assessed = !!row && !!dimensions && dimensions.every((d) => d.tone !== 'unknown')
  return render({ ...context, health: problem ? 'problem' : assessed ? 'healthy' : 'unknown' })
}
