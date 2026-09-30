import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_GROUP,
  CNPGBackupSummary,
  CNPGClusterSummary,
  CNPGClusterHASection,
  cnpgDimensions,
  CNPGDatabaseSummary,
  CNPGImageCatalogSummary,
  CNPGObjectStoreSummary,
  CNPGPoolerSummary,
  CNPGPublicationSummary,
  CNPGScheduledBackupSummary,
  CNPGSubscriptionSummary,
  CNPGDatabaseRoleSummary,
  FactRow,
  FactSource,
  FactValue,
  PaneLoader,
  isApiGroup,
  refToSelectedResource,
  type CNPGNavigate,
  type CNPGRef,
  type CNPGWorkspaceResponse,
  type NavigateToResource,
} from '@skyhook-io/k8s-ui'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { useCNPGRuntime, useCNPGScheduleCapabilities, type CNPGRuntimeResponse } from '../../api/cnpg'
import { cnpgBaseBackupFacts, describeCNPGBaseBackup } from './baseBackup'
import { useCNPGPoolerLive } from './useCNPGPoolerLive'
import { cnpgInstanceLive, cnpgReplicationLive, useCNPGClusterHA, withLiveReplication } from '../../api/cnpg-ha'
import { CNPGMaintenanceBanner } from './actions/CNPGMaintenanceBanner'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { CNPGRefreshFailedNotice } from './shared'

import { cnpgClusterFullPath, currentPageLabel } from './paths'
import { CNPGRestoreProgress } from './recovery/CNPGRestoreProgress'
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

function BaseBackupFact({ runtime }: { runtime: CNPGRuntimeResponse | undefined }) {
  const bb = cnpgBaseBackupFacts(runtime)
  if (!bb) return null
  return (
    <FactRow label="Base backup">
      {bb.rows.length > 1 ? (
        <ul className="space-y-0.5">
          {bb.rows.map((r) => (
            <li key={r.applicationName}>{describeCNPGBaseBackup(r)}</li>
          ))}
        </ul>
      ) : (
        <FactValue fact={bb.fact} />
      )}
      <FactSource fact={bb.fact} />
    </FactRow>
  )
}

function ClusterSummaryHost({ namespace, name, context, onNavigate }: SummaryContext) {
  const navigate = useNavigate()
  const { connection } = useConnection()
  // The workspace is read for the object's own namespace: an explicitly opened
  // Cluster shows its facts whatever the namespace filter is.
  const { query, fleet } = useCNPGFleet([namespace])
  const runtime = useCNPGRuntime(namespace, name)
  const ha = useCNPGClusterHA(namespace, name)
  const baseRow = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const row = baseRow ? withLiveReplication(baseRow, runtime.data) : undefined
  const live = cnpgInstanceLive(runtime.data)
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
      lead={
        <>
          <CNPGRefreshFailedNotice queries={[runtime, ha]} />
          {context === 'drawer' && <CNPGOperatorBanner namespaces={[namespace]} />}
          <CNPGMaintenanceBanner namespace={namespace} name={name} maintenance={ha.data?.maintenance} />
          {row.cluster?.spec?.bootstrap?.recovery && <CNPGRestoreProgress namespace={namespace} name={name} />}
        </>
      }
      dimensions={cnpgDimensions({ row, ha: ha.data, replication: cnpgReplicationLive(runtime.data) })}
      stateFacts={<BaseBackupFact runtime={runtime.data} />}
      haSection={
        <CNPGClusterHASection
          ha={ha.data}
          live={live}
          loading={ha.isLoading}
          error={ha.error instanceof Error ? ha.error.message : undefined}
          onNavigate={go}
          primaryConflict={row.primaryConflict}
        />
      }
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

function ScheduledBackupSummaryHost(props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) {
  const ns = props.resource?.metadata?.namespace ?? ''
  const name = props.resource?.metadata?.name ?? ''
  const caps = useCNPGScheduleCapabilities(ns, name)
  return <CNPGScheduledBackupSummary {...props} schedulePreview={caps.data?.facts.preview} />
}

const OBJECT_SUMMARIES: Record<string, ObjectSummary> = {
  Backup: CNPGBackupSummary,
  ScheduledBackup: ScheduledBackupSummaryHost,
  Pooler: CNPGPoolerSummary,
  Database: CNPGDatabaseSummary,
  Publication: CNPGPublicationSummary,
  Subscription: CNPGSubscriptionSummary,
  DatabaseRole: CNPGDatabaseRoleSummary,
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

function PoolerSummaryHost({ ctx }: { ctx: SummaryContext }) {
  const { query } = useCNPGFleet([ctx.namespace])
  const { live, queries } = useCNPGPoolerLive(ctx.namespace, ctx.name)
  if (query.isLoading) return <PaneLoader label="Loading summary…" className="h-40" />
  const workspace = query.data?.installed ? query.data : null
  const go = ctx.onNavigate ? (ref: CNPGRef) => ctx.onNavigate?.(refToSelectedResource(ref)) : undefined
  return <CNPGPoolerSummary resource={ctx.resource} workspace={workspace} onNavigate={go} live={live} lead={<CNPGRefreshFailedNotice queries={queries} />} />
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
  if (kind === 'Pooler') return <PoolerSummaryHost ctx={ctx} />
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
