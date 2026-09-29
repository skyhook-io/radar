import { useMemo, useState, type ReactNode } from 'react'
import { ChevronDown, DatabaseBackup, MoreHorizontal, Repeat } from 'lucide-react'
import { ActionConfirmDialog, Tooltip, type ActionWrite } from '@skyhook-io/k8s-ui'
import {
  cnpgActionErrorCode,
  useCNPGAction,
  useCNPGClusterCapabilities,
  useCNPGRuntime,
  type CNPGActionCapability,
  type CNPGBackupMethod,
  type CNPGClusterActionName,
  type CNPGClusterCapabilities,
} from '../../../api/cnpg'
import { useToast } from '../../ui/Toast'
import { useCNPGWriteGuard, type CNPGWriteScope } from './useCNPGWriteGuard'
import { CNPGRestoreDialog } from './CNPGRestoreDialog'
import { useOpenCNPGPsql } from './useOpenCNPGPsql'
import { backupNameFor, describeBackupMethod, pickDefaultStandby, type StandbyChoice } from './actionModel'

type DialogKind = CNPGClusterActionName | 'restore' | null

const MENU_ITEM = 'flex w-full items-center justify-between gap-3 px-3 py-1.5 text-left text-sm text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:text-theme-text-disabled'

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

function capabilityTitle(cap: CNPGActionCapability | undefined): string | undefined {
  if (!cap) return 'Checking permissions…'
  if (cap.allowed) return undefined
  return cap.reason ?? (cap.permission === 'denied' ? `Your account may not do this (${cap.grant ?? 'permission denied'})` : 'Not available')
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
  const actions = caps.data?.actions
  const openPsql = useOpenCNPGPsql()

  const item = (id: CNPGClusterActionName, label: string) => {
    const cap = actions?.[id]
    const title = capabilityTitle(cap)
    return (
      <Tooltip key={id} content={title} position="left" wrapperClassName="block">
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
        </button>
      </Tooltip>
    )
  }

  const hibernated = !!caps.data?.facts.hibernated

  return (
    <div className="relative flex items-center gap-1.5">
      <Tooltip content={capabilityTitle(actions?.backup) ?? 'Create an on-demand Backup'} position="bottom">
        <button
          type="button"
          disabled={!actions?.backup.allowed}
          onClick={() => setOpen('backup')}
          className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          <DatabaseBackup className="h-3.5 w-3.5" />
          {!compact && 'Back up now'}
        </button>
      </Tooltip>
      {!compact && (
        <Tooltip content={capabilityTitle(actions?.switchover) ?? 'Promote a standby to primary'} position="bottom">
          <button
            type="button"
            disabled={!actions?.switchover.allowed}
            onClick={() => setOpen('switchover')}
            className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50"
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
        onClick={() => setMenu((v) => !v)}
        className="inline-flex items-center gap-0.5 rounded-lg border border-theme-border bg-theme-surface px-2 py-1.5 text-xs text-theme-text-primary hover:bg-theme-hover"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
        <ChevronDown className="h-3 w-3" />
      </button>
      {menu && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setMenu(false)} aria-hidden />
          <div role="menu" className="absolute right-0 top-full z-50 mt-1 w-56 overflow-hidden rounded-lg border border-theme-border bg-theme-surface py-1 shadow-theme-lg">
            {compact && item('switchover', 'Switchover…')}
            {item('restart', 'Restart instances…')}
            {item('reload', 'Reload configuration…')}
            {item('fence', 'Fence instances…')}
            {item('unfence', 'Lift fencing…')}
            {hibernated ? item('rehydrate', 'Resume from hibernation…') : item('hibernate', 'Hibernate…')}
            <div className="my-1 border-t border-theme-border" />
            <Tooltip content={capabilityTitle(actions?.psql)} position="left" wrapperClassName="block">
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
              </button>
            </Tooltip>
            <button type="button" role="menuitem" className={MENU_ITEM} onClick={() => { setMenu(false); setOpen('restore') }}>
              Restore to a new cluster…
            </button>
          </div>
        </>
      )}
      {caps.data && open && open !== 'restore' && (
        <ClusterActionDialog kind={open} caps={caps.data} namespace={namespace} name={name} onClose={() => setOpen(null)} />
      )}
      {open === 'restore' && <CNPGRestoreDialog namespace={namespace} name={name} onClose={() => setOpen(null)} />}
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
  invalid: 'Restart a single instance from its row in the Runtime tab.',
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
  const runtime = useCNPGRuntime(namespace, name, kind === 'switchover')

  const backupMethods = facts.backupMethods.filter((m) => m.capability !== 'none')
  const [method, setMethod] = useState<CNPGBackupMethod | undefined>(backupMethods[0])
  const [target, setTarget] = useState<'' | 'primary' | 'prefer-standby'>('')
  const [backupName, setBackupName] = useState(() => backupNameFor(name))

  const standbys: StandbyChoice[] = useMemo(() => {
    const lags = new Map<string, { replayLagSeconds?: number; state?: string; syncState?: string }>()
    const primary = runtime.data?.instances.find((i) => i.role === 'primary')
    for (const r of primary?.status.replication ?? []) {
      lags.set(r.applicationName, { replayLagSeconds: r.replayLag, state: r.state, syncState: r.syncState })
    }
    return facts.instances
      .filter((i) => i.pod !== facts.currentPrimary)
      .map((i) => {
        const lag = lags.get(i.pod)
        const cap = caps.instanceActions?.[i.pod]?.switchoverTarget
        const ineligible = i.fenced ? 'fenced' : !i.podExists ? 'Pod missing' : !i.ready ? 'not ready' : cap && !cap.allowed ? cap.reason ?? 'not eligible' : undefined
        return { pod: i.pod, podUID: i.podUID, ineligible, ...lag }
      })
  }, [facts, runtime.data])
  const [switchTarget, setSwitchTarget] = useState<string | undefined>(() => initialPod ?? pickDefaultStandby(standbys)?.pod)
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
                <option value="">Cluster default</option>
                <option value="prefer-standby">Prefer a standby</option>
                <option value="primary">Primary</option>
              </select>
              <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-backup-name">Name</label>
              <input id="cnpg-backup-name" value={backupName} onChange={(e) => setBackupName(e.target.value)} className="rounded-lg border border-theme-border bg-theme-base px-2 py-1 font-mono text-sm" />
            </div>
          ),
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
          invalid: !method ? 'This cluster declares no backup method.' : !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(backupName) ? 'The name must be a valid Kubernetes object name.' : undefined,
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
              {standbys.map((s) => (
                <label key={s.pod} className="flex items-center gap-2 text-sm">
                  <input type="radio" name="cnpg-switch" value={s.pod} disabled={!!s.ineligible} checked={switchTarget === s.pod} onChange={() => setSwitchTarget(s.pod)} />
                  <span className="font-mono">{s.pod}</span>
                  <span className="text-xs text-theme-text-tertiary">
                    {s.ineligible ??
                      [s.state, s.syncState, s.replayLagSeconds !== undefined ? `replay lag ${s.replayLagSeconds.toFixed(1)} s` : runtime.isLoading ? 'lag loading…' : 'lag unknown']
                        .filter(Boolean)
                        .join(' · ')}
                  </span>
                </label>
              ))}
            </fieldset>
          ),
          warnings: [
            chosenStandby?.replayLagSeconds !== undefined && chosenStandby.replayLagSeconds > 5
              ? `${chosenStandby.pod} is ${chosenStandby.replayLagSeconds.toFixed(0)} s behind; the switchover waits for it to catch up.`
              : null,
            chosenStandby && chosenStandby.replayLagSeconds === undefined ? 'Replication lag for this standby is not known (runtime data unavailable).' : null,
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
          invalid: !switchTarget || chosenStandby?.ineligible ? 'Pick an eligible standby.' : undefined,
          success: () => `Switchover to ${switchTarget} requested.`,
        }
      case 'restart':
        return {
          title: `Restart ${name}?`,
          confirmLabel: 'Restart instances',
          effect: 'Restarts every instance with a rolling update: standbys first, then the primary.',
          body: caps.restartPlan ? (
            <div>
              <div className="mb-1 text-xs text-theme-text-secondary">
                Expected order (primaryUpdateStrategy {caps.restartPlan.primaryUpdateStrategy ?? 'unsupervised'}, primaryUpdateMethod {caps.restartPlan.primaryUpdateMethod ?? 'restart'}):
              </div>
              <ol className="list-decimal space-y-1 pl-5 text-xs text-theme-text-secondary">
                {caps.restartPlan.steps.map((s, i) => (
                  <li key={i}>
                    <span className="font-mono">{s.instance}</span> ({s.role}): {RESTART_EFFECT[s.effect] ?? s.effect}
                  </li>
                ))}
              </ol>
            </div>
          ) : undefined,
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

  const confirm = () => {
    mutation.mutate(
      {
        action: kind,
        request: { reviewedContext: caps.context, uid: caps.uid, facts: facts as unknown as Record<string, unknown>, params: spec.params },
        successMessage: '',
      },
      {
        onSuccess: (result) => {
          showSuccess(spec.success(result))
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
      notes={spec.notes}
      warnings={spec.warnings}
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      writes={spec.writes}
      typedConfirmation={spec.typed ? name : undefined}
      confirmLabel={spec.confirmLabel}
      disruptive={spec.disruptive}
      disabledReason={!cap.allowed ? capabilityTitle(cap) : spec.invalid}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={cnpgActionErrorCode(mutation.error) === 'outcome_unknown'}
    >
      {spec.body}
    </ActionConfirmDialog>
  )
}
