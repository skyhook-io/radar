import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { ChevronDown, DatabaseBackup, MoreHorizontal, Repeat } from 'lucide-react'
import { clsx } from 'clsx'
import { ActionConfirmDialog, Tooltip, cnpgPDBFact, cnpgQuorumFact, toneTextClass, type ActionWrite, type CNPGClusterHA, type CNPGProblem } from '@skyhook-io/k8s-ui'
import { useCNPGAction, useCNPGClusterCapabilities, useCNPGRuntime, type CNPGActionResult, type CNPGBackupMethod, type CNPGClusterActionName, type CNPGClusterCapabilities } from '../../../api/cnpg'
import { actionOutcomeLocked, type ActionCapability, capabilityReason } from '../../../api/actions'
import { useToast } from '../../ui/Toast'
import { useAnimatedUnmount } from '../../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../../utils/animation'
import { useCNPGClusterHA } from '../../../api/cnpg-ha'
import { trackCNPGOperation, type TrackCNPGOperationInput } from '../operations/store'
import { useCNPGWriteGuard, type CNPGWriteScope } from './useCNPGWriteGuard'
import { cnpgOperatorActionNote } from '../operatorStatus'
import { CNPGRestoreDialog } from '../recovery/CNPGRestoreDialog'
import { CNPGReportDialog } from './CNPGReportDialog'
import { useOpenCNPGPsql } from './useOpenCNPGPsql'
import {
  backupNameFor,
  describeBackupMethod,
  pickDefaultStandby,
  switchoverCandidateFacts,
  switchoverConcernWarning,
  switchoverConcerns,
  switchoverDefault,
  switchoverLagNote,
  type StandbyChoice,
} from './actionModel'
import { useCNPGFleet } from '../useCNPGSidebarWorkspace'
import { RefreshFailedNotice } from '../../workspace/layout'
import { assessRestoreSources } from '../recovery/restoreModel'
import { lsnDistance, standbyOwnBacklog } from '../lsn'

type DialogKind = CNPGClusterActionName | 'restore' | 'report' | null

const MENU_ITEM = 'block w-full px-3 py-1.5 text-left text-sm text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:text-theme-text-disabled'

const RESTART_EFFECT: Record<string, string> = {
  recreate: 'Pod recreated',
  skipped_fenced: 'skipped (fenced)',
  switchover: 'switchover to an updated standby, then recreated',
  restart: 'restarted in place',
  wait_for_user: 'waits for you to promote or restart it (supervised)',
  restart_only_instance: 'restarted (only instance: downtime)',
}

function EffectItem({ list, label }: { list: { available: boolean; reason?: string; names: string[] }; label: string }) {
  if (!list.available) return <li>{label}: not known{list.reason ? ` (${list.reason})` : ''}</li>
  if (list.names.length === 0) return null
  return <li>{label}: {list.names.join(', ')}</li>
}

function capabilityTitle(cap: ActionCapability | undefined): string | undefined {
  return cap ? capabilityReason(cap) : undefined
}

/**
 * Operations on a CloudNativePG Cluster. Every button reflects the server's
 * capability answer (RBAC plus the cluster's state) and says why it is
 * disabled; every write goes through a confirmation that leads with the
 * effect, lists the literal API writes, and names GitOps overwrite risk.
 */
