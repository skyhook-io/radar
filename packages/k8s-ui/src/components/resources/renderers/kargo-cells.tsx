import { clsx } from 'clsx'
import { healthColors } from '../resource-utils'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Tooltip } from '../../ui/Tooltip'
import {
  formatFreightVersions,
  getPromotionStatus,
  getStageCurrentFreight,
  getStageHealth,
  getStageLastPromotion,
  getStageStatus,
  shortFreightName,
  type KargoFreightContent,
  type KargoPromotion,
  type KargoStage,
} from '../resource-utils-kargo'

/** Freight aliases keyed "namespace/freightName", supplied by a host that can read Freight. */
export type KargoFreightAliases = Record<string, string>

function StatusPill({ text, level }: { text: string; level: keyof typeof healthColors }) {
  return <span className={clsx('badge', healthColors[level])}>{text}</span>
}

/**
 * One piece of Freight in a cell: alias first when known, then its versions. With
 * neither, the short Freight name, so a row never reads as empty when it holds
 * something.
 */
function FreightLine({ name, alias, content }: { name: string; alias?: string; content?: KargoFreightContent }) {
  const versions = formatFreightVersions(content)
  return (
    <Tooltip content={`Freight ${name}`} position="top">
      <span className="block truncate text-sm">
        {alias && <span className="font-medium text-theme-text-primary">{alias}</span>}
        {versions && <span className={clsx('font-mono text-xs', alias ? 'ml-1.5 text-theme-text-secondary' : 'text-theme-text-primary')}>{versions}</span>}
        {!alias && !versions && <span className="font-mono text-xs text-theme-text-secondary">{shortFreightName(name)}</span>}
      </span>
    </Tooltip>
  )
}

export function KargoStageCell({ resource, column, aliases }: { resource: KargoStage; column: string; aliases?: KargoFreightAliases }) {
  const namespace = resource.metadata?.namespace ?? ''
  switch (column) {
    case 'status': {
      const status = getStageStatus(resource)
      return <StatusPill text={status.text} level={status.level} />
    }
    case 'kargoFreight': {
      const current = getStageCurrentFreight(resource)
      if (current.length === 0) return <span className="text-sm text-theme-text-tertiary">None yet</span>
      return (
        <span className="flex min-w-0 flex-col">
          {current.map(item => (
            <FreightLine key={item.origin || item.name} name={item.name} alias={aliases?.[`${namespace}/${item.name}`]} content={item.ref} />
          ))}
        </span>
      )
    }
    case 'kargoHealth': {
      const health = getStageHealth(resource)
      return health.status ? <StatusPill text={health.status} level={health.tone} /> : <span className="text-sm text-theme-text-tertiary">Not reported</span>
    }
    case 'kargoLastPromotion': {
      const last = getStageLastPromotion(resource)
      if (!last) return <span className="text-sm text-theme-text-tertiary">None yet</span>
      return (
        <span className="flex items-center gap-1.5">
          <StatusPill text={last.phase ?? 'Not reported'} level={last.tone} />
          {last.finishedAt && <span className="text-xs text-theme-text-secondary">{formatRelativeAgeTime(last.finishedAt)}</span>}
        </span>
      )
    }
  }
  return <span className="text-sm text-theme-text-secondary">-</span>
}

export function KargoPromotionCell({ resource, column, aliases }: { resource: KargoPromotion; column: string; aliases?: KargoFreightAliases }) {
  const namespace = resource.metadata?.namespace ?? ''
  switch (column) {
    case 'status': {
      const status = getPromotionStatus(resource)
      return <StatusPill text={status.text} level={status.level} />
    }
    case 'kargoStage':
      return <span className="text-sm text-theme-text-primary">{resource.spec?.stage || '-'}</span>
    case 'kargoFreight': {
      const name = resource.status?.freight?.name ?? resource.spec?.freight ?? ''
      if (!name) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <FreightLine name={name} alias={aliases?.[`${namespace}/${name}`]} content={resource.status?.freight} />
    }
  }
  return <span className="text-sm text-theme-text-secondary">-</span>
}
