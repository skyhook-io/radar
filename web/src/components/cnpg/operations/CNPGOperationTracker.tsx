import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Check, Circle, CircleHelp, X } from 'lucide-react'
import { Badge, StatusDot, coverageReadable, formatAge, type HealthLevel } from '@skyhook-io/k8s-ui'
import { useConnection } from '../../../context/ConnectionContext'
import { useCNPGClusterCapabilities, useCNPGRuntime, useCNPGWorkspace } from '../../../api/cnpg'
import { useCNPGClusterHA } from '../../../api/cnpg-ha'
import { useAnimatedUnmount } from '../../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../../utils/animation'
import { CNPG_OP_TERMINAL, advanceCNPGOperation, type CNPGObservation, type CNPGOpState, type CNPGTrackedOperation } from './model'
import { dismissCNPGOperation, updateCNPGOperations, useCNPGOperations } from './store'

const STATE_TEXT: Record<CNPGOpState, string> = {
  requested: 'requested',
  observed: 'observed by the operator',
  progressing: 'in progress',
  completed: 'completed',
  failed: 'failed',
  stalled: 'stalled',
  superseded: 'superseded',
  unobservable: 'cannot be followed',
}

const STATE_TONE: Record<CNPGOpState, HealthLevel> = {
  requested: 'neutral',
  observed: 'neutral',
  progressing: 'neutral',
  completed: 'healthy',
  failed: 'unhealthy',
  stalled: 'degraded',
  superseded: 'unknown',
  unobservable: 'unknown',
}

const STATE_SEVERITY: Record<CNPGOpState, 'success' | 'error' | 'warning' | 'info' | 'neutral'> = {
  requested: 'info',
  observed: 'info',
  progressing: 'info',
  completed: 'success',
  failed: 'error',
  stalled: 'warning',
  superseded: 'neutral',
  unobservable: 'neutral',
}

const POLL_MS = 5_000

/**
 * Follows the operations requested on this Cluster in this browser session
 * and shows them in the header until they finish. Reads only what the caller
 * can read; a step it cannot see stays "unknown".
 */
export function CNPGOperationTracker({ namespace, name }: { namespace: string; name: string }) {
  const { connection } = useConnection()
  const context = connection.context
  const ops = useCNPGOperations({ namespace, cluster: name, context })
  const active = ops.filter((o) => !CNPG_OP_TERMINAL.has(o.state))
  const following = active.length > 0
  const needsRuntime = active.some((o) => ['switchover', 'unfence', 'restart', 'restartInstance'].includes(o.kind))

  const queryClient = useQueryClient()
  const caps = useCNPGClusterCapabilities(namespace, name, following)
  const ha = useCNPGClusterHA(namespace, name, { enabled: following, refetchInterval: following ? POLL_MS : false })
  const runtime = useCNPGRuntime(namespace, name, following && needsRuntime)
  const workspace = useCNPGWorkspace([namespace], { enabled: following })

  useEffect(() => {
    if (!following) return
    const t = setInterval(() => {
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'capabilities', 'clusters', namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['cnpg', 'workspace', namespace] })
    }, POLL_MS)
    return () => clearInterval(t)
  }, [following, queryClient, namespace, name])

  const observation = useMemo<CNPGObservation | null>(() => {
    if (!following) return null
    const ws = workspace.data
    const cluster = ws?.objects.clusters?.find((c: any) => c?.metadata?.namespace === namespace && c?.metadata?.name === name)
    const backupsReadable = ws?.coverage.backups ? coverageReadable(ws.coverage.backups, namespace) : false
    return {
      now: Date.now(),
      clusterUID: caps.data?.uid ?? ha.data?.cluster.uid ?? cluster?.metadata?.uid,
      facts: caps.data?.facts,
      cluster,
      ha: ha.data,
      runtime: runtime.data,
      backups: backupsReadable ? ws?.objects.backups ?? [] : undefined,
    }
  }, [following, caps.data, ha.data, runtime.data, workspace.data, namespace, name])

  useEffect(() => {
    if (!observation) return
    updateCNPGOperations((all) => {
      let changed = false
      const next = all.map((op) => {
        if (op.namespace !== namespace || op.cluster !== name || op.context !== context || CNPG_OP_TERMINAL.has(op.state)) return op
        const adv = advanceCNPGOperation(op, observation)
        if (JSON.stringify(adv) !== JSON.stringify(op)) changed = true
        return adv
      })
      return changed ? next : all
    })
  }, [observation, namespace, name, context])

  const [open, setOpen] = useState(false)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
  if (ops.length === 0) return null
  const lead = active[active.length - 1] ?? ops[ops.length - 1]

  return (
    <div className="relative">
      <button
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="inline-flex max-w-[22rem] items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs text-theme-text-primary hover:bg-theme-hover"
      >
        <StatusDot tone={STATE_TONE[lead.state]} />
        <span className="truncate">{lead.label}</span>
        <span className="shrink-0 text-theme-text-tertiary">· {STATE_TEXT[lead.state]}</span>
        {ops.length > 1 && <span className="shrink-0 text-theme-text-tertiary">+{ops.length - 1}</span>}
      </button>
      {open && <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} aria-hidden />}
      {shouldRender && (
        <div
          role="dialog"
          aria-label="Operations on this cluster"
          inert={!open || undefined}
          className={`absolute right-0 top-full z-50 mt-1 w-[26rem] origin-top-right space-y-2 rounded-lg border border-theme-border bg-theme-surface p-3 shadow-theme-lg ${TRANSITION_MENU} ${
            isOpen ? 'translate-y-0 scale-100 opacity-100' : '-translate-y-1 scale-[0.97] opacity-0'
          } ${open ? '' : 'pointer-events-none'}`}
          style={overlayTransitionStyle(isOpen, 'menu')}
        >
          <div className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Operations this session</div>
          {[...ops].reverse().map((op) => (
            <OperationRow key={op.id} op={op} />
          ))}
        </div>
      )}
    </div>
  )
}

function OperationRow({ op }: { op: CNPGTrackedOperation }) {
  const terminal = CNPG_OP_TERMINAL.has(op.state)
  return (
    <div className="rounded-md border border-theme-border bg-theme-base p-2 text-xs">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-medium text-theme-text-primary">{op.label}</span>
        <Badge severity={STATE_SEVERITY[op.state]} size="sm">{STATE_TEXT[op.state]}</Badge>
        {(terminal || op.state === 'unobservable') && (
          <button type="button" aria-label={`Dismiss ${op.label}`} onClick={() => dismissCNPGOperation(op.id)} className="rounded p-0.5 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary">
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <div className="mt-0.5 text-theme-text-tertiary">requested {formatAge(new Date(op.startedAt).toISOString())} ago</div>
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
    </div>
  )
}
