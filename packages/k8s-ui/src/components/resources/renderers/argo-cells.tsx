// ArgoCD cell components for ResourcesView table — extracted from ResourcesView.tsx

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import {
  getArgoApplicationProject,
  getArgoApplicationSync,
  getArgoApplicationHealth,
  getArgoApplicationRepo,
  getArgoApplicationSetGenerators,
  getArgoApplicationSetTemplate,
  getArgoApplicationSetAppCount,
  getArgoApplicationSetStatus,
  getArgoAppProjectDescription,
  getArgoAppProjectDestinations,
  getArgoAppProjectSources,
} from '../resource-utils'

export function ArgoApplicationCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'project': {
      const project = getArgoApplicationProject(resource)
      return <span className="text-sm text-theme-text-secondary">{project}</span>
    }
    case 'sync': {
      const { status, color } = getArgoApplicationSync(resource)
      return (
        <span className={clsx('badge', color)}>
          {status}
        </span>
      )
    }
    case 'health': {
      const { status, color } = getArgoApplicationHealth(resource)
      return (
        <span className={clsx('badge', color)}>
          {status}
        </span>
      )
    }
    case 'repo': {
      const repo = getArgoApplicationRepo(resource)
      return (
        <Tooltip content={resource.spec?.source?.repoURL || resource.spec?.sources?.[0]?.repoURL || repo}>
          <span className="text-sm text-theme-text-secondary truncate block">{repo}</span>
        </Tooltip>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ArgoApplicationSetCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'generators': {
      const generators = getArgoApplicationSetGenerators(resource)
      return (
        <Tooltip content={generators}>
          <span className="text-sm text-theme-text-secondary truncate block">{generators}</span>
        </Tooltip>
      )
    }
    case 'template': {
      const template = getArgoApplicationSetTemplate(resource)
      return <span className="text-sm text-theme-text-secondary">{template}</span>
    }
    case 'applications': {
      const count = getArgoApplicationSetAppCount(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count > 0 ? count : '-'}
        </span>
      )
    }
    case 'status': {
      const status = getArgoApplicationSetStatus(resource)
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

export function ArgoAppProjectCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'description': {
      const desc = getArgoAppProjectDescription(resource)
      return (
        <Tooltip content={desc}>
          <span className="text-sm text-theme-text-secondary truncate block">{desc}</span>
        </Tooltip>
      )
    }
    case 'destinations': {
      const count = getArgoAppProjectDestinations(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count === Infinity ? '*' : count}
        </span>
      )
    }
    case 'sources': {
      const count = getArgoAppProjectSources(resource)
      return (
        <span className="text-sm text-theme-text-secondary">
          {count === Infinity ? '*' : count}
        </span>
      )
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
