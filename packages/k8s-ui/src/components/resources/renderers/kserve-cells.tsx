// KServe cell components for ResourcesView table

import { clsx } from 'clsx'
import {
  getInferenceServiceStatus,
  getInferenceServiceUrl,
  getInferenceServiceModelFormat,
  getInferenceServiceRuntime,
  getInferenceServiceDeploymentMode,
  getServingRuntimeStatus,
  getServingRuntimeModelFormats,
  getServingRuntimeImage,
  getInferenceGraphStatus,
  getInferenceGraphNodeCount,
  getTrainedModelStatus,
  getTrainedModelFramework,
  getTrainedModelStorageUri,
  getTrainedModelInferenceService,
  getLLMInferenceServiceStatus,
  getLLMInferenceServiceModel,
  getLLMInferenceServiceReplicas,
} from '../resource-utils-kserve'

export function InferenceServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getInferenceServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'url': {
      const url = getInferenceServiceUrl(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{url}</span>
    }
    case 'modelFormat': {
      const format = getInferenceServiceModelFormat(resource)
      return <span className="text-sm text-theme-text-secondary">{format}</span>
    }
    case 'runtime': {
      const runtime = getInferenceServiceRuntime(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{runtime}</span>
    }
    case 'deploymentMode': {
      const mode = getInferenceServiceDeploymentMode(resource)
      return <span className="text-sm text-theme-text-secondary">{mode}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function ServingRuntimeCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getServingRuntimeStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'modelFormats': {
      const formats = getServingRuntimeModelFormats(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{formats}</span>
    }
    case 'image': {
      const image = getServingRuntimeImage(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{image}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function InferenceGraphCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getInferenceGraphStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'nodes': {
      const count = getInferenceGraphNodeCount(resource)
      return <span className="text-sm text-theme-text-secondary">{count > 0 ? count : '-'}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function TrainedModelCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getTrainedModelStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'framework': {
      const framework = getTrainedModelFramework(resource)
      return <span className="text-sm text-theme-text-secondary">{framework}</span>
    }
    case 'storageUri': {
      const uri = getTrainedModelStorageUri(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{uri}</span>
    }
    case 'inferenceService': {
      const parent = getTrainedModelInferenceService(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{parent}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}

export function LLMInferenceServiceCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getLLMInferenceServiceStatus(resource)
      return (
        <span className={clsx('badge', status.color)}>
          {status.text}
        </span>
      )
    }
    case 'model': {
      const model = getLLMInferenceServiceModel(resource)
      return <span className="text-sm text-theme-text-secondary truncate block">{model}</span>
    }
    case 'replicas': {
      const replicas = getLLMInferenceServiceReplicas(resource)
      return <span className="text-sm text-theme-text-secondary">{replicas}</span>
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>
  }
}
