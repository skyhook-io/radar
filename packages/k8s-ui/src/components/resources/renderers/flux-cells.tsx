// FluxCD cell components for ResourcesView table — extracted from ResourcesView.tsx

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getGitRepositoryUrl,
  getGitRepositoryRef,
  getGitRepositoryStatus,
  getGitRepositoryRevision,
  getOCIRepositoryUrl,
  getOCIRepositoryRef,
  getOCIRepositoryStatus,
  getOCIRepositoryRevision,
  getHelmRepositoryUrl,
  getHelmRepositoryType,
  getHelmRepositoryStatus,
  getKustomizationSource,
  getKustomizationPath,
  getKustomizationStatus,
  getKustomizationInventory,
  getFluxHelmReleaseChart,
  getFluxHelmReleaseVersion,
  getFluxHelmReleaseStatus,
  getFluxHelmReleaseRevision,
  getFluxAlertProvider,
  getFluxAlertEventCount,
  getFluxAlertStatus,
  formatAge,
} from '../resource-utils'

export function GitRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getGitRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'ref': {
      const ref = getGitRepositoryRef(resource)
      return <span className="text-sm text-theme-text-secondary">{ref}</span>
    }
    case 'status': {
      const status = getGitRepositoryStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getGitRepositoryRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary font-mono">{revision}</span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function OCIRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getOCIRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'ref': {
      const ref = getOCIRepositoryRef(resource)
      return <span className="text-sm text-theme-text-secondary">{ref}</span>
    }
    case 'status': {
      const status = getOCIRepositoryStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getOCIRepositoryRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary font-mono">{revision}</span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function HelmRepositoryCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'url': {
      const url = getHelmRepositoryUrl(resource)
      return (
        <Tooltip content={url}>
          <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
        </Tooltip>
      )
    }
    case 'type': {
      const type = getHelmRepositoryType(resource)
      return <span className="text-sm text-theme-text-secondary">{type}</span>
    }
    case 'status': {
      const status = getHelmRepositoryStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function KustomizationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'source': {
      const source = getKustomizationSource(resource)
      return (
        <Tooltip content={source}>
          <span className="text-sm text-theme-text-secondary truncate block">{source}</span>
        </Tooltip>
      )
    }
    case 'path': {
      const path = getKustomizationPath(resource)
      return <span className="text-sm text-theme-text-tertiary font-mono">{path}</span>
    }
    case 'status': {
      const status = getKustomizationStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = resource.status?.lastAppliedRevision || resource.status?.lastAttemptedRevision || ''
      if (!revision) return <span className="text-sm text-theme-text-tertiary">-</span>
      // Format: "refs/heads/main@sha1:abc123..." -> "main@abc123"
      const formatted = revision
        .replace(/^refs\/heads\//, '')
        .replace(/^refs\/tags\//, '')
        .replace(/@sha1:([a-f0-9]{8})[a-f0-9]*$/, '@$1')
      return (
        <Tooltip content={revision}>
          <span className="text-sm text-theme-text-secondary font-mono truncate block">{formatted}</span>
        </Tooltip>
      )
    }
    case 'inventory': {
      const count = getKustomizationInventory(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'lastUpdated': {
      // Kustomization uses history[0].lastReconciled or conditions[Ready].lastTransitionTime
      const lastReconcile = resource.status?.history?.[0]?.lastReconciled ||
        resource.status?.conditions?.find((c: any) => c.type === 'Ready')?.lastTransitionTime
      return (
        <span className="text-sm text-theme-text-secondary">
          {lastReconcile ? formatAge(lastReconcile) : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function FluxHelmReleaseCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'chart': {
      const chart = getFluxHelmReleaseChart(resource)
      return (
        <Tooltip content={chart}>
          <span className="text-sm text-theme-text-secondary truncate block">{chart}</span>
        </Tooltip>
      )
    }
    case 'version': {
      const version = getFluxHelmReleaseVersion(resource)
      return <span className="text-sm text-theme-text-secondary">{version}</span>
    }
    case 'status': {
      const status = getFluxHelmReleaseStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'revision': {
      const revision = getFluxHelmReleaseRevision(resource)
      return (
        <span className="text-sm text-theme-text-tertiary">
          {revision > 0 ? `#${revision}` : '-'}
        </span>
      )
    }
    case 'lastUpdated': {
      // HelmRelease uses history[0].lastDeployed or conditions[Ready].lastTransitionTime
      const lastReconcile = resource.status?.history?.[0]?.lastDeployed ||
        resource.status?.conditions?.find((c: any) => c.type === 'Ready')?.lastTransitionTime
      return (
        <span className="text-sm text-theme-text-secondary">
          {lastReconcile ? formatAge(lastReconcile) : '-'}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function FluxAlertCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'provider': {
      const provider = getFluxAlertProvider(resource)
      return <span className="text-sm text-theme-text-secondary">{provider}</span>
    }
    case 'events': {
      const count = getFluxAlertEventCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'status': {
      const status = getFluxAlertStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
