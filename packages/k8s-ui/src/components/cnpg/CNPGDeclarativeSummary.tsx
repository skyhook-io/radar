import type { ReactNode } from 'react'
import { getCNPGDeclarativeMessage, getCNPGReclaimPolicy } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { FactGrid, FactRow, FactValue, RefLink, SummaryHeading, toneTextClass, type CNPGNavigate } from './primitives'
import { ClusterLink, NotReported, ObjectProblems, SummaryShell } from './CNPGSharedSummary'
import {
  appliedFact,
  clustersIn,
  databaseForDeclaration,
  gitopsSourceOf,
  missingManagedRole,
  observedGenerationFact,
  refOf,
  relationUnavailable,
  replicationForDatabase,
  targetCluster,
  workspaceList,
} from './relations'

interface SummaryProps {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: CNPGNavigate
}

function ReclaimRow({ resource }: { resource: any }) {
  const reclaim = getCNPGReclaimPolicy(resource)
  return (
    <FactRow label="On delete">
      {reclaim.destructive ? (
        <span className={toneTextClass('degraded')}>{reclaim.value} · removing this resource drops it from PostgreSQL</span>
      ) : (
        <span>{reclaim.value} · removing this resource leaves PostgreSQL untouched</span>
      )}
    </FactRow>
  )
}

function Reconciled({ resource, extra }: { resource: any; extra?: ReactNode }) {
  const message = getCNPGDeclarativeMessage(resource)
  const applied = appliedFact(resource)
  return (
    <>
      <SummaryHeading>Reconciled</SummaryHeading>
      <FactGrid>
        <FactRow label="Applied">
          <FactValue fact={applied} />
        </FactRow>
        <FactRow label="Observed spec">
          <FactValue fact={observedGenerationFact(resource)} />
        </FactRow>
        <FactRow label="Operator message">
          {message ? (
            <span className={applied.tone === 'unhealthy' ? toneTextClass('unhealthy') : undefined}>{message}</span>
          ) : (
            <NotReported text="None" />
          )}
        </FactRow>
        {extra}
      </FactGrid>
    </>
  )
}

function DeclaredIn({ resource }: { resource: any }) {
  const src = gitopsSourceOf(resource)
  if (!src) return <span className="text-theme-text-secondary">Applied directly (no GitOps owner label)</span>
  return (
    <span>
      {src.tool === 'argocd' ? 'Argo CD application' : 'Flux'} <span className="font-mono">{src.namespace ? `${src.namespace}/${src.name}` : src.name}</span>
    </span>
  )
}

function DatabaseRef({ resource, workspace, onNavigate }: SummaryProps) {
  const dbname = resource?.spec?.dbname
  if (!dbname) return <NotReported text="Not set" />
  const ns = resource?.metadata?.namespace ?? ''
  const db = relationUnavailable(workspace, 'databases', ns, 'Databases')
    ? null
    : databaseForDeclaration(resource, workspaceList(workspace, 'databases'))
  return (
    <span>
      <span className="font-mono">{dbname}</span>
      {db && (
        <span className="text-theme-text-secondary">
          {' · declared by Database '}
          <RefLink refTo={refOf(db, 'Database')} onNavigate={onNavigate} mono />
        </span>
      )}
    </span>
  )
}

function LinkList({ items, kind, onNavigate }: { items: any[]; kind: string; onNavigate?: CNPGNavigate }) {
  return (
    <span className="flex flex-wrap gap-x-3">
      {items.map((o) => (
        <RefLink key={o.metadata?.name} refTo={refOf(o, kind)} onNavigate={onNavigate} mono />
      ))}
    </span>
  )
}

