import type { DetailSlots, HostResource, KindListProps, ResourceHost } from '../../integrations/resourceHost'
import { PoolHeaderBadge, SubstratePoolSummary } from './SubstratePoolSummary'
import type { SelectedResource } from '../../types'
import { useSubstrateStatus, useSubstrateWorkspace } from '../../api/substrateWorkspace'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { ScreenBody } from '../workspace/layout'
import { PoolsTable } from './SubstrateOverview'
import { SubstrateCoverageNotice, SubstrateHeader, SubstrateScreenGate, SubstrateSourceLine } from './shared'
import {
  SUBSTRATE_GROUP,
  SUBSTRATE_NAMESPACE_FILTER_TOOLTIP,
  SUBSTRATE_PATH,
  SUBSTRATE_WORKERPOOL_KIND,
  substrateKindListMode,
  substratePoolPath,
} from './routes'

const KIND_BY_PLURAL: Record<string, string> = { workerpools: 'WorkerPool', pods: 'Pod', nodes: 'Node' }

function SubstrateDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  return <DrawerTrailBack resource={resource} pathPrefix={SUBSTRATE_PATH} kindLabel={(plural) => KIND_BY_PLURAL[plural]} />
}

function isWorkerPool(resource: HostResource): boolean {
  return resource.group === SUBSTRATE_GROUP && kindToPluralWithGroup(resource.kind, resource.group) === SUBSTRATE_WORKERPOOL_KIND.name
}

/**
 * The WorkerPool kind's list joins Kubernetes and Substrate across namespaces.
 */
function SubstratePoolsKindList({ inspected, onInspect }: KindListProps) {
  const query = useSubstrateWorkspace()
  const status = useSubstrateStatus()
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
      <SubstrateScreenGate query={query} discoveryPending={false} discovered>
        {(data) => (
          <div className="flex min-h-0 flex-1 flex-col">
            <SubstrateHeader title="Pools" intro="Cluster-wide" />
            <ScreenBody>
              <SubstrateCoverageNotice coverage={data.coverage} kinds={['workers', 'actors']} />
              <PoolsTable data={data} onInspect={onInspect} inspected={inspected} />
              <SubstrateSourceLine tier={data.apiTier} status={status.data} />
            </ScreenBody>
          </div>
        )}
      </SubstrateScreenGate>
    </div>
  )
}

export const substrateResourceHost: ResourceHost = {
  id: 'substrate',
  feature: 'substrateWorkspace',
  drawerNavigation: {
    ownsPath: (pathname) => pathname === SUBSTRATE_PATH || pathname.startsWith(`${SUBSTRATE_PATH}/`),
    Component: SubstrateDrawerTrailBack,
  },
  resources: [{ name: SUBSTRATE_WORKERPOOL_KIND.name, group: SUBSTRATE_GROUP }],
  // A WorkerPool has a page of its own in the workspace: "Open full view" and
  // links to the generic detail land there.
  expandedPath: (resource, context, tab) => (isWorkerPool(resource) ? substratePoolPath({ namespace: resource.namespace, name: resource.name }, context, tab) : null),
  detailRedirect: (resource, search) => {
    if (!isWorkerPool(resource)) return null
    const params = new URLSearchParams(search)
    params.delete('apiGroup')
    const base = substratePoolPath({ namespace: resource.namespace, name: resource.name })
    const qs = params.toString()
    return qs ? `${base}?${qs}` : base
  },
  detailOwnership: ['summary', 'destination'],
  detailSlots: (resource): DetailSlots => (isWorkerPool(resource)
    ? {
        renderSummary: ({ namespace, name, resource: data, context, onNavigate }) => <SubstratePoolSummary namespace={namespace} name={name} resource={data} context={context} onNavigate={onNavigate} />,
        renderStatusBadge: (data: any) => <PoolHeaderBadge resource={data} />,
      }
    : {}),
  // Radar's pool table is the kind's list, so the sidebar's Pools row stands for it.
  kindLists: [
    { kind: SUBSTRATE_WORKERPOOL_KIND, title: 'Agent Substrate pools', mode: substrateKindListMode, namespaceFilterTooltip: SUBSTRATE_NAMESPACE_FILTER_TOOLTIP, Component: SubstratePoolsKindList },
  ],
}
