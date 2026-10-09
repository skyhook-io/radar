import { useEffect, useId, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { useLocation, useSearchParams } from 'react-router-dom'
import {
  AlertBanner,
  DialogPortal,
  Badge,
  FactGrid,
  FactRow,
  FactValue,
  getCNPGClusterBarmanPlugin,
  cnpgBackupDeclaration,
  cnpgScheduleDestinationBlocker,
  coverageReadable,
  getRadarUpgradeRequirement,
  RadarUpgradeNote,
  type CNPGFleetRow,
} from '@skyhook-io/k8s-ui'
import { useCNPGClusterCapabilities, useCNPGOperator } from '../../../api/cnpg'
import { useRadarFeature } from '../../../api/client'
import { radarUpgradeRequirement } from '../../../api/radarFeatures'
import { useConnection } from '../../../context/ConnectionContext'
import { useCNPGFleet } from '../useCNPGSidebarWorkspace'
import { CNPGAttachArchiveDialog } from './CNPGAttachArchiveDialog'
import { CNPGCreateScheduleDialog } from './CNPGCreateScheduleDialog'
import { CNPGScheduleRepairDialog } from './CNPGScheduleRepairButton'
import { ClusterActionDialog } from '../actions/CNPGClusterActions'
import type { SelectedResource } from '../../../types'
import { cnpgResource } from '../shared'

export function CNPGProtectionSetup({
  namespace,
  name,
  onInspect,
  onOpenYaml,
  onOpenOperator,
  onOpenArchivingRepair,
}: {
  namespace: string
  name: string
  onInspect: (resource: SelectedResource) => void
  onOpenYaml?: () => void
  onOpenOperator?: () => void
  onOpenArchivingRepair?: () => void
}) {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const feature = useRadarFeature('cnpgProtectionSetup')
  const [open, setOpen] = useState(() => params.get('protectionSetup') === '1')
  const requested = params.get('protectionSetup') === '1'
  useEffect(() => {
    if (requested) setOpen(true)
  }, [requested])
  const close = () => {
    setOpen(false)
    if (params.has('protectionSetup')) {
      const next = new URLSearchParams(params)
      next.delete('protectionSetup')
      setParams(next, { replace: true, state: location.state })
    }
  }
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className="btn-brand px-3 py-1.5 text-xs">
        Set up backups…
      </button>
      {feature.support === 'unsupported' ? (
        <DialogPortal
          open={open}
          onClose={close}
          ariaLabel="Backup setup needs a newer Radar"
          className="w-full max-w-lg p-5"
        >
          <RadarUpgradeNote requirement={radarUpgradeRequirement('cnpgProtectionSetup', {})} />
          <button type="button" onClick={close} className="btn-secondary mt-4 px-3 py-1.5 text-sm">
            Close
          </button>
        </DialogPortal>
      ) : (
        open && (
          <ProtectionGuide
            namespace={namespace}
            name={name}
            onClose={close}
            onInspect={onInspect}
            onOpenYaml={onOpenYaml}
            onOpenOperator={onOpenOperator}
            onOpenArchivingRepair={onOpenArchivingRepair}
          />
        )
      )}
    </>
  )
}

function Milestone({
  number,
  title,
  state,
  children,
}: {
  number: number
  title: string
  state: string
  children: ReactNode
}) {
  return (
    <section className="space-y-2 rounded-lg border border-theme-border bg-theme-surface p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h4 className="font-medium">
          {number}. {title}
        </h4>
        <Badge severity="neutral" size="sm">
          {state}
        </Badge>
      </div>
      {children}
    </section>
  )
}

