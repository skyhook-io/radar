// Crossplane cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getCrossplaneStatus,
  getCrossplaneStatusReason,
  getProviderStatus,
  getProviderConfigStatus,
  getProviderConfigCredentialsSource,
  getProviderPackage,
  getProviderCurrentRevision,
  getProviderConfigRef,
  getExternalName,
  getCompositionRef,
  getCrossplaneResourceRefs,
  getCompositionCompositeTypeRef,
  getCompositionMode,
  getCompositionFunctionCount,
  getXRDClaimNames,
  getXRDKind,
} from '../resource-utils-crossplane'

function StatusBadge({ resource, getStatus }: { resource: any; getStatus: (r: any) => { text: string; color: string } }) {
  const status = getStatus(resource)
  const reason = getCrossplaneStatusReason(resource)
  const badge = (
    <span className={clsx('badge', status.color)}>
      {status.text}
    </span>
  )
  return reason ? <Tooltip content={reason}>{badge}</Tooltip> : badge
}

export function ManagedResourceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getCrossplaneStatus} />
    case 'external': {
      const name = getExternalName(resource)
      if (!name) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={name}>
          <span className="text-sm text-theme-text-secondary font-mono truncate block">{name}</span>
        </Tooltip>
      )
    }
    case 'provider': {
      const ref = getProviderConfigRef(resource)
      if (!ref) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary">{ref.name}</span>
    }
    case 'kind':
      return <span className="text-sm text-theme-text-secondary">{resource.kind || '-'}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CompositeResourceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getCrossplaneStatus} />
    case 'composition': {
      const ref = getCompositionRef(resource)
      if (!ref) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={ref.name}>
          <span className="text-sm text-theme-text-secondary truncate block">{ref.name}</span>
        </Tooltip>
      )
    }
    case 'composed': {
      const refs = getCrossplaneResourceRefs(resource)
      return <span className="text-sm text-theme-text-secondary">{refs.length > 0 ? refs.length : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CrossplaneProviderCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getProviderStatus} />
    case 'package': {
      const pkg = getProviderPackage(resource)
      return (
        <Tooltip content={pkg}>
          <span className="text-sm text-theme-text-secondary font-mono truncate block">{pkg}</span>
        </Tooltip>
      )
    }
    case 'revision': {
      const rev = getProviderCurrentRevision(resource)
      if (!rev) return <span className="text-sm text-theme-text-tertiary">-</span>
      return (
        <Tooltip content={rev}>
          <span className="text-sm text-theme-text-tertiary font-mono truncate block">{rev}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CrossplaneProviderConfigCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status':
      return <StatusBadge resource={resource} getStatus={getProviderConfigStatus} />
    case 'credentials':
      return <span className="text-sm text-theme-text-secondary">{getProviderConfigCredentialsSource(resource)}</span>
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function XRDCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'kind':
      return <span className="text-sm text-theme-text-secondary font-mono">{getXRDKind(resource)}</span>
    case 'claim': {
      const cn = getXRDClaimNames(resource)
      if (!cn.kind) return <span className="text-sm text-theme-text-tertiary">-</span>
      return <span className="text-sm text-theme-text-secondary font-mono">{cn.kind}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function CompositionCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'composite':
      return <span className="text-sm text-theme-text-secondary font-mono">{getCompositionCompositeTypeRef(resource)}</span>
    case 'mode':
      return <span className="text-sm text-theme-text-secondary">{getCompositionMode(resource)}</span>
    case 'functions': {
      const count = getCompositionFunctionCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
