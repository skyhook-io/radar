import { isApiGroup } from '@skyhook-io/k8s-ui'
import type { ResourceHost } from '../../integrations/resourceHost'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { CNPGClusterLogs } from './CNPGClusterLogs'
import { CNPGDrawerTrailBack } from './CNPGDrawerTrail'
import { renderCNPGSummary } from './CNPGSummaryHost'
import { CNPG_DETAIL_KINDS, cnpgDetailKindFor, cnpgDetailPath } from './routes'
import { CNPGObjectStoreRenderer } from '../resources/renderers/CNPGObjectStoreRenderer'
import { CNPGClusterRenderer } from '../resources/renderers/CNPGClusterRenderer'
import { CNPGImageCatalogRenderer } from '../resources/renderers/CNPGImageCatalogRenderer'
import { CNPGDatabaseRenderer, CNPGPublicationRenderer, CNPGSubscriptionRenderer } from '../resources/renderers/CNPGDeclarativeRenderer'

export const cnpgResourceHost: ResourceHost = {
  id: 'cnpg',
  renderers: { CNPGObjectStoreRenderer, CNPGClusterRenderer, CNPGDatabaseRenderer, CNPGPublicationRenderer, CNPGSubscriptionRenderer, CNPGImageCatalogRenderer },
  resources: Object.entries(CNPG_DETAIL_KINDS).map(([name, kind]) => ({ name, group: kind.group })),
  detailOwnership: ['summary', 'destination', 'logs'],
  expandedPath(resource, context, tab) {
    const plural = cnpgDetailKindFor(kindToPluralWithGroup(resource.kind, resource.group ?? ''), resource.group)
    return plural ? cnpgDetailPath({ plural, namespace: resource.namespace, name: resource.name }, context, tab) : null
  },
  detailRedirect(resource, search) {
    const plural = cnpgDetailKindFor(kindToPluralWithGroup(resource.kind, resource.group ?? ''), resource.group)
    if (!plural) return null
    const params = new URLSearchParams(search)
    params.delete('apiGroup')
    const tab = params.get('tab')
    if (plural === 'clusters' && (tab === 'timeline' || tab === 'events')) params.set('tab', 'activity')
    const base = cnpgDetailPath({ plural, namespace: resource.namespace, name: resource.name })
    const qs = params.toString()
    return qs ? `${base}?${qs}` : base
  },
  detailSlots: resource => ({ renderSummary: props => renderCNPGSummary({ ...props, group: resource.group }) }),
  logs: (resource, data) => kindToPluralWithGroup(resource.kind, resource.group ?? '') === 'clusters' && isApiGroup(data?.apiVersion, 'postgresql.cnpg.io')
    ? <CNPGClusterLogs namespace={resource.namespace} name={resource.name} /> : null,
  drawerNavigation: {
    ownsPath: pathname => pathname === '/cnpg' || pathname.startsWith('/cnpg/'),
    Component: CNPGDrawerTrailBack,
  },
}