export function CNPGClusterActions({ namespace, name, compact = false }: { namespace: string; name: string; compact?: boolean }) {
  const caps = useCNPGClusterCapabilities(namespace, name)
  const [open, setOpen] = useState<DialogKind>(null)
  const [menu, setMenu] = useState(false)
  const [blockedAction, setBlockedAction] = useState<'backup' | 'switchover'>()
  const menuPresence = useAnimatedUnmount(menu, overlayExitMs('menu'))
  useEffect(() => {
    if (!menu) return
    // Capture phase, consumed: the page's own Escape (leave the full view)
    // must not also fire when Escape only closes this menu.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation()
      setMenu(false)
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [menu])
  const { query: workspace, fleet } = useCNPGFleet([namespace])
  const restoreSourceReason = assessRestoreSources(workspace.data, namespace, fleet?.rows.find((r) => r.name === name && r.namespace === namespace)?.cluster).disabledReason
  const actions = caps.data?.actions
  const openPsql = useOpenCNPGPsql()
  const unavailable = caps.data
    ? undefined
    : caps.error
      ? `Actions unavailable: what you may do could not be checked (${caps.error instanceof Error ? caps.error.message : 'unknown error'})`
      : 'Checking what you may do…'
  const blockedReason = blockedAction && !actions?.[blockedAction]?.allowed ? unavailable ?? capabilityTitle(actions?.[blockedAction]) : undefined

  const item = (id: CNPGClusterActionName, label: string) => {
    const cap = actions?.[id]
    const title = unavailable ?? capabilityTitle(cap)
    return (
      <Tooltip key={id} content={title} position="left" wrapperClassName="w-full">
        <button
          type="button"
          role="menuitem"
          disabled={!cap?.allowed}
          onClick={() => {
            setMenu(false)
            setOpen(id)
          }}
          className={MENU_ITEM}
        >
          {label}
          {!cap?.allowed && <span className="mt-0.5 block text-xs text-theme-text-secondary">{title}</span>}
        </button>
      </Tooltip>
    )
  }

  const hibernated = !!caps.data?.facts.hibernated

  return (
    <div className="relative flex flex-wrap items-center gap-1.5">
      <Tooltip content={unavailable ?? capabilityTitle(actions?.backup) ?? 'Create an on-demand Backup'} position="bottom">
        <button
          type="button"
          aria-disabled={!actions?.backup.allowed}
          aria-label="Back up now"
          onClick={() => { if (actions?.backup.allowed) { setBlockedAction(undefined); setOpen('backup') } else setBlockedAction('backup') }}
          className={clsx('btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium', !actions?.backup.allowed && 'cursor-not-allowed opacity-50')}
        >
          <DatabaseBackup className="h-3.5 w-3.5" />
          {!compact && 'Back up now'}
        </button>
      </Tooltip>
      {!compact && (
        <Tooltip content={unavailable ?? capabilityTitle(actions?.switchover) ?? 'Promote a standby to primary'} position="bottom">
          <button
            type="button"
            aria-disabled={!actions?.switchover.allowed}
            onClick={() => { if (actions?.switchover.allowed) { setBlockedAction(undefined); setOpen('switchover') } else setBlockedAction('switchover') }}
            className={clsx('btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium', !actions?.switchover.allowed && 'cursor-not-allowed opacity-50')}
          >
            <Repeat className="h-3.5 w-3.5" />
            Switchover
          </button>
        </Tooltip>
      )}
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={menu}
        aria-label="More cluster actions"
        onClick={() => { setBlockedAction(undefined); setMenu((v) => !v) }}
        className="btn-secondary inline-flex items-center gap-0.5 px-2 py-1.5 text-xs"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
        <ChevronDown className="h-3 w-3" />
      </button>
      {blockedReason && <div role="status" className="basis-full text-xs text-theme-text-secondary">{blockedReason}</div>}
      {menu && <div className="fixed inset-0 z-40" onClick={() => setMenu(false)} aria-hidden />}
      {menuPresence.shouldRender && (
        <>
          <div
            role="menu"
            inert={!menu || undefined}
            className={`absolute right-0 top-full z-50 mt-1 w-56 origin-top-right overflow-hidden rounded-lg border border-theme-border bg-theme-surface py-1 shadow-theme-lg ${TRANSITION_MENU} ${
              menuPresence.isOpen ? 'translate-y-0 scale-100 opacity-100' : '-translate-y-1 scale-[0.97] opacity-0'
            } ${menu ? '' : 'pointer-events-none'}`}
            style={overlayTransitionStyle(menuPresence.isOpen, 'menu')}
          >
            {compact && item('switchover', 'Switchover…')}
            {item('restart', 'Restart instances…')}
            {item('reload', 'Reload configuration…')}
            {item('fence', 'Fence instances…')}
            {item('unfence', 'Lift fencing…')}
            {hibernated ? item('rehydrate', 'Resume from hibernation…') : item('hibernate', 'Hibernate…')}
            <div className="my-1 border-t border-theme-border" />
            <Tooltip content={unavailable ?? capabilityTitle(actions?.psql)} position="left" wrapperClassName="w-full">
              <button
                type="button"
                role="menuitem"
                disabled={!actions?.psql.allowed || !caps.data?.facts.currentPrimary}
                onClick={() => {
                  setMenu(false)
                  if (caps.data?.facts.currentPrimary) openPsql(namespace, caps.data.facts.currentPrimary, true)
                }}
                className={MENU_ITEM}
              >
                Open psql on the primary
                {(!actions?.psql.allowed || !caps.data?.facts.currentPrimary) && <span className="mt-0.5 block text-xs text-theme-text-secondary">{unavailable ?? capabilityTitle(actions?.psql) ?? 'No primary instance is reported'}</span>}
              </button>
            </Tooltip>
            <div className="px-3 pb-0.5 pt-1 text-[11px] uppercase tracking-wide text-theme-text-tertiary">Advanced</div>
            {caps.data?.facts.maintenance.inProgress ? item('unsetMaintenance', 'Lift node maintenance…') : item('setMaintenance', 'Set node maintenance…')}
            <div className="my-1 border-t border-theme-border" />
            <Tooltip content={unavailable ?? capabilityTitle(actions?.restore) ?? restoreSourceReason} position="left" wrapperClassName="w-full">
              <button
                type="button"
                role="menuitem"
                disabled={!actions?.restore.allowed || !!restoreSourceReason}
                className={MENU_ITEM}
                onClick={() => {
                  setMenu(false)
                  setOpen('restore')
                }}
              >
                Restore to a new cluster…
              </button>
            </Tooltip>
            {(!actions?.restore.allowed || restoreSourceReason) && <div className="px-3 py-1 text-xs text-theme-text-secondary">{unavailable ?? capabilityTitle(actions?.restore) ?? restoreSourceReason}</div>}
            <button type="button" role="menuitem" className={MENU_ITEM} onClick={() => { setMenu(false); setOpen('report') }}>
              Download report…
            </button>
          </div>
        </>
      )}
      <RefreshFailedNotice queries={[caps]} />
      {caps.error && !caps.data && (
        <div role="status" className="text-xs text-theme-text-secondary">
          Actions could not be checked: {caps.error instanceof Error ? caps.error.message : 'unknown error'}.{' '}
          <button type="button" onClick={() => void caps.refetch()} disabled={caps.isFetching} className="text-accent-text hover:underline">Retry</button>
        </div>
      )}
      {caps.data && open && open !== 'restore' && open !== 'report' && (
        <ClusterActionDialog kind={open} caps={caps.data} namespace={namespace} name={name} onClose={() => setOpen(null)} />
      )}
      {open === 'restore' && <CNPGRestoreDialog namespace={namespace} entry={{ kind: 'cluster', name }} onClose={() => setOpen(null)} />}
      {open === 'report' && <CNPGReportDialog namespace={namespace} name={name} onClose={() => setOpen(null)} />}
    </div>
  )
}

const UNSUPPORTED = {
  title: 'Unsupported action',
  confirmLabel: 'Close',
  effect: '',
  writes: [],
  scope: { kind: 'status' as const },
  success: () => '',
  invalid: 'Restart a single instance from its row in the Replication tab.',
}

interface DialogSpec {
  title: string
  confirmLabel: string
  effect: ReactNode
  body?: ReactNode
  notes?: ReactNode[]
  warnings?: ReactNode[]
  writes: ActionWrite[]
  scope: CNPGWriteScope
  typed?: boolean
  disruptive?: boolean
  params?: Record<string, unknown>
  success: (result: { backup?: string }) => string
  invalid?: string
  /** What the form still needs; disables confirm without an alert. */
  incomplete?: string
}

type TrackedSpec = Omit<TrackCNPGOperationInput, 'context' | 'namespace' | 'cluster' | 'clusterUID'>

/** What to follow after the server accepted `kind`: the target and the baseline to compare against. */
function trackedOperationFor(
  kind: CNPGClusterActionName,
  x: {
    result: CNPGActionResult
    facts: CNPGClusterCapabilities['facts']
    switchTarget?: string
    chosenPodUID?: string
    fenceSel: string
    fenced: string[]
    instance?: CNPGClusterCapabilities['facts']['instances'][number]
  },
): TrackedSpec | null {
  const pods = x.facts.instances.map((i) => i.pod)
  const podUIDs = Object.fromEntries(x.facts.instances.filter((i) => i.podUID).map((i) => [i.pod, i.podUID]))
  switch (kind) {
    case 'backup':
      return x.result.backup
        ? { kind, label: `Backup ${x.result.backup}`, target: { name: x.result.backup }, link: { kind: 'Backup', group: 'postgresql.cnpg.io', name: x.result.backup } }
        : null
    case 'switchover':
      return x.switchTarget
        ? { kind, label: `Switchover to ${x.switchTarget}`, target: { name: x.switchTarget, uid: x.chosenPodUID }, baseline: { currentPrimary: x.facts.currentPrimary } }
        : null
    case 'restart':
      return { kind, label: 'Rolling restart', baseline: { instances: pods, podUIDs } }
    case 'restartInstance':
      return x.instance
        ? { kind, label: `Restart ${x.instance.pod}`, target: { name: x.instance.pod, uid: x.instance.podUID }, baseline: { podUIDs: { [x.instance.pod]: x.instance.podUID } } }
        : null
    case 'reload':
      return { kind, label: 'Configuration reload' }
    case 'fence':
      return { kind, label: x.fenceSel === '*' ? 'Fence all instances' : `Fence ${x.fenceSel}`, baseline: { instances: x.fenceSel === '*' ? pods : [x.fenceSel] } }
    case 'unfence': {
      const lifted = x.fenceSel === '*' ? (x.fenced.includes('*') ? pods : x.fenced) : [x.fenceSel]
      return { kind, label: x.fenceSel === '*' ? 'Lift all fencing' : `Lift fence on ${x.fenceSel}`, baseline: { instances: lifted } }
    }
    case 'hibernate':
      return { kind, label: 'Hibernate' }
    case 'rehydrate':
      return { kind, label: 'Resume from hibernation' }
    case 'setMaintenance':
      return { kind, label: 'Set node maintenance' }
    case 'unsetMaintenance':
      return { kind, label: 'Lift node maintenance' }
    default:
      return null
  }
}

/** Sync requirements, quorum and disruption budgets, as they stand before a switchover. */
function SwitchoverContext({ ha, haLoading, target }: { ha?: CNPGClusterHA; haLoading: boolean; target?: string }) {
  if (!ha) {
    return <div className="mt-2 text-xs text-theme-text-tertiary">{haLoading ? 'Reading quorum and disruption budgets…' : 'Quorum and disruption budgets could not be read.'}</div>
  }
  const q = ha.quorum
  const quorum = cnpgQuorumFact(q)
  const pdb = cnpgPDBFact(ha.pdbs)
  return (
    <div className="mt-3 space-y-1 rounded-md border border-theme-border bg-theme-base p-2 text-xs text-theme-text-secondary">
      <div>
        <span className="text-theme-text-tertiary">Synchronous replication: </span>
        {q.method || q.number !== undefined
          ? `${(q.method ?? '').toUpperCase()} ${q.number ?? ''}${q.dataDurability ? ` · dataDurability ${q.dataDurability}` : ''}`.trim()
          : 'not configured (asynchronous)'}
      </div>
      <div>
        <span className="text-theme-text-tertiary">Failover quorum: </span>
        {quorum.text}
        {q.enabled && q.promotable && target ? (q.promotable.includes(target) ? ` · ${target} is promotable` : ` · ${target} is not counted as promotable`) : ''}
      </div>
      <div>
        <span className="text-theme-text-tertiary">Disruption budgets: </span>
        {pdb.text}
      </div>
      <div className="text-theme-text-tertiary">
        The operator restarts the old primary itself rather than evicting it, so disruption budgets do not gate the switchover; the primary’s budget follows the new
        primary for later node drains.
      </div>
    </div>
  )
}

export function ClusterActionDialog({
  kind,
  caps,
  namespace,
  name,
  onClose,
  initialPod,
}: {
  kind: CNPGClusterActionName
  caps: CNPGClusterCapabilities
  namespace: string
  name: string
  onClose: () => void
  initialPod?: string
}) {
  const facts = caps.facts
  const cap = caps.actions[kind]
  const mutation = useCNPGAction('clusters', namespace, name)
  const { showSuccess } = useToast()
  const { fleet } = useCNPGFleet([namespace])
  const runtime = useCNPGRuntime(namespace, name, kind === 'switchover')
  const ha = useCNPGClusterHA(namespace, name, { enabled: kind === 'switchover' || kind === 'setMaintenance' || kind === 'unsetMaintenance', refetchInterval: false })
  const [reusePVC, setReusePVC] = useState(() => facts.maintenance.reusePVC)

  const backupMethods = facts.backupMethods.filter((m) => m.capability !== 'none')
  const [method, setMethod] = useState<CNPGBackupMethod | undefined>(backupMethods[0])
  const [target, setTarget] = useState<'' | 'primary' | 'prefer-standby'>('')
  const [backupName, setBackupName] = useState(() => backupNameFor(name))

  const standbys: StandbyChoice[] = useMemo(() => {
    const lags = new Map<string, { replayLagSeconds?: number; replayBacklogBytes?: number; state?: string; syncState?: string }>()
    const primary = runtime.data?.instances.find((i) => i.role === 'primary')
    for (const r of primary?.status.replication ?? []) {
      lags.set(r.applicationName, {
        replayLagSeconds: r.replayLag,
        replayBacklogBytes: lsnDistance(primary?.status.currentLsn, r.replayLsn),
        state: r.state,
        syncState: r.syncState,
      })
    }
    return facts.instances
      .filter((i) => i.pod !== facts.currentPrimary)
      .map((i) => {
        // A standby that isn't connected has no row; its own replayed
        // position still says how far behind it is.
        const own = standbyOwnBacklog(primary?.status, runtime.data?.instances.find((x) => x.pod === i.pod)?.status)
        const lag = lags.get(i.pod) ?? (own !== undefined ? { replayBacklogBytes: own } : undefined)
        const cap = caps.instanceActions?.[i.pod]?.switchoverTarget
        const ineligible = i.fenced ? 'fenced' : !i.podExists ? 'Pod missing' : !i.ready ? 'not ready' : cap && !cap.allowed ? cap.reason ?? 'not eligible' : undefined
        const concerns = switchoverConcerns(primary?.status, runtime.data?.instances.find((x) => x.pod === i.pod))
        return { pod: i.pod, podUID: i.podUID, ineligible, ...lag, concerns }
      })
  }, [facts, runtime.data, caps.instanceActions])
  const [switchTarget, setSwitchTarget] = useState<string | undefined>(() => initialPod ?? pickDefaultStandby(standbys)?.pod)
  const [switchTouched, setSwitchTouched] = useState(false)
  // Lag and backlog arrive with the runtime read, often after the dialog
  // opened: re-pick the default until the user chooses for themselves.
  useEffect(() => {
    const next = switchoverDefault({ touched: switchTouched || !!initialPod, current: switchTarget, standbys })
    if (next !== switchTarget) setSwitchTarget(next)
  }, [standbys, switchTouched, initialPod]) // eslint-disable-line react-hooks/exhaustive-deps
  const chosenStandby = standbys.find((s) => s.pod === switchTarget)

  const fenced = facts.fencedInstances.all ? ['*'] : facts.fencedInstances.instances
  const fencingMalformed = !!facts.fencedInstances.malformed
  const allFenced = facts.fencedInstances.all
  const [fenceSel, setFenceSel] = useState<string>(initialPod ?? '*')
  const instance = facts.instances.find((i) => i.pod === initialPod)
  const instanceIsPrimary = !!initialPod && initialPod === facts.currentPrimary

  const spec: DialogSpec = (() => {
    switch (kind) {
      case 'backup':
        return {
          title: `Back up ${name} now?`,
          confirmLabel: 'Create Backup',
          effect: (
            <>
              Creates Backup <span className="font-mono">{backupName}</span>. The operator runs it on an eligible instance; a backup that is already running
              makes this one wait.
            </>
          ),
          body: (
            <div className="grid grid-cols-[7rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
              <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-backup-method">Method</label>
              <select
                id="cnpg-backup-method"
                value={method ? `${method.method}:${method.pluginName ?? ''}` : ''}
                onChange={(e) => setMethod(backupMethods.find((m) => `${m.method}:${m.pluginName ?? ''}` === e.target.value))}
                className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm"
              >
                {backupMethods.map((m) => (
                  <option key={`${m.method}:${m.pluginName ?? ''}`} value={`${m.method}:${m.pluginName ?? ''}`}>
                    {describeBackupMethod(m)}
                  </option>
                ))}
              </select>
              <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-backup-target">Target</label>
              <select id="cnpg-backup-target" value={target} onChange={(e) => setTarget(e.target.value as typeof target)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm">
                <option value="">{facts.backupTarget ? `Cluster default (${facts.backupTarget})` : 'Cluster default'}</option>
                <option value="prefer-standby">Prefer a standby</option>
                <option value="primary">Primary</option>
              </select>
              <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-backup-name">Name</label>
              <input id="cnpg-backup-name" value={backupName} onChange={(e) => setBackupName(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 font-mono text-sm" />
            </div>
          ),
          // A volume snapshot can be recovered without archived WAL, so only
          // the object-store methods are warned.
          warnings:
            facts.archivingFailing && method?.method === 'barmanObjectStore'
              ? ['WAL archiving is failing. The operator checks archiving before a Barman backup and marks this Backup walArchivingFailing instead of taking it: fix archiving first.']
              : facts.archivingFailing && method?.method === 'plugin'
                ? ['WAL archiving is failing. A base backup in the object store can only be restored once the WAL written during it reaches the archive, so fix archiving first.']
                : [],
          notes: [
            method?.capability === 'unknown' ? `The plugin ${method.pluginName} does not report whether it can take backups; the operator will reject the Backup if it cannot.` : null,
            method?.method === 'barmanObjectStore' ? 'The in-tree Barman object store is deprecated in favour of the barman-cloud plugin.' : null,
            'A Backup’s spec cannot be edited once it is created.',
          ].filter(Boolean) as string[],
          writes: [
            {
              summary: `create Backup ${namespace}/${backupName}`,
              detail: JSON.stringify(
                {
                  spec: {
                    cluster: { name },
                    method: method?.method,
                    ...(method?.pluginName ? { pluginConfiguration: { name: method.pluginName } } : {}),
                    ...(target ? { target } : {}),
                  },
                },
                null,
                2,
              ),
            },
          ],
          scope: { kind: 'create-child' },
          params: { method: method?.method, pluginName: method?.pluginName, target: target || undefined, name: backupName },
          invalid: !method ? 'This cluster declares no backup method.' : undefined,
          incomplete: !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(backupName) ? 'The name must be a valid Kubernetes object name.' : undefined,
          success: (r) => `Backup ${r.backup ?? backupName} requested. The operator does the rest.`,
        }
      case 'switchover':
        return {
          title: `Switch over ${name}?`,
          confirmLabel: 'Switch over',
          effect: (
            <>
              Promotes <span className="font-mono">{switchTarget ?? '—'}</span> to primary. The current primary <span className="font-mono">{facts.currentPrimary}</span> shuts
              down and returns as a standby; applications lose their connection while the primary changes.
            </>
          ),
          body: (
            <fieldset className="space-y-1.5">
              <legend className="mb-1 text-xs text-theme-text-secondary">New primary</legend>
              {standbys.length === 0 && <div className="text-sm text-theme-text-tertiary">No standby instances.</div>}
              {standbys.map((s) => {
                const behind = switchoverLagNote(s)
                return (
                  <div key={s.pod}>
                    <label className="flex items-center gap-2 text-sm">
                      <input type="radio" name="cnpg-switch" value={s.pod} disabled={!!s.ineligible} checked={switchTarget === s.pod} onChange={() => {
                          setSwitchTouched(true)
                          setSwitchTarget(s.pod)
                        }}
                      />
                      <span className="font-mono">{s.pod}</span>
                      <span className={clsx('text-xs', !s.ineligible && s.concerns?.length ? toneTextClass('degraded') : 'text-theme-text-tertiary')}>
                        {s.ineligible ??
                          (s.concerns?.length
                            ? s.concerns.join(' · ')
                            : [s.state, s.syncState, ...(s.replayLagSeconds !== undefined || s.replayBacklogBytes !== undefined ? switchoverCandidateFacts(s) : [runtime.isLoading ? 'lag loading…' : 'lag unknown'])]
                                .filter(Boolean)
                                .join(' · '))}
                      </span>
                    </label>
                    {behind && switchTarget === s.pod && <div className={clsx('ml-6 mt-0.5 text-xs', toneTextClass('degraded'))}>{behind}</div>}
                  </div>
                )
              })}
              <SwitchoverContext ha={ha.data} haLoading={ha.isLoading} target={switchTarget} />
            </fieldset>
          ),
          warnings: [
            ha.data?.quorum.enabled && ha.data.quorum.holds === false
              ? 'The failover quorum does not hold right now (R + W ≤ N): an automatic failover would wait. A switchover you request still proceeds.'
              : null,
            ha.data?.quorum.enabled && switchTarget && ha.data.quorum.status && ha.data.quorum.status.standbyNames.length > 0 && !ha.data.quorum.status.standbyNames.includes(switchTarget)
              ? `${switchTarget} is not among the recorded potentially synchronous standbys (${ha.data.quorum.status.standbyNames.join(', ')}).`
              : null,
            chosenStandby && chosenStandby.concerns?.length
              ? switchoverConcernWarning(chosenStandby.pod, chosenStandby.concerns)
              : chosenStandby && chosenStandby.replayLagSeconds === undefined && chosenStandby.replayBacklogBytes === undefined
                ? runtime.data && runtime.data.permission.proxy !== 'denied'
                  ? 'How far behind this standby is was not reported.'
                  : 'How far behind this standby is is unknown: live instance data is not readable.'
                : null,
          ].filter(Boolean) as string[],
          notes: ['The operator performs a controlled shutdown of the old primary; its duration depends on spec.switchoverDelay and open transactions.'],
          writes: [
            {
              summary: `patch Cluster ${namespace}/${name} status (subresource)`,
              detail: `status.targetPrimary = ${switchTarget}\nstatus.phase = "Switchover in progress"\nstatus.phaseReason = "Switching over to ${switchTarget}"`,
            },
          ],
          scope: { kind: 'status' },
          typed: true,
          disruptive: true,
          params: { target: switchTarget, targetPodUID: chosenStandby?.podUID },
          incomplete: !switchTarget || chosenStandby?.ineligible ? 'Pick an eligible standby.' : undefined,
          success: () => `Switchover to ${switchTarget} requested.`,
        }
      case 'restart':
        return {
          title: `Restart ${name}?`,
          confirmLabel: 'Restart instances',
          effect: 'Restarts every instance with a rolling update: standbys first, then the primary.',
          body: <CNPGRestartReview caps={caps} problems={fleet?.rows.find((r) => r.namespace === namespace && r.name === name)?.problems} />,
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: 'metadata.annotations["kubectl.kubernetes.io/restartedAt"] = <now, RFC 3339>' }],
          scope: { kind: 'metadata', paths: ['metadata.annotations["kubectl.kubernetes.io/restartedAt"]'] },
          typed: true,
          disruptive: true,
          success: () => 'Restart requested. Instances restart one at a time.',
        }
      case 'reload':
        return {
          title: `Reload ${name}’s configuration?`,
          confirmLabel: 'Reload',
          effect: 'Asks every instance to reload its PostgreSQL configuration and certificates without restarting.',
          notes: ['Nothing reports when a reload completes.'],
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: 'metadata.annotations["cnpg.io/reloadedAt"] = <now>' }],
          scope: { kind: 'metadata', paths: ['metadata.annotations["cnpg.io/reloadedAt"]'] },
          success: () => 'Reload requested.',
        }
      case 'fence': {
        const choices = ['*', ...facts.instances.map((i) => i.pod).filter((p) => !fenced.includes(p))]
        const next = fenceSel === '*' ? ['*'] : [...fenced.filter((f) => f !== '*'), fenceSel]
        const fencesPrimary = fenceSel === '*' || fenceSel === facts.currentPrimary
        return {
          title: fenceSel === '*' ? `Fence every instance of ${name}?` : `Fence ${fenceSel}?`,
          confirmLabel: 'Fence',
          effect: fencesPrimary
            ? 'Stops PostgreSQL on the fenced instances while keeping their Pods and volumes. Writes stop and no failover happens while the primary is fenced.'
            : 'Stops PostgreSQL on this standby while keeping its Pod and volume for investigation.',
          body: (
            <label className="flex items-center gap-2 text-sm">
              <span className="text-xs text-theme-text-secondary">Instance</span>
              <select value={fenceSel} onChange={(e) => setFenceSel(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm">
                {choices.map((c) => (
                  <option key={c} value={c}>{c === '*' ? 'All instances' : c}{c === facts.currentPrimary ? ' (primary)' : ''}</option>
                ))}
              </select>
            </label>
          ),
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: `metadata.annotations["cnpg.io/fencedInstances"] = ${JSON.stringify(JSON.stringify(next))}` }],
          scope: { kind: 'metadata', paths: ['metadata.annotations["cnpg.io/fencedInstances"]'] },
          typed: fencesPrimary,
          disruptive: fencesPrimary,
          params: { instances: fenceSel === '*' ? '*' : [fenceSel] },
          invalid: fencingMalformed ? 'The cnpg.io/fencedInstances annotation is not valid JSON; fix it in YAML first.' : undefined,
          success: () => 'Fencing requested.',
        }
      }
      case 'unfence': {
        const choices = allFenced ? ['*'] : ['*', ...fenced]
        return {
          title: `Lift fencing on ${name}?`,
          confirmLabel: 'Lift fencing',
          effect: 'Starts PostgreSQL again on the selected fenced instances. An instance fenced on purpose (for example during an investigation) rejoins the cluster.',
          body: (
            <label className="flex items-center gap-2 text-sm">
              <span className="text-xs text-theme-text-secondary">Instance</span>
              <select value={fenceSel} onChange={(e) => setFenceSel(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm">
                {choices.map((c) => <option key={c} value={c}>{c === '*' ? 'All fenced instances' : c}</option>)}
              </select>
            </label>
          ),
          notes: allFenced ? ['The whole cluster is fenced ("*"); fencing can only be lifted for every instance at once here.'] : [],
          writes: [
            {
              summary: `patch Cluster ${namespace}/${name}`,
              detail: fenceSel === '*' ? 'metadata.annotations["cnpg.io/fencedInstances"] removed' : `metadata.annotations["cnpg.io/fencedInstances"] = ${JSON.stringify(JSON.stringify(fenced.filter((f) => f !== fenceSel)))}`,
            },
          ],
          scope: { kind: 'metadata', paths: ['metadata.annotations["cnpg.io/fencedInstances"]'] },
          typed: true,
          disruptive: true,
          params: { instances: fenceSel === '*' ? '*' : [fenceSel] },
          invalid: fencingMalformed ? 'The cnpg.io/fencedInstances annotation is not valid JSON; fix it in YAML first.' : fenced.length === 0 ? 'No instance is fenced.' : undefined,
          success: () => 'Fencing lifted.',
        }
      }
      case 'hibernate': {
        const fx = caps.hibernateEffects
        return {
          title: `Hibernate ${name}?`,
          confirmLabel: 'Hibernate',
          effect: 'Shuts down every instance and deletes their Pods, primary first. Volumes are kept so the cluster can resume where it stopped.',
          body: fx ? (
            <ul className="list-disc space-y-1 pl-5 text-xs text-theme-text-secondary">
              <EffectItem list={{ available: fx.volumes.available, reason: fx.volumes.reason, names: fx.volumes.items.map((v) => `${v.name}${v.capacity ? ` (${v.capacity})` : ''}`) }} label="Kept volumes" />
              <EffectItem list={fx.poolers} label="Poolers that lose their backend" />
              <EffectItem list={fx.unsuspendedScheduledBackups} label="Schedules still active (each run will fail until it resumes)" />
              <EffectItem list={{ available: fx.databases.available && fx.publications.available && fx.subscriptions.available, names: [...fx.databases.names, ...fx.publications.names, ...fx.subscriptions.names] }} label="Declarations that stop reconciling" />
            </ul>
          ) : undefined,
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: 'metadata.annotations["cnpg.io/hibernation"] = "on"' }],
          scope: { kind: 'metadata', paths: ['metadata.annotations["cnpg.io/hibernation"]'] },
          typed: true,
          disruptive: true,
          success: () => 'Hibernation requested. Pods shut down in order.',
        }
      }
      case 'rehydrate':
        return {
          title: `Resume ${name} from hibernation?`,
          confirmLabel: 'Resume',
          effect: 'Recreates the instance Pods on the kept volumes, primary first.',
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: 'metadata.annotations["cnpg.io/hibernation"] = "off"' }],
          scope: { kind: 'metadata', paths: ['metadata.annotations["cnpg.io/hibernation"]'] },
          success: () => 'Resume requested.',
        }
      case 'setMaintenance':
      case 'unsetMaintenance': {
        const setting = kind === 'setMaintenance'
        const single = facts.instances.length <= 1
        return {
          title: setting ? `Put ${name} in node maintenance?` : `Lift node maintenance on ${name}?`,
          confirmLabel: setting ? 'Set maintenance' : 'Lift maintenance',
          effect: setting ? (
            <>
              Tells the operator that node maintenance is under way. While it lasts, self-healing, rolling updates and the PodDisruptionBudgets are limited:
              {reusePVC
                ? ' an instance on a drained node waits for that node to return and restarts on the same volume, and the disruption budget is removed so the drain can proceed.'
                : ' an instance on a drained node is recreated on another node with a new volume cloned from the primary, and the old volume is deleted.'}{' '}
              Keep the window as short as possible.
            </>
          ) : (
            'Tells the operator maintenance is over: self-healing, rolling updates and disruption budgets return to normal.'
          ),
          body: (
            <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-0.5" checked={reusePVC} onChange={(e) => setReusePVC(e.target.checked)} />
              <span>
                Reuse volumes (<span className="font-mono">reusePVC</span>)
                <span className="block text-xs text-theme-text-secondary">
                  {setting ? 'On: wait for the node and keep the data volume. Off: rebuild the instance elsewhere (slow for large databases).' : `Currently ${facts.maintenance.reusePVC ? 'on' : 'off'}; kubectl cnpg maintenance unset writes this value too.`}
                </span>
              </span>
            </label>
          ),
          notes: [
            'CloudNativePG recommends managing drains with PodDisruptionBudgets (spec.enablePDB) instead; this mode is kept for local-storage setups.',
            ...(setting && single && !reusePVC ? ['With one instance and volume reuse off, the operator still refuses to drain its node: deleting the only instance would lose the data.'] : []),
          ],
          warnings: setting && ha.data?.pdbs.state === 'ok' && ha.data.pdbs.items.length > 0 && reusePVC ? ['The Cluster’s PodDisruptionBudgets are removed while maintenance is in progress, so drains are no longer held back.'] : [],
          writes: [{ summary: `patch Cluster ${namespace}/${name}`, detail: `spec.nodeMaintenanceWindow = {"inProgress": ${setting}, "reusePVC": ${reusePVC}}` }],
          scope: { kind: 'spec', paths: ['spec.nodeMaintenanceWindow.inProgress', 'spec.nodeMaintenanceWindow.reusePVC'] },
          typed: setting,
          disruptive: setting,
          params: { reusePVC },
          success: () => (setting ? 'Maintenance mode set. Lift it as soon as the node work is done.' : 'Maintenance mode lifted.'),
        }
      }
      case 'restartInstance':
        if (!instance) return UNSUPPORTED
        {
          return {
            title: `Restart ${instance.pod}?`,
            confirmLabel: 'Restart instance',
            effect: instanceIsPrimary
              ? 'Restarts PostgreSQL on the primary in place. Writes stop until it is back; no switchover happens.'
              : 'Deletes this standby’s Pod; the operator recreates it on the same volume and it catches up from the primary.',
            warnings: [
              !instanceIsPrimary && facts.instances.filter((i) => i.pod !== facts.currentPrimary).length <= 1
                ? 'This is the only standby: while it restarts there is no failover target, and a synchronous quorum may block writes.'
                : null,
            ].filter(Boolean) as string[],
            writes: instanceIsPrimary
              ? [{ summary: `patch Cluster ${namespace}/${name} status (subresource)`, detail: 'status.phase = "Primary instance is being restarted in-place"\nstatus.phaseReason = "Requested by the user"' }]
              : [{ summary: `delete Pod ${namespace}/${instance.pod}`, detail: `preconditions.uid = ${instance.podUID}` }],
            scope: instanceIsPrimary ? { kind: 'status' } : { kind: 'delete-operator-owned', owner: `Cluster ${name}` },
            typed: instanceIsPrimary,
            disruptive: true,
            params: { pod: instance.pod, podUID: instance.podUID },
            success: () => `Restart of ${instance.pod} requested.`,
          }
        }
      default:
        return UNSUPPORTED
    }
  })()

  const guard = useCNPGWriteGuard({ namespace, name, scope: spec.scope })
  const operatorNote = cnpgOperatorActionNote(caps.operator)

  const confirm = () => {
    mutation.mutate(
      {
        action: kind,
        request: { reviewedContext: caps.context, uid: caps.uid, facts: facts as unknown as Record<string, unknown>, params: spec.params },
      },
      {
        onSuccess: (result) => {
          showSuccess(spec.success(result))
          const tracked = trackedOperationFor(kind, { result, facts, switchTarget, chosenPodUID: chosenStandby?.podUID, fenceSel, fenced, instance })
          if (tracked) trackCNPGOperation({ ...tracked, context: caps.context, namespace, cluster: name, clusterUID: caps.uid })
          onClose()
        },
      },
    )
  }

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={confirm}
      title={spec.title}
      subject={{ kind: 'Cluster', namespace, name }}
      context={caps.context}
      effect={spec.effect}
      notes={operatorNote?.tone === 'info' ? [...(spec.notes ?? []), operatorNote.text] : spec.notes}
      warnings={operatorNote?.tone === 'warning' ? [operatorNote.text, ...(spec.warnings ?? [])] : spec.warnings}
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      writes={spec.writes}
      typedConfirmation={spec.typed ? name : undefined}
      confirmLabel={spec.confirmLabel}
      disruptive={spec.disruptive}
      disabledReason={!cap.allowed ? capabilityTitle(cap) : spec.invalid}
      incompleteReason={spec.incomplete}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={actionOutcomeLocked(mutation.error)}
    >
      {spec.body}
    </ActionConfirmDialog>
  )
}

