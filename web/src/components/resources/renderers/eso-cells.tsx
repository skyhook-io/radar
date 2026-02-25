// External Secrets Operator cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getExternalSecretStatus,
  getExternalSecretStore,
  getExternalSecretRefreshInterval,
  getExternalSecretLastSync,
  getExternalSecretProvider,
  getClusterExternalSecretStatus,
  getClusterExternalSecretNamespaceCount,
  getClusterExternalSecretFailedCount,
  getSecretStoreStatus,
  getSecretStoreProviderType,
  getClusterSecretStoreStatus,
} from '../resource-utils-eso'

export function ExternalSecretCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getExternalSecretStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'store': {
      const store = getExternalSecretStore(resource)
      return (
        <span className="text-sm text-theme-text-secondary truncate block" title={`${store.kind}/${store.name}`}>
          {store.name}
        </span>
      )
    }
    case 'provider': {
      const provider = getExternalSecretProvider(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{provider}</span>
    }
    case 'refreshInterval': {
      const interval = getExternalSecretRefreshInterval(resource)
      return <span className="text-sm text-theme-text-secondary">{interval}</span>
    }
    case 'lastSync': {
      const lastSync = getExternalSecretLastSync(resource)
      return <span className="text-sm text-theme-text-secondary">{lastSync}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterExternalSecretCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getClusterExternalSecretStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'namespaces': {
      const count = getClusterExternalSecretNamespaceCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    case 'failed': {
      const failedCount = getClusterExternalSecretFailedCount(resource)
      if (failedCount > 0) {
        return <span className="text-sm text-red-400 font-medium">{failedCount}</span>
      }
      return <span className="text-sm text-theme-text-secondary">0</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function SecretStoreCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getSecretStoreStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'provider': {
      const provider = getSecretStoreProviderType(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{provider}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ClusterSecretStoreCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getClusterSecretStoreStatus(resource)
      return (
        <span className={clsx('inline-flex items-center px-2 py-0.5 rounded text-xs font-medium', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'provider': {
      const provider = getSecretStoreProviderType(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{provider}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
