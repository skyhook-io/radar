import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Check, Circle, CircleHelp, Copy, X } from 'lucide-react'
import { Badge, DialogPortal, StatusDot, coverageReadable, formatAge } from '@skyhook-io/k8s-ui'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { routePath } from '../../../api/config'
import { cnpgClusterFullPath } from '../paths'
import { useToast } from '../../ui/Toast'
import { useConnection } from '../../../context/ConnectionContext'
import { useCNPGClusterCapabilities, useCNPGRuntime, useCNPGWorkspace } from '../../../api/cnpg'
import { useCNPGClusterHA } from '../../../api/cnpg-ha'
import {
  CNPG_OP_TERMINAL,
  advanceCNPGOperation,
  cnpgOperationFollowed,
  type CNPGObservation,
  type CNPGTrackedOperation,
} from './model'
import {
  CNPG_OP_STATE_TEXT as STATE_TEXT,
  CNPG_OP_STATE_TONE as STATE_TONE,
  CNPG_OP_STATE_SEVERITY as STATE_SEVERITY,
  cnpgOperationHandoff,
  cnpgOperationsForCluster,
} from './presentation'
import { dismissCNPGOperation, updateCNPGOperations, useCNPGOperations } from './store'

const POLL_MS = 5_000

function sourceFreshness(q: { dataUpdatedAt: number; errorUpdatedAt: number; isError: boolean; isFetching: boolean; isFetchedAfterMount: boolean }) {
  return { updatedAt: q.dataUpdatedAt, checkedAt: Math.max(q.dataUpdatedAt, q.errorUpdatedAt), failed: q.isError, initialPending: q.isFetching && !q.isFetchedAfterMount }
}

/**
 * Follows the operations requested on this Cluster in this browser session
 * and shows them in the header until they finish. Reads only what the caller
 * can read; a step it cannot see stays "unknown".
 */
export function CNPGOperationTracker({ namespace, name, uid }: { namespace: string; name: string; uid: string }) {
  const { connection } = useConnection()
  const context = connection.context
  const allOps = useCNPGOperations()
  const ops = cnpgOperationsForCluster(allOps, { namespace, name, context, uid })
  const active = ops.filter(cnpgOperationFollowed)
  const followed = cnpgOperationsForCluster(allOps, { namespace, name, context }).filter(cnpgOperationFollowed)
  const following = followed.length > 0
  const needsRuntime = followed.some((o) => ['switchover', 'unfence', 'restart', 'restartInstance'].includes(o.kind))

  const queryClient = useQueryClient()
  const caps = useCNPGClusterCapabilities(namespace, name, following)
  const ha = useCNPGClusterHA(namespace, name, { enabled: following, refetchInterval: following ? POLL_MS : false })
  const runtime = useCNPGRuntime(namespace, name, following && needsRuntime)
  const workspace = useCNPGWorkspace([namespace], { enabled: following })

  useEffect(() => {
    if (!following) return
    const t = setInterval(() => {
      if (document.visibilityState === 'hidden') return
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'capabilities', 'clusters', namespace, name] }, { cancelRefetch: false })
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'workspace', namespace] }, { cancelRefetch: false })
    }, POLL_MS)
    return () => clearInterval(t)
  }, [following, queryClient, namespace, name])

  const capsFresh = sourceFreshness(caps)
  const haFresh = sourceFreshness(ha)
  const runtimeFresh = sourceFreshness(runtime)
  const workspaceFresh = sourceFreshness(workspace)
  const observation = useMemo<CNPGObservation | null>(() => {
    if (!following) return null
    const ws = workspace.data
    const cluster = ws?.objects.clusters?.find(
      (c: any) => c?.metadata?.namespace === namespace && c?.metadata?.name === name,
    )
    const backupsReadable = ws?.coverage.backups ? coverageReadable(ws.coverage.backups, namespace) : false
    const identity = [
      { uid: caps.data?.uid, updatedAt: capsFresh.updatedAt, failed: capsFresh.failed },
      { uid: ha.data?.cluster.uid, updatedAt: haFresh.updatedAt, failed: haFresh.failed },
      { uid: runtime.data?.cluster.uid, updatedAt: runtimeFresh.updatedAt, failed: runtimeFresh.failed },
      { uid: cluster?.metadata?.uid, updatedAt: workspaceFresh.updatedAt, failed: workspaceFresh.failed },
    ].filter((source) => source.uid && !source.failed).sort((a, b) => b.updatedAt - a.updatedAt)[0]
    return {
      // Failed polls must advance elapsed-time checks even when no data changes.
      now: Math.max(Date.now(), capsFresh.checkedAt, haFresh.checkedAt, runtimeFresh.checkedAt, workspaceFresh.checkedAt),
      clusterUID: identity?.uid,
      identityPending: capsFresh.initialPending || haFresh.initialPending || workspaceFresh.initialPending || (needsRuntime && runtimeFresh.initialPending),
      facts: caps.data?.facts,
      cluster,
      ha: ha.data,
      runtime: runtime.data,
      backups: backupsReadable ? (ws?.objects.backups ?? []) : undefined,
      freshness: {
        clusterUID: identity && { updatedAt: identity.updatedAt, failed: identity.failed },
        facts: { updatedAt: capsFresh.updatedAt, failed: capsFresh.failed, clusterUID: caps.data?.uid },
        cluster: { updatedAt: workspaceFresh.updatedAt, failed: workspaceFresh.failed, clusterUID: cluster?.metadata?.uid },
        backups: { updatedAt: workspaceFresh.updatedAt, failed: workspaceFresh.failed },
        ha: { updatedAt: haFresh.updatedAt, failed: haFresh.failed, clusterUID: ha.data?.cluster.uid },
        runtime: { updatedAt: runtimeFresh.updatedAt, failed: runtimeFresh.failed, clusterUID: runtime.data?.cluster.uid },
      },
    }
  }, [
    following,
    needsRuntime,
    namespace,
    name,
    caps.data,
    ha.data,
    runtime.data,
    workspace.data,
    capsFresh.updatedAt,
    capsFresh.failed,
    haFresh.updatedAt,
    haFresh.failed,
    runtimeFresh.updatedAt,
    runtimeFresh.failed,
    workspaceFresh.updatedAt,
    workspaceFresh.failed,
    capsFresh.initialPending,
    haFresh.initialPending,
    workspaceFresh.initialPending,
    runtimeFresh.initialPending,
    capsFresh.checkedAt,
    haFresh.checkedAt,
    workspaceFresh.checkedAt,
    runtimeFresh.checkedAt,
  ])

  useEffect(() => {
    if (!observation) return
    updateCNPGOperations((all) => {
      let changed = false
      const next = all.map((op) => {
        if (op.namespace !== namespace || op.cluster !== name || op.context !== context || !cnpgOperationFollowed(op))
          return op
        const adv = advanceCNPGOperation(op, observation)
        if (JSON.stringify(adv) !== JSON.stringify(op)) changed = true
        return adv
      })
      return changed ? next : all
    })
  }, [observation, namespace, name, context])

  const [open, setOpen] = useState(false)
  if (ops.length === 0) return null
  const lead = active[active.length - 1] ?? ops[ops.length - 1]

  return (
    <div className="relative">
      <button
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="btn-secondary inline-flex max-w-[22rem] items-center gap-1.5 px-2.5 py-1.5 text-xs"
      >
        <StatusDot tone={STATE_TONE[lead.state]} />
        <span className="truncate">{lead.label}</span>
        <span className="shrink-0 text-theme-text-tertiary">· {STATE_TEXT[lead.state]}</span>
        {ops.length > 1 && <span className="shrink-0 text-theme-text-tertiary">+{ops.length - 1}</span>}
      </button>
      <DialogPortal
        open={open}
        onClose={() => setOpen(false)}
        ariaLabel="Operations on this cluster"
        className="w-full max-w-xl p-4"
      >
        <div className="mb-3 flex items-center justify-between gap-3">
          <h3 className="text-sm font-semibold text-theme-text-primary">Operations in this tab</h3>
          <button
            type="button"
            onClick={() => setOpen(false)}
            aria-label="Close operations"
            className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="max-h-[65vh] space-y-2 overflow-y-auto">
          {[...ops].reverse().map((op) => (
            <OperationRow key={op.id} op={op} />
          ))}
        </div>
      </DialogPortal>
    </div>
  )
}

