import { Tooltip } from '../ui/Tooltip'
import {
  type AppRow,
  type AppWorkloadClass,
  type BatchSignal,
  CHIP,
  CHIP_TONE,
  CLASS_META,
  overlayProvenance,
} from '../../utils/applications'
import { midTruncate } from '../../utils/format'
import { ProvenanceTooltip, CategoryTooltip, VersionTooltip } from './AppTooltips'

// The composed badges shared by ApplicationsList and ApplicationDetail. Both
// surfaces must render the same chip with the same tooltip for the same fact —
// defining them once is what keeps that true.

export function ProvenanceBadge({ tier, appKey, confidence }: { tier?: number; appKey: string; confidence?: string }) {
  if (!tier) {
    return (
      <Tooltip content="No GitOps, Helm, or app-label grouping signal — shown as the raw workload." delay={150}>
        <span className={`${CHIP} ${CHIP_TONE.muted}`}>ungrouped</span>
      </Tooltip>
    )
  }
  const conf = confidence ?? 'low'
  const tone = conf === 'high' ? CHIP_TONE.emerald : conf === 'medium' ? CHIP_TONE.neutral : CHIP_TONE.amber
  return (
    <Tooltip content={<ProvenanceTooltip tier={tier} appKey={appKey} confidence={conf} />} delay={150}>
      <span className={`${CHIP} ${tone}`}>{overlayProvenance(tier)}</span>
    </Tooltip>
  )
}

export function ClassBadge({
  workloadClass,
  composition,
}: {
  workloadClass: AppWorkloadClass
  /** Per-class counts (classCompositionOf) — lets a Mixed badge's tooltip say
   *  what the mix actually contains. */
  composition?: { cls: AppWorkloadClass; count: number }[]
}) {
  const meta = CLASS_META[workloadClass]
  const tooltip =
    workloadClass === 'mixed' && composition && composition.length > 0 ? (
      <div className="space-y-1">
        <div className="text-xs text-theme-text-primary">
          Contains {composition.map((c) => `${c.count} ${CLASS_META[c.cls].label}${c.count > 1 ? 's' : ''}`).join(' · ')}
        </div>
        <div className="text-[11px] leading-snug text-theme-text-secondary">Class filters match any of the contained classes.</div>
      </div>
    ) : (
      meta.tooltip
    )
  return (
    <Tooltip content={tooltip} delay={150}>
      <span className={`${CHIP} ${meta.pill}`}>{meta.label}</span>
    </Tooltip>
  )
}

/** The add-on / mixed classification chip; renders nothing for plain apps. */
export function CategoryChip({ category, addonReason }: { category?: string; addonReason?: string }) {
  if (category !== 'addon' && category !== 'mixed') return null
  return (
    <Tooltip content={<CategoryTooltip category={category} addonReason={addonReason} />} delay={150}>
      <span className={`${CHIP} ${category === 'mixed' ? CHIP_TONE.amber : CHIP_TONE.muted}`}>{category === 'addon' ? 'add-on' : 'mixed'}</span>
    </Tooltip>
  )
}

export function BatchSignalChip({ signal }: { signal: BatchSignal | null }) {
  if (!signal) return null
  return (
    <Tooltip content={signal.detail} delay={150}>
      <span className={`${CHIP} ${CHIP_TONE[signal.tone]}`}>{signal.label}</span>
    </Tooltip>
  )
}

/** The version display: appVersion when the workloads agree on one upstream
 *  version, the single image tag, or "N versions" (amber only on real skew).
 *  `cell` = table chip styling, `fact` = the detail context strip. */
export function VersionInfo({ app, variant }: { app: AppRow; variant: 'cell' | 'fact' }) {
  const versions = Array.from(new Set((app.versions || []).filter(Boolean)))
  const workloads = app.workloads ?? []
  const max = variant === 'fact' ? 32 : 24
  const mono = variant === 'fact' ? 'font-mono' : 'font-mono text-xs text-theme-text-secondary'
  if (app.appVersion) {
    return (
      <Tooltip content={<VersionTooltip workloads={workloads} />} delay={150}>
        <span className={mono}>{midTruncate(app.appVersion, max)}</span>
      </Tooltip>
    )
  }
  if (versions.length === 0) {
    return variant === 'cell' ? <span className="text-theme-text-tertiary">—</span> : null
  }
  if (versions.length === 1) {
    const v = versions[0]
    return v.length > max ? (
      <Tooltip content={v} delay={150}>
        <span className={mono}>{midTruncate(v, max)}</span>
      </Tooltip>
    ) : (
      <span className={mono}>{v}</span>
    )
  }
  return (
    <Tooltip content={<VersionTooltip workloads={workloads} />} delay={150}>
      {variant === 'cell' ? (
        <span className={`${CHIP} ${app.versionSkew ? CHIP_TONE.amber : CHIP_TONE.neutral}`}>{versions.length} versions</span>
      ) : (
        <span className={mono}>{versions.length} versions</span>
      )}
    </Tooltip>
  )
}
