import type { ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_GROUP,
  CNPG_KIND_BY_KEY,
  CNPGBackupSummary,
  CNPGClusterSummary,
  CNPGDatabaseSummary,
  CNPGImageCatalogSummary,
  CNPGObjectStoreSummary,
  CNPGPoolerSummary,
  CNPGPublicationSummary,
  CNPGScheduledBackupSummary,
  CNPGSubscriptionSummary,
  PaneLoader,
  isApiGroup,
  refToSelectedResource,
  type CNPGNavigate,
  type CNPGRef,
  type CNPGWorkspaceResponse,
  type NavigateToResource,
} from '@skyhook-io/k8s-ui'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { cnpgClusterFullPath } from './paths'
import { decodeDrawerTrail, encodeDrawerTrail } from './routes'

interface SummaryContext {
  apiKind: string
  namespace: string
  name: string
  group?: string
  resource: any
  context: 'drawer' | 'expanded'
  onNavigate?: NavigateToResource
}

function ClusterSummaryHost({ namespace, name, context, onNavigate }: SummaryContext) {
  const navigate = useNavigate()
  // The workspace is read for the object's own namespace: an explicitly opened
  // Cluster shows its facts whatever the namespace filter is.
  const { query, fleet } = useCNPGFleet([namespace])
  const row = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  if (!row) {
    if (query.isLoading) return <PaneLoader label="Loading summary…" className="h-40" />
    return (
      <div className="px-4 py-4 text-sm text-theme-text-secondary">
        {query.error instanceof Error
          ? `The CloudNativePG summary could not be loaded: ${query.error.message}`
          : 'This Cluster is not in the CloudNativePG workspace for your identity.'}{' '}
        Spec & status still shows everything the object reports.
      </div>
    )
  }
  const go = onNavigate ? (ref: CNPGRef) => onNavigate(refToSelectedResource(ref)) : undefined
  return (
    <CNPGClusterSummary
      row={row}
      onNavigate={go}
      actions={context === 'drawer' ? [{ label: 'Open cluster', primary: true, onClick: () => navigate(cnpgClusterFullPath(namespace, name)) }] : undefined}
    />
  )
}

type ObjectSummary = (props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) => ReactNode

const OBJECT_SUMMARIES: Record<string, ObjectSummary> = {
  Backup: CNPGBackupSummary,
  ScheduledBackup: CNPGScheduledBackupSummary,
  Pooler: CNPGPoolerSummary,
  Database: CNPGDatabaseSummary,
  Publication: CNPGPublicationSummary,
  Subscription: CNPGSubscriptionSummary,
  ImageCatalog: CNPGImageCatalogSummary,
  ClusterImageCatalog: CNPGImageCatalogSummary,
}

function ObjectSummaryHost({ ctx, Summary }: { ctx: SummaryContext; Summary: ObjectSummary }) {
  // A ClusterImageCatalog is referenced from any namespace, so its users are
  // read across every namespace the caller can see.
  const clusterScoped = ctx.resource?.kind === 'ClusterImageCatalog'
  const { query } = useCNPGFleet(clusterScoped ? [] : [ctx.namespace])
  if (query.isLoading) return <PaneLoader label="Loading summary…" className="h-40" />
  const workspace = query.data?.installed ? query.data : null
  const go = ctx.onNavigate ? (ref: CNPGRef) => ctx.onNavigate?.(refToSelectedResource(ref)) : undefined
  return <Summary resource={ctx.resource} workspace={workspace} onNavigate={go} />
}

// The object's own apiVersion decides: Velero also ships a Backup kind.
function groupOf(ctx: SummaryContext): string | undefined {
  const apiVersion = ctx.resource?.apiVersion
  if (typeof apiVersion !== 'string') return ctx.group
  if (isApiGroup(apiVersion, CNPG_GROUP)) return CNPG_GROUP
  if (isApiGroup(apiVersion, CNPG_BARMAN_OBJECTSTORE_GROUP)) return CNPG_BARMAN_OBJECTSTORE_GROUP
  return undefined
}

function renderSummaryFor(ctx: SummaryContext): ReactNode {
  const group = groupOf(ctx)
  const kind = ctx.resource?.kind
  if (group === CNPG_BARMAN_OBJECTSTORE_GROUP && kind === 'ObjectStore') {
    return <ObjectSummaryHost ctx={ctx} Summary={CNPGObjectStoreSummary} />
  }
  if (group !== CNPG_GROUP) return null
  if (kind === 'Cluster') return <ClusterSummaryHost {...ctx} />
  const Summary = OBJECT_SUMMARIES[kind]
  return Summary ? <ObjectSummaryHost ctx={ctx} Summary={Summary} /> : null
}

const KIND_BY_PLURAL: Record<string, string> = Object.fromEntries(
  Object.values(CNPG_KIND_BY_KEY).map((k) => [k.plural, k.kind]),
)

// On a workspace screen the drawer URL carries the chain of objects opened
// from inside it; this renders the step back to the previous one.
function DrawerTrailBack({ name, children }: { name: string; children: ReactNode }) {
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const trail = decodeDrawerTrail(searchParams.get('drawer'))
  const current = trail[trail.length - 1]
  if (!location.pathname.startsWith('/cnpg') || trail.length < 2 || current?.name !== name) return <>{children}</>
  const prev = trail[trail.length - 2]
  const back = () => {
    const params = new URLSearchParams(searchParams)
    params.set('drawer', encodeDrawerTrail(trail.slice(0, -1)))
    setSearchParams(params, { replace: true })
  }
  return (
    <>
      <div className="px-4 pt-3">
        <button type="button" onClick={back} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" />
          {KIND_BY_PLURAL[prev.kind] ?? prev.kind} {prev.name}
        </button>
      </div>
      {children}
    </>
  )
}

/**
 * The composed Overview for CloudNativePG kinds. Returns null for kinds
 * without one, which keeps the default Overview.
 */
export function renderCNPGSummary(ctx: SummaryContext): ReactNode {
  const node = renderSummaryFor(ctx)
  if (!node || ctx.context !== 'drawer') return node
  return <DrawerTrailBack name={ctx.name}>{node}</DrawerTrailBack>
}