export function CNPGRestartReview({ caps, problems = [] }: { caps: CNPGClusterCapabilities; problems?: CNPGProblem[] }) {
  const instances = caps.facts.instances
  const other = instances.filter((i) => i.pod !== caps.facts.currentPrimary)
  const noOtherReady = other.every((i) => i.podReadable && !i.ready)
  const primary = caps.restartPlan?.steps.find((s) => s.role === 'primary')
  const waiting = instances.filter((i) => i.podReadable && (!i.podExists || !i.ready))
  const primaryWillRestart = primary && ['restart', 'restart_only_instance', 'switchover'].includes(primary.effect)
  return (
    <div className="space-y-2 text-xs text-theme-text-secondary">
      {waiting.length > 0 && <p className={toneTextClass('degraded')}>The rolling restart waits for every instance to be ready. Currently blocked by {waiting.map((i) => i.pod).join(', ')}.</p>}
      {primaryWillRestart && <p>Restarting the primary interrupts its connections.{primary.effect === 'restart_only_instance' ? ' There is no other instance: the cluster stops serving until the primary is back.' : noOtherReady ? ' No other instance is ready now. If the primary restarts without another ready instance, the cluster stops serving until it is back.' : !other.some((i) => i.ready) ? ' Other instances’ readiness could not be read; service continuity is unknown.' : ''}</p>}
      {primary?.effect === 'wait_for_user' && <p>The primary waits for your manual promotion or restart. Restarting it later interrupts its connections.</p>}
      {primary?.effect === 'skipped_fenced' && <p>The fenced primary is skipped.</p>}
      {caps.restartPlan && <>
        <div>Expected order (primaryUpdateStrategy {caps.restartPlan.primaryUpdateStrategy}, primaryUpdateMethod {caps.restartPlan.primaryUpdateMethod}):</div>
        <ol className="list-decimal space-y-1 pl-5">
          {caps.restartPlan.steps.map((s) => {
            const instance = instances.find((i) => i.pod === s.instance)
            const blockers = problems.filter((p) => p.instance === s.instance || p.subject.kind === 'Pod' && p.subject.name === s.instance)
            return <li key={s.instance}>
              <span className="font-mono">{s.instance}</span> ({s.role}): {RESTART_EFFECT[s.effect] ?? s.effect}
              <span> · {instance?.podReadable ? instance.podExists ? instance.ready ? 'ready now' : 'not ready now' : 'instance Pod absent' : 'readiness not read'}</span>
              {blockers.map((p) => <div key={p.id} className={toneTextClass('degraded')}>{p.title}{p.detail ? ` · ${p.detail}` : ''}</div>)}
            </li>
          })}
        </ol>
      </>}
    </div>
  )
}
