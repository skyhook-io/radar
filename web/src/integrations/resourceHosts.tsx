import { useMemo } from 'react'
import { useLocation } from 'react-router-dom'
import { PaneLoader, type WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../api/client'
import { apiVersionToGroup } from '../utils/navigation'
import type { RenderDiagnoseAction } from '../context/DiagnoseCustomization'
import type { SelectedResource } from '../types'
import { cnpgResourceHost } from '../components/cnpg/host'
import {
  composeDetailSlots, composeResourceHosts, hostsForResource, hostSupport, resourceHostForSlot,
  type DetailSlots, type HostFeatures, type HostResource, type HostKind,
  type KindListProps, type ResourceHost,
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

export function useResourceHostFeatures(): HostFeatures {
  // Hook order is fixed here; selected adapters mount components that own their data hooks.
  const { support } = useRadarFeature('cnpgWorkspace')
  return useMemo(() => ({ cnpgWorkspace: support }), [support])
}

export function resourceExpandedPath(resource: HostResource, features: HostFeatures, context?: string, tab?: 'yaml'): string | null {
  return resourceHostForSlot(resourceHosts, resource, 'destination', features)?.expandedPath?.(resource, context, tab) ?? null
}

export function resourceDetailRedirect(resource: HostResource, features: HostFeatures, search: URLSearchParams): string | null {
  return resourceHostForSlot(resourceHosts, resource, 'destination', features)?.detailRedirect?.(resource, search) ?? null
}

export function resourceDetailSlots(resource: HostResource, features: HostFeatures, inheritedTabs?: WorkloadExtraTab[]): DetailSlots {
  const slots = hostsForResource(resourceHosts, resource)
    .filter(host => host.canRender?.(hostSupport(host, features)) ?? true)
    .map(host => ({ id: host.id, slots: host.detailSlots?.(resource) }))
  return composeDetailSlots(slots, inheritedTabs)
}

export function decorateResourceDiagnose(render: RenderDiagnoseAction | undefined): RenderDiagnoseAction | undefined {
  return resourceHosts.reduce((current, host) => host.diagnoseAction?.(current) ?? current, render)
}

export function resourceLogs(resource: HostResource, data: { apiVersion?: string } | null | undefined, features: HostFeatures) {
  const observed = { ...resource, group: data?.apiVersion ? apiVersionToGroup(data.apiVersion) : resource.group }
  const host = resourceHostForSlot(resourceHosts, observed, 'logs', features)
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

function kindListHost(kind: Pick<HostKind, 'name' | 'group'> | null) {
  return kind ? resourceHosts.flatMap(host => (host.kindLists ?? []).map(list => ({ host, list })))
    .find(({ list }) => list.kind.name === kind.name && list.kind.group === kind.group) : undefined
}

export function resourceKindListMode(kind: Pick<HostKind, 'name' | 'group'> | null, features: HostFeatures, pending: boolean) {
  const contribution = kindListHost(kind)
  return contribution?.list.mode(hostSupport(contribution.host, features), pending) ?? 'table'
}

export function resourceKindListTitle(kind: Pick<HostKind, 'name' | 'group'>): string | null {
  return kindListHost(kind)?.list.title ?? null
}

export function renderResourceKindList(kind: Pick<HostKind, 'name' | 'group'> | null, features: HostFeatures, pending: boolean, props: KindListProps) {
  const contribution = kindListHost(kind)
  if (!contribution) return null
  const mode = contribution.list.mode(hostSupport(contribution.host, features), pending)
  if (mode === 'wait') return <PaneLoader label="Loading…" className="flex-1" />
  if (mode === 'table') return null
  const Component = contribution.list.Component
  return <Component {...props} />
}
