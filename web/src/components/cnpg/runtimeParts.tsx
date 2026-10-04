import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import { Lock } from 'lucide-react'
import { toneTextClass } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeInstance } from '../../api/cnpg'

export function seconds(s?: number): string {
  if (s === undefined) return '—'
  if (s < 1) return `${(s * 1000).toFixed(0)} ms`
  if (s < 90) return `${s.toFixed(1)} s`
  if (s < 5400) return `${Math.round(s / 60)} min`
  return `${(s / 3600).toFixed(1)} h`
}

export function SourceState({ label, state, error }: { label: string; state: string; error?: string }) {
  if (state === 'ok') return null
  const text =
    state === 'denied'
      ? `${label}: no access (needs get pods/proxy)`
      : state === 'partial'
        ? `${label}: partial${error ? ` · ${error}` : ''}`
        : `${label}: ${state}${error ? ` · ${error}` : ''}`
  return <div className="text-xs text-theme-text-tertiary">{text}</div>
}

// Denied is not zero: the section says what it would show and the grant it needs.
export function ProxyDenied({ what, grant }: { what: string; grant: string }) {
  return (
    <div className="max-w-2xl rounded-xl border border-dashed border-theme-border p-5">
      <div className="flex items-center gap-2 font-medium text-theme-text-primary">
        <Lock className="h-4 w-4" />
        No access to live instance data
      </div>
      <p className="mt-2 text-sm text-theme-text-secondary">
        {what} are read from each instance through the Kubernetes API proxy, which your identity may not use. Nothing is shown as zero; it is omitted.
      </p>
      <pre className="mt-2 rounded-md bg-theme-elevated px-3 py-2 font-mono text-xs text-theme-text-primary">{`requires: ${grant}`}</pre>
    </div>
  )
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
  return <SourceState label={what} state={inst.metrics.state} error={inst.metrics.error} />
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
