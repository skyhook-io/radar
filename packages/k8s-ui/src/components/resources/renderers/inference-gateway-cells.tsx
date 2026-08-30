// Gateway API Inference Extension cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getInferencePoolStatus,
  getInferencePoolSelector,
  getInferencePoolTargetPorts,
  getInferencePoolExtensionRef,
  getInferenceObjectiveStatus,
  getInferenceObjectivePoolRef,
  getInferenceObjectivePriority,
} from '../resource-utils-inference-gateway'

export function InferencePoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getInferencePoolStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'selector': {
      const selector = getInferencePoolSelector(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{selector}</span>
    }
    case 'targetPorts': {
      const ports = getInferencePoolTargetPorts(resource)
      return <span className="text-sm text-theme-text-secondary">{ports}</span>
    }
    case 'extensionRef': {
      const ref = getInferencePoolExtensionRef(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{ref}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function InferenceObjectiveCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getInferenceObjectiveStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'poolRef': {
      const poolRef = getInferenceObjectivePoolRef(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{poolRef}</span>
    }
    case 'priority': {
      const priority = getInferenceObjectivePriority(resource)
      return <span className="text-sm text-theme-text-secondary">{priority}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
