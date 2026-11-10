import { isApiGroup } from './resource-utils-cnpg'
import type { ResourceRef } from '../../types'
import { objectReferenceToResourceRef } from '../../utils/navigation'
import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

export function getFlaggerCanaryStatus(data: any): StatusBadge {
  const phase = data.status?.phase
  let text = phase || 'Not reported'
  let level: StatusBadge['level'] = 'unknown'
  if (phase === 'Failed') level = 'unhealthy'
  else if (!phase && data.spec?.suspend === true) { text = 'Suspend requested'; level = 'neutral' }
  else if (phase === 'Succeeded') level = data.spec?.suspend === true ? 'neutral' : 'healthy'
  else if (['Initializing', 'Initialized', 'Waiting', 'Progressing', 'WaitingPromotion', 'Promoting', 'Finalising', 'Terminating', 'Terminated'].includes(phase)) level = 'neutral'
  return { text, level, color: healthColors[level] }
}

// Both fields exist in the served v1beta1 schema. The controller's GetAnalysis
// explicitly prefers analysis and still accepts deprecated canaryAnalysis.
export function getFlaggerAnalysis(data: any): any {
  return data.spec?.analysis ?? data.spec?.canaryAnalysis ?? {}
}

export function getFlaggerStrategy(data: any): string {
  const analysis = getFlaggerAnalysis(data)
  if (analysis.iterations > 0) {
    if (analysis.match?.length > 0) return 'A/B testing'
    return analysis.mirror ? 'Blue/green with mirroring' : 'Blue/green'
  }
  return analysis.mirror ? 'Canary with mirroring' : 'Canary'
}

// Match the pinned controller factory: it chooses its native clients by kind,
// not arbitrary targetRef.apiVersion (except the exact Knative Service case).
export function getFlaggerTargetRef(data: any): ResourceRef | null {
  const target = data.spec?.targetRef
  if (!target?.name) return null
  let apiVersion: string
  if (target.kind === 'Deployment' || target.kind === 'DaemonSet') apiVersion = 'apps/v1'
  else if (target.kind === 'Service') apiVersion = target.apiVersion === 'serving.knative.dev/v1' ? target.apiVersion : 'v1'
  else return null
  return objectReferenceToResourceRef({ ...target, apiVersion, namespace: data.metadata?.namespace })
}

export function getFlaggerAutoscalerRef(data: any): ResourceRef | null {
  const scaler = data.spec?.autoscalerRef
  const apiVersion = scaler?.kind === 'HorizontalPodAutoscaler' ? 'autoscaling/v2' : scaler?.kind === 'ScaledObject' ? 'keda.sh/v1alpha1' : null
  return apiVersion ? objectReferenceToResourceRef({ ...scaler, apiVersion, namespace: data.metadata?.namespace }) : null
}

// scheduler_metrics.go reads only templateRef.name/namespace through its typed
// MetricTemplate client. apiVersion/kind are ignored, so do not link elsewhere.
export function getFlaggerMetricTemplateRef(data: any, metric: any): ResourceRef | null {
  return metric.templateRef?.name ? objectReferenceToResourceRef({ apiVersion: 'flagger.app/v1beta1', kind: 'MetricTemplate', name: metric.templateRef.name, namespace: metric.templateRef.namespace || data.metadata?.namespace }) : null
}

export function getFlaggerColumnValue(data: any, column: string): string | undefined {
  if (!isApiGroup(data?.apiVersion, 'flagger.app')) return undefined
  if (data.kind === 'Canary') {
    switch (column) {
      case 'status': return getFlaggerCanaryStatus(data).text
      case 'strategy': return getFlaggerStrategy(data)
      case 'target': return data.spec?.targetRef?.name ? `${data.spec.targetRef.kind}/${data.spec.targetRef.name}` : ''
      case 'canaryWeight': return getFlaggerAnalysis(data).iterations > 0 ? 'Not weighted' : data.status?.canaryWeight != null ? `${data.status.canaryWeight}%` : 'Not reported'
      case 'suspended': return data.spec?.suspend === true ? 'Yes' : 'No'
    }
  }
  if (data.kind === 'MetricTemplate') {
    switch (column) {
      case 'status': return 'Configured'
      case 'provider': return data.spec?.provider?.type ?? ''
      case 'address': return data.spec?.provider?.address ?? ''
    }
  }
  return undefined
}
