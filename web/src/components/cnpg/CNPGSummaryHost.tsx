import { useRef, type ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_CONNECT_SELECTOR,
  CNPGOpenIssueContext,
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
  relationUnavailable,
  cnpgLogicalPaths,
  cnpgLogicalSlotFact,
  cnpgSubscriptionHostNamespace,
  FactRow,
  FactSource,
  FactValue,
  PaneLoader,
  isApiGroup,
  refToSelectedResource,
  type CNPGNavigate,
  type CNPGRef,
  type CNPGLogicalPath,
  type CNPGWorkspaceResponse,
  type NavigateToResource,
} from '@skyhook-io/k8s-ui'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { useCNPGRuntime, useCNPGScheduleCapabilities, useCNPGWorkspace, type CNPGRuntimeResponse } from '../../api/cnpg'
import { cnpgPublisherSlotsFrom, useCNPGPublisherSlots } from './logicalSlots'
import { cnpgBaseBackupFacts, describeCNPGBaseBackup } from './baseBackup'
import { useCNPGPoolerLive } from './useCNPGPoolerLive'
import { cnpgInstanceLive, cnpgInstanceLiveUnavailable, cnpgReplicationGap, cnpgReplicationLive, useCNPGClusterHA, withLiveReplication } from '../../api/cnpg-ha'
import { CNPGMaintenanceBanner } from './actions/CNPGMaintenanceBanner'
import { CNPGOperatorBanner } from './CNPGOperatorBanner'
import { CNPGRefreshFailedNotice } from './shared'

import { cnpgClusterFullPath, cnpgDimensionPath, cnpgIssuesPath, cnpgWithinDetail, currentPageLabel } from './paths'
import { CNPGRestoreProgress } from './recovery/CNPGRestoreProgress'
import { restoreBackupDeclared } from './recovery/restoreModel'
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
  const location = useLocation()
  const summaryRef = useRef<HTMLDivElement>(null)
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
  const goRef = onNavigate ? (ref: CNPGRef) => onNavigate(refToSelectedResource(ref)) : undefined
  // On the Cluster's own page this is a tab change, applied like a tab click;
  // anywhere else (the drawer, another page) it is a push to the full page
  // with a return label.
  const go = (path: string) => {
    const inPlace = context === 'expanded' ? cnpgWithinDetail(location.pathname, location.search, path) : null
    if (inPlace) navigate(inPlace, { replace: true, state: location.state })
    else navigate(path, { state: { returnLabel: currentPageLabel(), returnCtx: connection.context } })
  }
  return (
    <div ref={summaryRef}>
      <CNPGClusterSummary
        row={row}
        onNavigate={goRef}
        lead={
          <>
            <CNPGRefreshFailedNotice queries={[runtime, ha]} />
            {context === 'drawer' && <CNPGOperatorBanner namespaces={[namespace]} />}
            <CNPGMaintenanceBanner namespace={namespace} name={name} maintenance={ha.data?.maintenance} />
            {row.cluster?.spec?.bootstrap?.recovery && (
              <CNPGRestoreProgress
                namespace={namespace}
                name={name}
                nextSteps={{
                  backup: row.cluster ? restoreBackupDeclared(row.cluster) : undefined,
                  onOpen: (step) => {
                    if (step === 'connect') {
                      summaryRef.current?.querySelector(CNPG_CONNECT_SELECTOR)?.scrollIntoView({ block: 'start' })
                      return
                    }
                    const protection = cnpgClusterFullPath(namespace, name, connection.context || undefined, 'protection')
                    go(step === 'validate' ? `${protection}&validate=1` : protection)
                  },
                }}
              />
            )}
          </>
        }
        onSelectDimension={(id) => go(cnpgDimensionPath(namespace, name, connection.context || undefined, id))}
        initialProblemsExpanded={context === 'expanded' && new URLSearchParams(location.search).get('problems') === 'all'}
        dimensions={cnpgDimensions({ row, ha: ha.data, replication: cnpgReplicationLive(runtime.data), replicationGap: cnpgReplicationGap(runtime.data, runtime.error) })}
        stateFacts={<BaseBackupFact runtime={runtime.data} />}
        haSection={
          <CNPGClusterHASection
            ha={ha.data}
            live={live}
            liveUnavailable={cnpgInstanceLiveUnavailable(runtime.data, runtime.error)}
            loading={ha.isLoading}
            error={ha.error instanceof Error ? ha.error.message : undefined}
            onNavigate={goRef}
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
    </div>
  )
}

type ObjectSummary = (props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) => ReactNode

