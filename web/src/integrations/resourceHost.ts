import { createElement, Fragment, type ComponentProps, type ComponentType, type ReactNode } from 'react'
import type { RendererOverrides, WorkloadExtraTab, WorkloadView } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../types'
import { kindToPluralWithGroup } from '../utils/navigation'

export type HostResource = Pick<SelectedResource, 'kind' | 'group' | 'namespace' | 'name'>
export type DetailSlots = Pick<ComponentProps<typeof WorkloadView>, 'renderSummary' | 'renderHeaderActions' | 'extraTabs'>
type ExclusiveDetailSlot = 'summary' | 'destination' | 'logs'
export interface ResourceHost {
  id: string
  renderers?: RendererOverrides
  resources?: readonly { name: string; group: string }[]
  expandedPath?: (resource: HostResource, context?: string, tab?: 'yaml') => string | null
  detailRedirect?: (resource: HostResource, search: URLSearchParams) => string | null
  detailSlots?: (resource: HostResource) => DetailSlots
  detailOwnership?: readonly ExclusiveDetailSlot[]
  logs?: (resource: HostResource, data: { apiVersion?: string } | null | undefined) => ReactNode
  drawerNavigation?: {
    ownsPath: (pathname: string) => boolean
    Component: ComponentType<{ resource: SelectedResource }>
  }
}

function resourceKey(name: string, group: string): string { return `${group}\u0000${name}` }

export function hostsForResource(hosts: readonly ResourceHost[], resource: HostResource): ResourceHost[] {
  // An unresolved group must not select a colliding CRD, or be treated as core.
  if (resource.group === undefined) return []
  const plural = kindToPluralWithGroup(resource.kind, resource.group)
  return hosts.filter(host => host.resources?.some(kind => kind.name === plural && kind.group === resource.group))
}

export function resourceHostForSlot(hosts: readonly ResourceHost[], resource: HostResource, slot: ExclusiveDetailSlot): ResourceHost | undefined {
  return hostsForResource(hosts, resource).find(host => host.detailOwnership?.includes(slot))
}

export function composeResourceHosts(hosts: readonly ResourceHost[]): RendererOverrides {
  const renderers: RendererOverrides = {}
  const owners = new Map<string, string>()
  const claim = (key: string, id: string) => {
    const previous = owners.get(key)
    if (previous) throw new Error(`Resource host ownership conflict: ${previous} and ${id} own ${key}`)
    owners.set(key, id)
  }
  for (const host of hosts) {
    claim(`host:${host.id}`, host.id)
    if ((host.expandedPath || host.detailRedirect) && !host.detailOwnership?.includes('destination')) throw new Error(`Resource host ${host.id} must declare destination ownership`)
    if (host.logs && !host.detailOwnership?.includes('logs')) throw new Error(`Resource host ${host.id} must declare logs ownership`)
    for (const name of Object.keys(host.renderers ?? {})) claim(`renderer:${name}`, host.id)
    Object.assign(renderers, host.renderers)
    for (const kind of host.resources ?? []) {
      for (const slot of host.detailOwnership ?? []) claim(`${slot}:${resourceKey(kind.name, kind.group)}`, host.id)
    }
  }
  return renderers
}

export function composeDetailSlots(contributions: readonly { id: string; slots: DetailSlots | undefined }[], inheritedTabs: WorkloadExtraTab[] = []): DetailSlots {
  const summaries = contributions.filter(host => host.slots?.renderSummary)
  if (summaries.length > 1) throw new Error(`Resource host summary ownership conflict: ${summaries.map(host => host.id).join(', ')}`)
  const summary = summaries[0]?.slots?.renderSummary
  const actions = contributions.filter(host => host.slots?.renderHeaderActions)
  const tabs = [...inheritedTabs, ...contributions.flatMap(host => host.slots?.extraTabs ?? [])]
  const tabIds = new Set(inheritedTabs.map(tab => tab.id))
  for (const tab of contributions.flatMap(host => host.slots?.extraTabs ?? [])) {
    if (tabIds.has(tab.id)) throw new Error(`Resource host tab ownership conflict: ${tab.id}`)
    tabIds.add(tab.id)
  }
  return {
    ...(summary ? { renderSummary: summary } : {}),
    ...(actions.length ? { renderHeaderActions: (props: Parameters<NonNullable<DetailSlots['renderHeaderActions']>>[0]) => actions.map(host => createElement(Fragment, { key: host.id }, host.slots!.renderHeaderActions!(props))) } : {}),
    ...(tabs.length ? { extraTabs: tabs } : {}),
  }
}