function OperationRow({ op }: { op: CNPGTrackedOperation }) {
  const terminal = CNPG_OP_TERMINAL.has(op.state)
  const { showSuccess, showError } = useToast()
  const handoff = async () => {
    const path = cnpgClusterFullPath(op.namespace, op.cluster, op.context)
    const url = new URL(routePath(path), window.location.origin).href
    const copied = await copyText(cnpgOperationHandoff(op, url))
    if (copied) showSuccess('Handoff copied · the link opens current Cluster facts')
    else showError('Could not copy the handoff')
  }
  return (
    <div className="rounded-md border border-theme-border bg-theme-base p-2 text-xs">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-medium text-theme-text-primary">{op.label}</span>
        <Badge severity={STATE_SEVERITY[op.state]} size="sm">
          {STATE_TEXT[op.state]}
        </Badge>
        {(terminal || op.state === 'unobservable') && (
          <button
            type="button"
            aria-label={`Dismiss ${op.label}`}
            onClick={() => dismissCNPGOperation(op.id)}
            className="rounded p-0.5 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <div className="mt-0.5 text-theme-text-tertiary">
        requested {formatAge(new Date(op.startedAt).toISOString())} ago
      </div>
      <div className="mt-0.5 text-theme-text-tertiary">
        {op.lastCheckedAt
          ? `Last checked in this tab ${formatAge(new Date(op.lastCheckedAt).toISOString())} ago`
          : 'Not yet checked in this tab'}
      </div>
      {op.detail && <div className="mt-1 text-theme-text-secondary">{op.detail}</div>}
      {op.steps && op.steps.length > 0 && (
        <ul className="mt-1.5 space-y-0.5">
          {op.steps.map((s) => (
            <li key={s.label} className="flex items-center gap-1.5 text-theme-text-secondary">
              {s.done === true ? (
                <Check className="h-3 w-3 shrink-0 text-accent" aria-label="done" />
              ) : s.done === false ? (
                <Circle className="h-3 w-3 shrink-0 text-theme-text-tertiary" aria-label="not yet" />
              ) : (
                <CircleHelp className="h-3 w-3 shrink-0 text-theme-text-tertiary" aria-label="not observable" />
              )}
              <span>{s.label}</span>
            </li>
          ))}
        </ul>
      )}
      <button
        type="button"
        onClick={() => void handoff()}
        className="mt-2 inline-flex items-center gap-1 text-accent-text hover:underline"
      >
        <Copy className="h-3 w-3" />
        Copy handoff
      </button>
    </div>
  )
}
