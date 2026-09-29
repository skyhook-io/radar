import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_GROUP,
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
import { useCNPGRuntime, type CNPGRuntimeResponse } from '../../api/cnpg'
import type { CNPGFleetRow } from '@skyhook-io/k8s-ui'

// Replaces the Kubernetes-only replication fact with the primary's
// pg_stat_replication when it has been read; otherwise keeps "lag unknown".
function withLiveReplication(row: CNPGFleetRow, rt: CNPGRuntimeResponse | undefined): CNPGFleetRow {
  const primary = rt?.instances.find((i) => i.role === 'primary')
  if (!primary || primary.status.state !== 'ok' || row.replication.text === 'Single instance' || row.replication.text === 'Hibernated') return row
  const reps = primary.status.replication ?? []
  const standbys = row.pods.filter((p) => p.role === 'replica').length
  const streaming = reps.filter((r) => r.state === 'streaming').length
  const lags = reps.map((r) => r.replayLag).filter((v): v is number => v !== undefined)
  const maxLag = lags.length ? Math.max(...lags) : undefined
  const tone = streaming < standbys ? 'degraded' : maxLag !== undefined && maxLag >= 30 ? 'unhealthy' : maxLag !== undefined && maxLag >= 5 ? 'degraded' : 'healthy'
  return {
    ...row,
    replication: {
      text: `${streaming}/${standbys} streaming${maxLag !== undefined ? ` · max replay lag ${maxLag < 1 ? `${Math.round(maxLag * 1000)} ms` : `${maxLag.toFixed(1)} s`}` : ''}`,
      tone,
      source: 'From the primary’s pg_stat_replication via the instance manager',
      at: primary.status.capturedAt,
    },
  }
}
import { cnpgClusterFullPath, currentPageLabel } from './paths'
import { useConnection } from '../../context/ConnectionContext'

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
  const { connection } = useConnection()
  // The workspace is read for the object's own namespace: an explicitly opened
  // Cluster shows its facts whatever the namespace filter is.
  const { query, fleet } = useCNPGFleet([namespace])
  const runtime = useCNPGRuntime(namespace, name)
  const baseRow = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const row = baseRow ? withLiveReplication(baseRow, runtime.data) : undefined
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
      actions={
        context === 'drawer'
          ? [
              {
                label: 'Open cluster',
                primary: true,
                onClick: () =>
                  navigate(cnpgClusterFullPath(namespace, name, connection.context || undefined), {
                    state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                  }),
              },
              {
                label: 'Logs',
                onClick: () =>
                  navigate(cnpgClusterFullPath(namespace, name, connection.context || undefined, 'logs'), {
                    state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                  }),
              },
              {
                label: 'Protection',
                onClick: () =>
                  navigate(cnpgClusterFullPath(namespace, name, connection.context || undefined, 'protection'), {
                    state: { returnLabel: currentPageLabel(), returnCtx: connection.context },
                  }),
              },
            ]
          : undefined
      }
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

/**
 * The composed Overview for CloudNativePG kinds. Returns null for kinds
 * without one, which keeps the default Overview.
 */
export function renderCNPGSummary(ctx: SummaryContext): ReactNode {
  return renderSummaryFor(ctx)
}
