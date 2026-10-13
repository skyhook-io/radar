import { useLocation } from 'react-router-dom'
import type { WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import { apiVersionToGroup } from '../utils/navigation'
import type { SelectedResource } from '../types'
import { cnpgResourceHost } from '../components/cnpg/host'
import {
  composeDetailSlots, composeResourceHosts, hostsForResource, resourceHostForSlot,
  type DetailSlots, type HostResource,
  type ResourceHost,
} from './resourceHost'
import { RayJobRenderer } from '../components/resources/renderers/RayJobRenderer'
import { JobRenderer, JobSetRenderer } from '../components/resources/renderers/JobAdmissionRenderers'
import { RayClusterRenderer } from '../components/resources/renderers/RayClusterRenderer'
import { RayServiceRenderer } from '../components/resources/renderers/RayServiceRenderer'
import { KueueWorkloadRenderer } from '../components/resources/renderers/KueueWorkloadRenderer'
import { PodRenderer } from '../components/resources/renderers/PodRenderer'
import { KarpenterNodePoolRenderer } from '../components/resources/renderers/KarpenterNodePoolRenderer'
import { NodeRenderer } from '../components/resources/renderers/NodeRenderer'
import { ServiceRenderer } from '../components/resources/renderers/ServiceRenderer'
import { WorkloadRenderer } from '../components/resources/renderers/WorkloadRenderer'
import { CompositeRenderer } from '../components/resources/CompositeRenderer'
import { ServiceAccountRenderer } from '../components/resources/renderers/ServiceAccountRenderer'
import { RoleRenderer } from '../components/resources/renderers/RoleRenderer'
import { RoleBindingRenderer } from '../components/resources/renderers/RoleBindingRenderer'
import { NamespaceRenderer } from '../components/resources/renderers/NamespaceRenderer'
import { CAPIClusterRenderer } from '../components/resources/renderers/CAPIClusterRenderer'
import { HPARenderer } from '../components/resources/renderers/HPARenderer'
import { PVCRenderer } from '../components/resources/renderers/PVCRenderer'
import { RolloutRenderer } from '../components/resources/renderers/RolloutRenderer'
import { KyvernoPolicyCoverage } from '../components/resources/renderers/KyvernoPolicyCoverage'
import { KyvernoPolicyQueued } from '../components/resources/renderers/KyvernoPolicyQueued'
import { VeleroBSLRenderer } from '../components/resources/renderers/VeleroBSLRenderer'
import { VeleroBackupRenderer } from '../components/resources/renderers/VeleroBackupRenderer'
import { VeleroRestoreRenderer } from '../components/resources/renderers/VeleroRestoreRenderer'

export const resourceHosts: readonly ResourceHost[] = [
  { id: 'kubernetes', renderers: { PodRenderer, NodeRenderer, ServiceRenderer, WorkloadRenderer, HPARenderer, PVCRenderer } },
  { id: 'rbac', renderers: { ServiceAccountRenderer, RoleRenderer, RoleBindingRenderer, NamespaceRenderer } },
  { id: 'batch-and-admission', renderers: { JobRenderer, JobSetRenderer, KueueWorkloadRenderer } },
  { id: 'ray', renderers: { RayJobRenderer, RayServiceRenderer, RayClusterRenderer } },
  { id: 'capacity', renderers: { KarpenterNodePoolRenderer } },
  { id: 'capi', renderers: { CAPIClusterRenderer } },
  { id: 'crossplane', renderers: { CompositeRenderer } },
  { id: 'rollouts', renderers: { RolloutRenderer } },
  { id: 'kyverno', renderers: { KyvernoPolicyCoverage, KyvernoPolicyQueued } },
  { id: 'velero', renderers: { VeleroBSLRenderer, VeleroBackupRenderer, VeleroRestoreRenderer } },
  cnpgResourceHost,
]

export const resourceRendererOverrides = composeResourceHosts(resourceHosts)

export function resourceExpandedPath(resource: HostResource, context?: string, tab?: 'yaml'): string | null {
  return resourceHostForSlot(resourceHosts, resource, 'destination')?.expandedPath?.(resource, context, tab) ?? null
}

export function resourceDetailRedirect(resource: HostResource, search: URLSearchParams): string | null {
  return resourceHostForSlot(resourceHosts, resource, 'destination')?.detailRedirect?.(resource, search) ?? null
}

export function resourceDetailSlots(resource: HostResource, inheritedTabs?: WorkloadExtraTab[]): DetailSlots {
  const slots = hostsForResource(resourceHosts, resource)
    .map(host => ({ id: host.id, slots: host.detailSlots?.(resource) }))
  return composeDetailSlots(slots, inheritedTabs)
}

export function resourceLogs(resource: HostResource, data: { apiVersion?: string } | null | undefined) {
  const observed = { ...resource, group: data?.apiVersion ? apiVersionToGroup(data.apiVersion) : resource.group }
  const host = resourceHostForSlot(resourceHosts, observed, 'logs')
  return host?.logs?.(observed, data) ?? null
}

export function ResourceDrawerNavigation({ resource }: { resource: SelectedResource }) {
  const { pathname } = useLocation()
  return <>{resourceHosts.map(host => {
    const navigation = host.drawerNavigation
    if (!navigation?.ownsPath(pathname)) return null
    const Component = navigation.Component
    return <Component key={host.id} resource={resource} />
  })}</>
}
