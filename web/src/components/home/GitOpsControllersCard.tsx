import { GitBranch, AlertCircle, CheckCircle2 } from 'lucide-react'
import type { DashboardGitOpsControllers, DashboardGitOpsController } from '../../api/client'
import { clsx } from 'clsx'

interface GitOpsControllersCardProps {
  data: DashboardGitOpsControllers
  onNavigate: () => void
}

// GitOpsControllersCard surfaces Argo CD / Flux controller pod health
// on the Home dashboard so an operator can spot "source-controller is
// CrashLoopBackOff" before drilling into individual GitOps applications
// and seeing the *symptoms* (apps stuck OutOfSync, sources unfetched).
//
// Capability-gated: the parent only renders this card when the backend
// actually detected controllers in the cluster (DashboardResponse.gitopsControllers
// is null on non-GitOps clusters). The card itself doesn't have an
// "empty state" branch — by the time we get here, we have something to show.
export function GitOpsControllersCard({ data, onNavigate }: GitOpsControllersCardProps) {
  const headerTone =
    data.status === 'crashing'
      ? 'text-red-500'
      : data.status === 'degraded'
        ? 'text-amber-400'
        : 'text-emerald-500'

  const headerLabel =
    data.status === 'crashing'
      ? 'Controllers crashing'
      : data.status === 'degraded'
        ? 'Controllers degraded'
        : 'Controllers healthy'

  // Group controllers by tool so the card reads as two sections (Argo +
  // Flux) when both are installed. Operators with only one tool see a
  // single-section card without empty placeholders.
  const argo = data.controllers.filter((c) => c.tool === 'argocd')
  const flux = data.controllers.filter((c) => c.tool === 'fluxcd')

  return (
    <button
      type="button"
      onClick={onNavigate}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <GitBranch className={clsx('h-4 w-4', headerTone)} />
            <span className={clsx('text-xs font-semibold uppercase tracking-wider', headerTone)}>
              GitOps Controllers
            </span>
          </div>
          <span className={clsx('text-[11px] font-medium', headerTone)}>{headerLabel}</span>
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto px-5 py-3 flex flex-col gap-3">
          {argo.length > 0 && <ControllerSection label="Argo CD" controllers={argo} />}
          {flux.length > 0 && <ControllerSection label="Flux CD" controllers={flux} />}
        </div>
      </div>
    </button>
  )
}

function ControllerSection({ label, controllers }: { label: string; controllers: DashboardGitOpsController[] }) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className="flex flex-col gap-1">
        {controllers.map((c) => (
          <ControllerRow key={`${c.tool}-${c.name}`} c={c} />
        ))}
      </div>
    </div>
  )
}

function ControllerRow({ c }: { c: DashboardGitOpsController }) {
  // Per-row tone matches the per-controller status. We don't reuse
  // mapHealthToTone because the controller status vocabulary
  // (healthy/degraded/crashing/pending) is different from resource
  // health (Healthy/Degraded/Missing/etc). Mapping inline keeps the
  // intent obvious.
  const tone =
    c.status === 'crashing'
      ? 'text-red-500'
      : c.status === 'degraded' || c.status === 'pending'
        ? 'text-amber-400'
        : 'text-emerald-500'
  const Icon = c.status === 'crashing' ? AlertCircle : c.status === 'degraded' || c.status === 'pending' ? AlertCircle : CheckCircle2
  return (
    <div className="flex items-center justify-between gap-2 text-[12px]">
      <div className="flex min-w-0 items-center gap-1.5">
        <Icon className={clsx('h-3 w-3 shrink-0', tone)} />
        <span className="truncate text-theme-text-primary">{c.name}</span>
      </div>
      <div className="flex shrink-0 items-center gap-1.5 text-[11px] text-theme-text-secondary">
        <span>
          {c.ready}/{c.total} ready
        </span>
        {c.crashReason && (
          <span className={clsx('rounded border px-1 py-px text-[10px] font-medium', 'border-red-500/40 bg-red-500/10 text-red-400')}>
            {c.crashReason}
          </span>
        )}
      </div>
    </div>
  )
}
