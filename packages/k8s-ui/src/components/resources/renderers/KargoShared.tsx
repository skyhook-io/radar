import type { ReactNode } from 'react'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import { ResourceLink, type ConditionTone } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import type { HealthLevel } from '../resource-utils'
import {
  KARGO_GROUP,
  formatFreightVersion,
  isKargoOutcomeCondition,
  isKargoWaitingReason,
  getFreightVersions,
  shortFreightName,
  type FreightVersion,
  type KargoFreightContent,
} from '../resource-utils-kargo'

export type KargoNavigate = (ref: ResourceRef) => void

/** Host-wired: opens the project's pipeline page, at the given Stage's card when one is named. */
export type KargoOpenPipeline = (stage?: string) => void

export function OpenInPipelineLink({ onOpenPipeline, stage }: { onOpenPipeline?: KargoOpenPipeline; stage?: string }) {
  if (!onOpenPipeline) return null
  return (
    <div className="mb-3">
      <button type="button" onClick={() => onOpenPipeline(stage)} className="text-xs font-medium text-accent-text hover:underline">
        Open in pipeline →
      </button>
    </div>
  )
}

const TONE_SEVERITY: Record<HealthLevel, BadgeSeverity> = {
  healthy: 'success',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  neutral: 'info',
  unknown: 'neutral',
}

export function ToneBadge({ tone, children, size }: { tone: HealthLevel; children: ReactNode; size?: 'sm' | 'default' }) {
  return <Badge severity={TONE_SEVERITY[tone]} size={size}>{children}</Badge>
}

const VERSION_TYPE_LABEL: Record<FreightVersion['type'], string> = {
  image: 'Image',
  commit: 'Commit',
  chart: 'Chart',
  artifact: 'Artifact',
}

/** The versions a piece of Freight carries, one chip each, with the full repository on hover. */
export function FreightVersionChips({ freight }: { freight: KargoFreightContent | null | undefined }) {
  const versions = getFreightVersions(freight)
  if (versions.length === 0) return <span className="text-theme-text-tertiary">No versions recorded</span>
  return (
    <span className="flex flex-wrap gap-1">
      {versions.map((v, i) => (
        <Tooltip key={`${v.type}-${v.repoURL ?? v.source}-${i}`} content={`${VERSION_TYPE_LABEL[v.type]}${v.repoURL ? `: ${v.repoURL}` : ''}`} position="top">
          <Badge tone="structural" size="sm">{formatFreightVersion(v)}</Badge>
        </Tooltip>
      ))}
    </span>
  )
}

export function KargoLink({ kind, name, namespace, label, onNavigate }: {
  kind: 'Stage' | 'Freight' | 'Promotion' | 'Warehouse'
  name: string
  namespace: string
  label?: ReactNode
  onNavigate?: KargoNavigate
}) {
  return <ResourceLink kind={kind} name={name} namespace={namespace} group={KARGO_GROUP} label={label} onNavigate={onNavigate} />
}

/** "singing-octopus" with the short Freight name beside it, or the short name alone when the alias is not known. */
export function FreightNameLink({ name, alias, namespace, onNavigate }: {
  name: string
  alias?: string
  namespace: string
  onNavigate?: KargoNavigate
}) {
  if (!name) return <span className="text-theme-text-tertiary">Not recorded</span>
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <KargoLink kind="Freight" name={name} namespace={namespace} onNavigate={onNavigate} label={alias ?? shortFreightName(name)} />
      {alias && (
        <Tooltip content={name} position="top">
          <span className="font-mono text-xs text-theme-text-tertiary">{shortFreightName(name)}</span>
        </Tooltip>
      )}
    </span>
  )
}

/**
 * Kargo's in-flight conditions (Promoting, Reconciling) are normal-false: True
 * means work is under way, not that something broke. A Stage with no Freight
 * yet is waiting, not failing.
 */
export function kargoConditionTone(cond: { type?: string; status?: string; reason?: string }): ConditionTone | undefined {
  if (cond.type === 'Promoting' || cond.type === 'Reconciling') return cond.status === 'True' ? 'neutral' : 'ok'
  if (isKargoOutcomeCondition(cond.type)) return cond.status === 'True' ? 'ok' : 'neutral'
  if (cond.status === 'False' && isKargoWaitingReason(cond.reason)) return 'neutral'
  return undefined
}