function ScheduledBackupSummaryHost(props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) {
  const ns = props.resource?.metadata?.namespace ?? ''
  const name = props.resource?.metadata?.name ?? ''
  const caps = useCNPGScheduleCapabilities(ns, name)
  return <CNPGScheduledBackupSummary {...props} schedulePreview={caps.data?.facts.preview} />
}

// The publisher may live in another namespace than the subscriber: that
// namespace's Clusters, Publications and Poolers are read too.
function useLogicalWorkspace(ws: CNPGWorkspaceResponse | null, subscriptions: any[]) {
  const hostNs = [...new Set(subscriptions.map((s) => cnpgSubscriptionHostNamespace(s, ws?.objects.clusters ?? [])).filter((n): n is string => !!n))]
  const extra = ws?.namespaces === null ? [] : hostNs.filter((n) => !(ws?.namespaces ?? []).includes(n))
  const other = useCNPGWorkspace(extra, { enabled: !!ws && extra.length > 0 })
  const merged = (key: 'clusters' | 'publications' | 'poolers') => [...(ws?.objects[key] ?? []), ...(other.data?.installed ? other.data.objects[key] ?? [] : [])]
  const publicationsUnavailable = (ns: string) =>
    ws?.namespaces === null || ws?.namespaces?.includes(ns)
      ? relationUnavailable(ws, 'publications', ns, 'Publications')
      : relationUnavailable(other.data?.installed ? other.data : null, 'publications', ns, 'Publications')
  return { clusters: merged('clusters'), publications: merged('publications'), poolers: merged('poolers'), publicationsUnavailable }
}

function LogicalPathSlot({ path, children }: { path: CNPGLogicalPath; children: (slot: ReturnType<typeof cnpgLogicalSlotFact>, notice: ReactNode) => ReactNode }) {
  const { observed, query } = useCNPGPublisherSlots(path.publisher)
  return <>{children(cnpgLogicalSlotFact(path, observed), <CNPGRefreshFailedNotice queries={[query]} />)}</>
}

function SubscriptionSummaryHost(props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) {
  const lw = useLogicalWorkspace(props.workspace, [props.resource])
  const path = props.workspace ? cnpgLogicalPaths([props.resource], lw.clusters, lw.publications, lw.poolers, lw.publicationsUnavailable)[0] : undefined
  if (!path) return <CNPGSubscriptionSummary {...props} />
  return <LogicalPathSlot path={path}>{(slot, notice) => <CNPGSubscriptionSummary {...props} logicalPath={{ path, slot, notice }} />}</LogicalPathSlot>
}

function PublicationSummaryHost(props: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: CNPGNavigate }) {
  // Subscribers are the Subscriptions in view; the publisher's own runtime
  // answers for every slot.
  const pubCluster = props.resource?.spec?.cluster?.name
  const ns = props.resource?.metadata?.namespace ?? ''
  const runtime = useCNPGRuntime(ns, pubCluster ?? '', !!pubCluster)
  const all = useCNPGWorkspace([], { enabled: !!props.workspace })
  const ws = all.data?.installed ? all.data : props.workspace
  const paths = ws
    ? cnpgLogicalPaths(ws.objects.subscriptions ?? [], ws.objects.clusters ?? [], ws.objects.publications ?? [], ws.objects.poolers ?? []).filter(
        (p) => p.publication.object?.namespace === ns && p.publication.object?.name === props.resource?.metadata?.name,
      )
    : []
  const observed = cnpgPublisherSlotsFrom(runtime.data, runtime.error, runtime.isRefetchError)
  const notice = <CNPGRefreshFailedNotice queries={[runtime]} />
  return <CNPGPublicationSummary {...props} subscribers={paths.map((path) => ({ path, slot: cnpgLogicalSlotFact(path, observed), notice }))} />
}

const OBJECT_SUMMARIES: Record<string, ObjectSummary> = {
  Backup: CNPGBackupSummary,
  ScheduledBackup: ScheduledBackupSummaryHost,
  Pooler: CNPGPoolerSummary,
  Database: CNPGDatabaseSummary,
  Publication: PublicationSummaryHost,
  Subscription: SubscriptionSummaryHost,
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
  return <IssueLinks>{renderSummaryFor(ctx)}</IssueLinks>
}

// "See in Issues →" on every CNPG problem: the Issues page narrowed to the
// problem's subject (it has no link to a single issue).
function IssueLinks({ children }: { children: ReactNode }) {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  return (
    <CNPGOpenIssueContext.Provider
      value={(p) =>
        navigate(cnpgIssuesPath(p.subject, searchParams.get('namespaces')))
      }
    >
      {children}
    </CNPGOpenIssueContext.Provider>
  )
}