function ProtectionGuide({
  namespace,
  name,
  onClose,
  onInspect,
  onOpenYaml,
  onOpenOperator,
  onOpenArchivingRepair,
}: {
  namespace: string
  name: string
  onClose: () => void
  onInspect: (resource: SelectedResource) => void
  onOpenYaml?: () => void
  onOpenOperator?: () => void
  onOpenArchivingRepair?: () => void
}) {
  const titleId = useId()
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const { query, fleet } = useCNPGFleet([namespace])
  const operator = useCNPGOperator()
  const caps = useCNPGClusterCapabilities(namespace, name)
  const [step, setStep] = useState<'attach' | 'schedule' | 'backup' | null>(null)
  const [repair, setRepair] = useState<string | null>(null)
  const data = query.data
  const row: CNPGFleetRow | undefined = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const cluster = row?.cluster
  const plugin = cluster ? getCNPGClusterBarmanPlugin(cluster) : null
  const attached = !!plugin?.barmanObjectName && plugin.isWALArchiver
  const declaration = cluster ? cnpgBackupDeclaration(cluster) : null
  const unsupported =
    !!declaration &&
    (declaration.inTreeConfigured ||
      declaration.snapshotsConfigured ||
      declaration.plugins.some((p) => p.enabled && p.isWALArchiver && p.name !== 'barman-cloud.cloudnative-pg.io'))
  const pluginVisible = operator.data?.components.some(
    (component) => component.pluginName === 'barman-cloud.cloudnative-pg.io',
  )
  const pluginKnownAbsent = !pluginVisible && operator.data?.coverage.services.state === 'full'
  const stores = (data?.objects.objectStores ?? []).filter((store) => store.metadata.namespace === namespace)
  const schedules = (data?.objects.scheduledBackups ?? []).filter(
    (schedule) => schedule.metadata.namespace === namespace && schedule.spec?.cluster?.name === name,
  )
  const scheduleCoverage = data?.coverage.scheduledBackups
  const schedulesReadable = scheduleCoverage && coverageReadable(scheduleCoverage, namespace)
  const matching = schedules.filter(
    (schedule) =>
      !cnpgScheduleDestinationBlocker(schedule, [cluster]) &&
      schedule.spec?.method === 'plugin' &&
      schedule.spec?.pluginConfiguration?.name === 'barman-cloud.cloudnative-pg.io',
  )
  const repairable = schedules.filter(
    (schedule) =>
      cnpgScheduleDestinationBlocker(schedule, [cluster]) &&
      (schedule.spec?.method || 'barmanObjectStore') !== 'volumeSnapshot',
  )
  const changed = connection.context !== context
  const upgrade = getRadarUpgradeRequirement(query.error)
  if (step === 'attach' && cluster && !changed)
    return (
      <CNPGAttachArchiveDialog
        namespace={namespace}
        name={name}
        uid={cluster.metadata.uid}
        stores={stores}
        current={
          plugin?.barmanObjectName
            ? { objectStore: plugin.barmanObjectName, serverName: plugin.serverName || name }
            : undefined
        }
        onClose={() => setStep(null)}
      />
    )
  if (step === 'schedule' && !changed)
    return (
      <CNPGCreateScheduleDialog
        namespace={namespace}
        cluster={name}
        onClose={() => setStep(null)}
        onCreated={() => void query.refetch()}
      />
    )
  if (repair && !changed)
    return <CNPGScheduleRepairDialog namespace={namespace} name={repair} onClose={() => setRepair(null)} />
  if (step === 'backup' && caps.data && !changed)
    return (
      <ClusterActionDialog
        kind="backup"
        caps={caps.data}
        namespace={namespace}
        name={name}
        onClose={() => setStep(null)}
      />
    )
  return (
    <DialogPortal open onClose={onClose} ariaLabelledBy={titleId} className="w-full max-w-4xl">
      <div className="flex items-start gap-3 border-b border-theme-border p-4">
        <div className="min-w-0 flex-1">
          <h3 id={titleId} className="text-lg font-semibold text-theme-text-primary">
            Set up backups for {name}
          </h3>
          <p className="mt-0.5 text-xs text-theme-text-tertiary">
            Cluster{' '}
            <span className="font-mono">
              {namespace}/{name}
            </span>{' '}
            · context <span className="font-mono">{context}</span>
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover"
        >
          <X className="h-5 w-5" />
        </button>
      </div>
      <div className="max-h-[70vh] space-y-4 overflow-y-auto p-4 text-sm text-theme-text-primary">
        <p>
          Connect existing storage, establish a matching schedule, then verify the results. Each change has its own
          review; saving configuration alone does not prove protection.
        </p>
        {changed && (
          <AlertBanner
            variant="info"
            title="Context changed"
            message="Close this guide and start again in the current context."
          />
        )}
        {!row ? (
          <div>
            {upgrade ? (
              <RadarUpgradeNote requirement={upgrade} />
            ) : (
              <>
                <p>
                  {query.error
                    ? query.error.message
                    : query.isLoading
                      ? 'Reading Cluster backup configuration…'
                      : 'This Cluster is not readable in the workspace. Check its identity and access before setup.'}
                </p>
                <button
                  type="button"
                  className="text-accent-text hover:underline"
                  onClick={() => void query.refetch()}
                  disabled={query.isFetching}
                >
                  Retry
                </button>
              </>
            )}
          </div>
        ) : (
          <div className="space-y-3">
            {row.protection.walArchiving.state === 'failing' && <AlertBanner variant="error" title="Repair WAL archiving first">
              <p>Uploads are failing. Repair the archive connection before relying on a schedule or taking a new base backup.</p>
              {onOpenArchivingRepair && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => { onClose(); onOpenArchivingRepair() }}>Open archiving repair →</button>}
            </AlertBanner>}
            <Milestone number={1} title="Archive storage" state={row.protection.walArchiving.state === 'failing' ? 'Uploads failing' : attached ? 'Declared' : 'Not attached'}>
              {attached ? (
                <p className="text-sm text-theme-text-secondary">
                  ObjectStore <span className="font-mono">{plugin.barmanObjectName}</span> · archive server{' '}
                  <span className="font-mono">{plugin.serverName}</span>. Uploads still need verification.
                </p>
              ) : (
                <>
                  <p className="text-sm text-theme-text-secondary">
                    Attach an existing ObjectStore in {namespace} and reserve a distinct archive identity for this
                    Cluster.
                  </p>
                  <button
                    type="button"
                    className="btn-secondary px-3 py-1.5 text-xs"
                    onClick={() => setStep('attach')}
                    disabled={unsupported || pluginKnownAbsent || changed}
                  >
                    Choose existing ObjectStore…
                  </button>
                </>
              )}
              {unsupported && (
                <p className="text-xs text-theme-text-secondary">
                  This configuration needs a backup-method migration. Review it in Cluster YAML; the guide preserves
                  existing in-tree, snapshot and other-plugin arrangements.
                </p>
              )}
              {pluginKnownAbsent && (
                <p className="text-xs text-theme-text-secondary">
                  No Barman plugin Service was found in the readable operator scope. Install it before attaching
                  storage.
                </p>
              )}
              {!operator.data && (
                <p className="text-xs text-theme-text-tertiary">
                  Plugin installation could not yet be established. Server review checks the declaration; it cannot
                  verify upload readiness.
                </p>
              )}
              {onOpenOperator && (
                <button
                  type="button"
                  className="text-xs text-accent-text hover:underline"
                  onClick={() => {
                    onClose()
                    onOpenOperator()
                  }}
                >
                  Operator & plugin prerequisites →
                </button>
              )}
              {onOpenYaml && (
                <button
                  type="button"
                  className="ml-3 text-xs text-accent-text hover:underline"
                  onClick={() => {
                    onClose()
                    onOpenYaml()
                  }}
                >
                  Cluster YAML →
                </button>
              )}
            </Milestone>
            <Milestone
              number={2}
              title="Backup schedule"
              state={
                !schedulesReadable ? 'Inventory incomplete' : matching.length ? 'Matching declaration' : 'Needs setup'
              }
            >
              {matching.map((schedule) => (
                <p key={schedule.metadata.name} className="text-sm text-theme-text-secondary">
                  <button
                    type="button"
                    className="text-accent-text hover:underline"
                    onClick={() => {
                      onClose()
                      onInspect(cnpgResource('ScheduledBackup', namespace, schedule.metadata.name))
                    }}
                  >
                    {schedule.metadata.name} →
                  </button>{' '}
                  · {schedule.spec.schedule}
                  {schedule.spec.suspend ? ' · suspended: open the schedule to resume it' : ''}
                </p>
              ))}
              {repairable.map((schedule) => (
                <div key={schedule.metadata.name} className="flex flex-wrap items-center gap-2 text-sm">
                  <span>{schedule.metadata.name} cannot use the declared destination.</span>
                  <button
                    type="button"
                    className="btn-secondary px-3 py-1.5 text-xs"
                    onClick={() => setRepair(schedule.metadata.name)}
                    disabled={!attached || changed}
                  >
                    Review method repair…
                  </button>
                </div>
              ))}
              {!matching.length && (
                <button
                  type="button"
                  className="btn-secondary px-3 py-1.5 text-xs"
                  onClick={() => setStep('schedule')}
                  disabled={!attached || changed}
                >
                  Create matching schedule…
                </button>
              )}
              {!attached && (
                <p className="text-xs text-theme-text-tertiary">
                  Attach the Barman archive destination first. Existing schedules are left unchanged.
                </p>
              )}
              {!schedulesReadable && (
                <p className="text-xs text-theme-text-tertiary">
                  Schedules may exist outside the readable inventory. Check before creating another; strict creation
                  will not replace an existing object.
                </p>
              )}
            </Milestone>
            <Milestone number={3} title="Verify protection" state="Observed evidence">
              <FactGrid>
                <FactRow label="WAL archiving">
                  <FactValue fact={row.protection.walArchiving} />
                </FactRow>
                <FactRow label="Last successful backup">
                  <FactValue fact={row.protection.lastSuccessfulBackup} />
                </FactRow>
              </FactGrid>
              <p className="text-xs text-theme-text-secondary">
                Confirm uploads resumed, then complete a new base backup for this configuration. Older successful
                backups do not prove a new attachment works. Restore validation is a separate task.
              </p>
              <button
                type="button"
                className="btn-secondary px-3 py-1.5 text-xs"
                onClick={() => setStep('backup')}
                disabled={!caps.data?.actions.backup.allowed || changed}
              >
                Back up now…
              </button>
              {!caps.data?.actions.backup.allowed && (
                <p className="text-xs text-theme-text-tertiary">
                  {caps.data?.actions.backup.reason ||
                    (caps.error ? caps.error.message : 'Checking backup permission and Cluster state…')}
                </p>
              )}
              <button
                type="button"
                className="ml-3 text-xs text-accent-text hover:underline"
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
              >
                Refresh evidence
              </button>
            </Milestone>
          </div>
        )}
      </div>
      <div className="flex justify-end border-t border-theme-border p-4">
        <button type="button" onClick={onClose} className="btn-secondary px-4 py-2 text-sm">
          Close guide
        </button>
      </div>
    </DialogPortal>
  )
}