export function CNPGDatabaseSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const ns = resource?.metadata?.namespace ?? ''
  const cluster = targetCluster(resource, clustersIn(workspace))
  const missingRole = missingManagedRole(resource, cluster)
  const pubsUnavailable = relationUnavailable(workspace, 'publications', ns, 'Publications')
  const subsUnavailable = relationUnavailable(workspace, 'subscriptions', ns, 'Subscriptions')
  const related = replicationForDatabase(resource, workspaceList(workspace, 'publications'), workspaceList(workspace, 'subscriptions'))

  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Database')} onNavigate={onNavigate} />

      <SummaryHeading>Declared</SummaryHeading>
      <FactGrid>
        <FactRow label="PostgreSQL database">
          {resource?.spec?.name ? <span className="font-mono">{resource.spec.name}</span> : <NotReported text="Not set" />}
        </FactRow>
        <FactRow label="Owner role">
          {resource?.spec?.owner ? <span className="font-mono">{resource.spec.owner}</span> : <NotReported text="Not set" />}
        </FactRow>
        <FactRow label="Ensure">{resource?.spec?.ensure ?? 'present'}</FactRow>
        <ReclaimRow resource={resource} />
      </FactGrid>

      <Reconciled
        resource={resource}
        extra={
          missingRole && (
            <FactRow label="Managed roles">
              “{missingRole}” is not among {cluster?.metadata?.name}'s managed roles
            </FactRow>
          )
        }
      />

      <SummaryHeading>Source and target</SummaryHeading>
      <FactGrid>
        <FactRow label="Declared in">
          <DeclaredIn resource={resource} />
        </FactRow>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Publications">
          {pubsUnavailable ? (
            <NotReported text={pubsUnavailable} />
          ) : related.publications.length === 0 ? (
            <span className="text-theme-text-secondary">None on this database</span>
          ) : (
            <LinkList items={related.publications} kind="Publication" onNavigate={onNavigate} />
          )}
        </FactRow>
        <FactRow label="Subscriptions">
          {subsUnavailable ? (
            <NotReported text={subsUnavailable} />
          ) : related.subscriptions.length === 0 ? (
            <span className="text-theme-text-secondary">None on this database</span>
          ) : (
            <LinkList items={related.subscriptions} kind="Subscription" onNavigate={onNavigate} />
          )}
        </FactRow>
      </FactGrid>
    </SummaryShell>
  )
}

function publicationTargets(resource: any): ReactNode {
  const target = resource?.spec?.target
  if (target?.allTables === true) return 'All tables'
  const objects = Array.isArray(target?.objects) ? target.objects : []
  if (objects.length === 0) return <NotReported text="Not set" />
  const labels = objects.map((o: any) => {
    if (o?.tablesInSchema) return `All tables in schema ${o.tablesInSchema}`
    const t = o?.table
    if (t?.name) {
      const name = t.schema ? `${t.schema}.${t.name}` : t.name
      return Array.isArray(t.columns) && t.columns.length > 0 ? `${name} (${t.columns.join(', ')})` : name
    }
    return 'Unrecognized entry'
  })
  return (
    <ul className="space-y-0.5 font-mono">
      {labels.map((l: string, i: number) => (
        <li key={i}>{l}</li>
      ))}
    </ul>
  )
}

export function CNPGPublicationSummary({ resource, workspace, onNavigate }: SummaryProps) {
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Publication')} onNavigate={onNavigate} />

      <SummaryHeading>Declared</SummaryHeading>
      <FactGrid>
        <FactRow label="Publication">
          {resource?.spec?.name ? <span className="font-mono">{resource.spec.name}</span> : <NotReported text="Not set" />}
        </FactRow>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Database">
          <DatabaseRef resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Publishes">{publicationTargets(resource)}</FactRow>
        <ReclaimRow resource={resource} />
        <FactRow label="Declared in">
          <DeclaredIn resource={resource} />
        </FactRow>
      </FactGrid>

      <Reconciled resource={resource} />
    </SummaryShell>
  )
}

export function CNPGSubscriptionSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const pub = resource?.spec?.publicationName
  const ext = resource?.spec?.externalClusterName
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Subscription')} onNavigate={onNavigate} />

      <SummaryHeading>Declared</SummaryHeading>
      <FactGrid>
        <FactRow label="Subscription">
          {resource?.spec?.name ? <span className="font-mono">{resource.spec.name}</span> : <NotReported text="Not set" />}
        </FactRow>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Database">
          <DatabaseRef resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Subscribes to">
          {pub ? (
            <span>
              Publication <span className="font-mono">{pub}</span>
              {ext ? (
                <span className="text-theme-text-secondary">
                  {' on external cluster '}
                  <span className="font-mono">{ext}</span>
                </span>
              ) : null}
            </span>
          ) : (
            <NotReported text="Not set" />
          )}
        </FactRow>
        <ReclaimRow resource={resource} />
        <FactRow label="Declared in">
          <DeclaredIn resource={resource} />
        </FactRow>
      </FactGrid>

      <Reconciled resource={resource} />
    </SummaryShell>
  )
}
