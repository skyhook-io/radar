import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import { Lock } from 'lucide-react'
import { toneTextClass } from '@skyhook-io/k8s-ui'
import { useLocation } from 'react-router-dom'
import { useCNPGNavigate } from './useCNPGNavigate'
import { cnpgClusterProblemsPath, cnpgWithinDetail } from './paths'
import type { CNPGRuntimeInstance } from '../../api/cnpg'

export function seconds(s?: number): string {
  if (s === undefined) return '—'
  if (s < 1) return `${(s * 1000).toFixed(0)} ms`
  if (s < 90) return `${s.toFixed(1)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
}

export function SourceState({ label, state, error, reason }: { label: string; state: string; error?: string; reason?: string }) {
  if (state === 'ok') return null
  const detail = [reason, error].filter(Boolean).join(' · ')
  const text =
    state === 'denied'
      ? `${label}: no access (needs get pods/proxy)`
      : state === 'partial'
        ? `${label}: partial${detail ? ` · ${detail}` : ''}`
        : `${label}: ${state}${detail ? ` · ${detail}` : ''}`
  return <div className="text-xs text-theme-text-tertiary">{text}</div>
}

// Denied is not zero: the section says what it would show and the grant it needs.
export function ProxyDenied({ what, grant }: { what: string; grant: string }) {
  return <Denied title="No access to live instance data" grant={grant}>
    {what} are read from each instance through the Kubernetes API proxy, which your identity may not use. These readings are unavailable.
  </Denied>
}

export function Denied({ title, grant, children }: { title: string; grant: string; children: ReactNode }) {
  return <div className="w-full rounded-xl border border-dashed border-theme-border p-4">
    <div className="flex items-center gap-2 text-sm font-medium text-theme-text-primary"><Lock className="h-4 w-4 shrink-0" />{title}</div>
    <p className="mt-2 text-sm text-theme-text-secondary">{children}</p>
    <div className="mt-2 break-words rounded-md bg-theme-elevated px-3 py-2 font-mono text-xs text-theme-text-primary">requires: {grant}</div>
  </div>
}

export function PrimaryPrerequisite({ namespace, cluster }: { namespace: string; cluster: string }) {
  const navigate = useCNPGNavigate()
  const location = useLocation()
  const overviewPath = `${cnpgClusterProblemsPath(namespace, cluster, new URLSearchParams(location.search).get('ctx') ?? undefined)}&tab=overview`
  const inPlace = cnpgWithinDetail(location.pathname, location.search, overviewPath)
  return <div className="text-sm text-theme-text-tertiary">Available once the primary is running. <button type="button" className="text-accent-text hover:underline" onClick={() => navigate(inPlace ?? overviewPath, { replace: !!inPlace, state: location.state })}>See Overview →</button></div>
}

export function Card({ title, children, footer, aside }: { title: ReactNode; children: ReactNode; footer?: ReactNode; aside?: ReactNode }) {
  return (
    <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-theme-border px-4 py-2.5">
        <div className="text-sm font-semibold text-theme-text-primary">{title}</div>
        {aside && <div className="ml-auto text-xs">{aside}</div>}
      </div>
      <div className="p-4">{children}</div>
      {footer && <div className="border-t border-theme-border px-4 py-2 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

export function Unavailable({ inst, what }: { inst?: CNPGRuntimeInstance; what: string }) {
  if (!inst) return <div className="text-sm text-theme-text-tertiary">No instance is reported, so {what} is unknown.</div>
  return <SourceState label={what} state={inst.metrics.state} error={inst.metrics.error} reason={inst.metrics.reason} />
}

export function Metric({ label, value, tone, caption }: { label: string; value: ReactNode; tone?: 'degraded' | 'unhealthy'; caption?: ReactNode }) {
  return (
    <div>
      <div className="text-xs text-theme-text-tertiary">{label}</div>
      <div className={clsx('font-mono text-base', tone ? toneTextClass(tone) : 'text-theme-text-primary')}>{value}</div>
      {caption && <div className="text-[11px] text-theme-text-tertiary">{caption}</div>}
    </div>
  )
}
