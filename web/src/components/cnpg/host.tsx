import type { ComponentProps } from 'react'
import { PaneLoader, WorkloadView, isApiGroup } from '@skyhook-io/k8s-ui'
import type { RenderDiagnoseAction } from '../../context/DiagnoseCustomization'
import type { SelectedResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import type { RadarFeatureSupport } from '../../api/radarFeatures'
import { CNPGClustersKindView } from './CNPGClustersKindView'
import { CNPGClusterLogs } from './CNPGClusterLogs'
import { CNPGDrawerTrailBack } from './CNPGDrawerTrail'
import { CNPGInvestigationAction } from './CNPGClusterTabs'
import { renderCNPGSummary } from './CNPGSummaryHost'
import { renderCNPGHeaderActions } from './actions/renderCNPGHeaderActions'
import { cnpgClusterKindListMode, cnpgDetailKindFor, cnpgDetailPath } from './routes'

type DetailSlots = Pick<ComponentProps<typeof WorkloadView>, 'renderSummary' | 'renderHeaderActions'>
type HostResource = Pick<SelectedResource, 'kind' | 'group' | 'namespace' | 'name'>

export function useCNPGHostSupport() {
  const { support } = useRadarFeature('cnpgWorkspace')
  return { support, canRedirect: support === 'supported', canRender: support !== 'unsupported' }
}

export const cnpgHost = {
  clusterKind: { name: 'clusters', kind: 'Cluster', group: 'postgresql.cnpg.io' },
  DrawerNavigation: CNPGDrawerTrailBack,
  kindListMode: cnpgClusterKindListMode,

  expandedPath(resource: HostResource, context?: string, tab?: 'yaml'): string | null {
    const plural = cnpgDetailKindFor(resource.kind, resource.group)
    return plural ? cnpgDetailPath({ plural, namespace: resource.namespace, name: resource.name }, context, tab) : null
  },

  detailRedirect(resource: HostResource, search: URLSearchParams): string | null {
    const plural = cnpgDetailKindFor(resource.kind, resource.group)
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

  kindView(kind: Parameters<typeof cnpgClusterKindListMode>[0], support: RadarFeatureSupport, pending: boolean, props: ComponentProps<typeof CNPGClustersKindView>) {
    switch (cnpgClusterKindListMode(kind, support, pending)) {
      case 'view': return <CNPGClustersKindView {...props} />
      case 'wait': return <PaneLoader label="Loading…" className="flex-1" />
      default: return null
    }
  },
}
