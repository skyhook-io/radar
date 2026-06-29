import { HEALTH_META } from '../../utils/applications'

/** Ready/desired progress bar shared by the Applications list and detail —
 *  colors come from HEALTH_META so the bar can't drift from the health system. */
export function ReadyBar({ ready, desired, width = 'w-12' }: { ready: number; desired: number; width?: string }) {
  if (desired <= 0) {
    return <span className="font-mono text-xs tabular-nums text-theme-text-tertiary">—</span>
  }
  const pct = Math.min(100, Math.round((ready / desired) * 100))
  const ok = ready >= desired
  // Text matches the bar's tier: amber for partial readiness, red only when
  // nothing is ready — partial must not read as fully down.
  const tier = ok ? HEALTH_META.healthy : ready === 0 ? HEALTH_META.unhealthy : HEALTH_META.degraded
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`inline-block h-1.5 ${width} rounded-full bg-theme-hover`}>
        <span className={`block h-1.5 rounded-full ${tier.bar}`} style={{ width: `${pct}%` }} />
      </span>
      <span className={`font-mono text-xs tabular-nums ${ok ? 'text-theme-text-secondary' : tier.text}`}>{ready}/{desired || '—'}</span>
    </span>
  )
}
