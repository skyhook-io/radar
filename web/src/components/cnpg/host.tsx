import { isApiGroup } from '@skyhook-io/k8s-ui'
import type { RenderDiagnoseAction } from '../../context/DiagnoseCustomization'
import { CNPGClustersKindView } from './CNPGClustersKindView'
import { CNPGClusterLogs } from './CNPGClusterLogs'
import { CNPGDrawerTrailBack } from './CNPGDrawerTrail'
import { CNPGInvestigationAction } from './CNPGClusterTabs'
import { renderCNPGSummary } from './CNPGSummaryHost'
import { renderCNPGHeaderActions } from './actions/renderCNPGHeaderActions'
import { CNPG_DETAIL_KINDS, cnpgClusterKindListMode, cnpgDetailKindFor, cnpgDetailPath } from './routes'
import type { DetailSlots, HostResource, KindListProps, ResourceHost } from '../../integrations/resourceHost'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { CNPGObjectStoreRenderer } from '../resources/renderers/CNPGObjectStoreRenderer'
import { CNPGClusterRenderer } from '../resources/renderers/CNPGClusterRenderer'
import { CNPGImageCatalogRenderer } from '../resources/renderers/CNPGImageCatalogRenderer'
import {
  CNPGDatabaseRenderer,
  CNPGPublicationRenderer,
  CNPGSubscriptionRenderer,
} from '../resources/renderers/CNPGDeclarativeRenderer'

export const cnpgHost = {
  clusterKind: { name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io' },

  expandedPath(resource: HostResource, context?: string, tab?: 'yaml'): string | null {
    const plural = cnpgDetailKindFor(kindToPluralWithGroup(resource.kind, resource.group ?? ''), resource.group)
    return plural ? cnpgDetailPath({ plural, namespace: resource.namespace, name: resource.name }, context, tab) : null
  },

  detailRedirect(resource: HostResource, search: URLSearchParams): string | null {
    const plural = cnpgDetailKindFor(kindToPluralWithGroup(resource.kind, resource.group ?? ''), resource.group)
    if (!plural) return null
    const params = new URLSearchParams(search)
    params.delete('apiGroup')
    const tab = params.get('tab')
    if (plural === 'clusters' && (tab === 'timeline' || tab === 'events')) params.set('tab', 'activity')
    const base = cnpgDetailPath({ plural, namespace: resource.namespace, name: resource.name })
    return `${base}${params.size ? `?${params}` : ''}`
  },

  detailSlots(namespace: string, name: string, group?: string): DetailSlots {
    return {
      renderHeaderActions: ({ resource, context, onNavigate }) => renderCNPGHeaderActions({ resource, namespace, name, compact: context === 'drawer', onNavigate }),
      renderSummary: (props) => renderCNPGSummary({ ...props, group }),
    }
  },

  diagnoseAction(render: RenderDiagnoseAction | undefined): RenderDiagnoseAction | undefined {
    return render ? (context) => context.kind === 'Cluster' && context.group === 'postgresql.cnpg.io'
      ? <CNPGInvestigationAction namespace={context.namespace} name={context.name} render={render} context={context} />
      : render(context) : undefined
  },

  logs(kind: string, resource: { apiVersion?: string } | null | undefined, namespace: string, name: string) {
    return kind === 'Cluster' && isApiGroup(resource?.apiVersion, 'postgresql.cnpg.io') ? <CNPGClusterLogs namespace={namespace} name={name} /> : null
  },
}

function CNPGKindList({ onCreate, ...props }: KindListProps) {
  return <CNPGClustersKindView {...props} onCreate={() => onCreate(cnpgHost.clusterKind)} />
}

export const cnpgResourceHost: ResourceHost = {
  id: 'cnpg',
  renderers: {
    CNPGObjectStoreRenderer, CNPGClusterRenderer, CNPGDatabaseRenderer,
    CNPGPublicationRenderer, CNPGSubscriptionRenderer, CNPGImageCatalogRenderer,
  },
  resources: Object.entries(CNPG_DETAIL_KINDS).map(([name, kind]) => ({ name, group: kind.group })),
  feature: 'cnpgWorkspace',
  canRender: support => support !== 'unsupported',
  canRedirect: support => support === 'supported',
  expandedPath: cnpgHost.expandedPath,
  detailRedirect: cnpgHost.detailRedirect,
  detailOwnership: ['summary', 'destination', 'logs'],
  detailSlots: resource => cnpgHost.detailSlots(resource.namespace, resource.name, resource.group),
  diagnoseAction: cnpgHost.diagnoseAction,
  logs: (resource, data) => kindToPluralWithGroup(resource.kind, resource.group ?? '') === 'clusters' ? cnpgHost.logs('Cluster', data, resource.namespace, resource.name) : null,
  drawerNavigation: {
    ownsPath: pathname => pathname === '/cnpg' || pathname.startsWith('/cnpg/'),
    Component: CNPGDrawerTrailBack,
  },
  kindLists: [{
    kind: cnpgHost.clusterKind,
    title: 'CloudNativePG Clusters',
    mode: (support, pending) => cnpgClusterKindListMode(cnpgHost.clusterKind, support, pending),
    Component: CNPGKindList,
  }],
}
