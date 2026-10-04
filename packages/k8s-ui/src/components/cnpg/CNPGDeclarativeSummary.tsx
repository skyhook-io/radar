import type { ReactNode } from 'react'
import { getCNPGDeclarativeMessage, getCNPGReclaimPolicy } from '../resources/resource-utils-cnpg'
import { cnpgManagedBy, type CNPGWorkspaceResponse } from './workspace'
import { type Fact } from '../facts'
import { cnpgLogicalPaths, type CNPGLogicalPath } from './logicalReplication'
import { CNPGLogicalPathView } from './CNPGLogicalPath'
import { cnpgDatabaseRoleFacts } from './databaseRole'
import { ClusterLink, NotReported, ObjectProblems, SummaryShell } from './CNPGSharedSummary'
import {
  appliedFact,
  clustersIn,
  databaseForDeclaration,
  missingManagedRole,
  observedGenerationFact,
  refOf,
  relationUnavailable,
  replicationForDatabase,
  targetCluster,
  workspaceList,
} from './relations'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { toneTextClass } from '../ui/status-tone'
import { FactGrid, FactRow, FactValue, ManagedByText, managedByLabel } from '../facts'
import { SectionHeading } from '../ui/FoldSection'

interface SummaryProps {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: NavigateToRef
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
      <SectionHeading>Reconciled</SectionHeading>
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

function DeclaredIn({ resource, workspace, onNavigate }: { resource: any; workspace: CNPGWorkspaceResponse | null; onNavigate?: NavigateToRef }) {
  const manager = cnpgManagedBy(workspace, resource)
  if (!manager || !managedByLabel(manager)) return <NotReported text="GitOps source not recorded" />
  return <ManagedByText refTo={manager} onNavigate={onNavigate} />
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

function LinkList({ items, kind, onNavigate }: { items: any[]; kind: string; onNavigate?: NavigateToRef }) {
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

      <SectionHeading>Declared</SectionHeading>
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

      <SectionHeading>Source and target</SectionHeading>
      <FactGrid>
        <FactRow label="Declared in">
          <DeclaredIn resource={resource} workspace={workspace} onNavigate={onNavigate} />
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

function workspacePaths(workspace: CNPGWorkspaceResponse | null | undefined, subscriptions: any[]): CNPGLogicalPath[] {
  return cnpgLogicalPaths(subscriptions, clustersIn(workspace), workspaceList(workspace, 'publications'), workspaceList(workspace, 'poolers'), (ns) =>
    relationUnavailable(workspace, 'publications', ns, 'Publications'),
  )
}

export interface CNPGLogicalPathReading {
  path: CNPGLogicalPath
  /** The publisher primary's report of the slot; absent when not read. */
  slot?: Fact
  notice?: ReactNode
}

export function CNPGPublicationSummary({
  resource,
  workspace,
  onNavigate,
  subscribers,
}: SummaryProps & {
  /** Subscriptions reading this publication, with their slots; derived from the workspace when omitted. */
  subscribers?: CNPGLogicalPathReading[]
}) {
  const readings: CNPGLogicalPathReading[] =
    subscribers ??
    workspacePaths(workspace, workspaceList(workspace, 'subscriptions'))
      .filter((p) => p.publication.object?.namespace === resource?.metadata?.namespace && p.publication.object?.name === resource?.metadata?.name)
      .map((path) => ({ path }))
  const subsUnavailable = relationUnavailable(workspace, 'subscriptions', resource?.metadata?.namespace ?? '', 'Subscriptions')
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Publication')} onNavigate={onNavigate} />

      <SectionHeading>Declared</SectionHeading>
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
          <DeclaredIn resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
      </FactGrid>

      <Reconciled resource={resource} />

      <SectionHeading hint="Subscription objects Radar can see">Subscribers</SectionHeading>
      {readings.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">
          {subsUnavailable ?? 'No visible Subscription object reads this publication. Subscribers outside Radar\'s view, or created in SQL, are not listed.'}
        </div>
      ) : (
        <div className="space-y-3">
          {readings.map((r) => (
            <CNPGLogicalPathView key={`${r.path.subscription.namespace}/${r.path.subscription.name}`} path={r.path} slot={r.slot} notice={r.notice} onNavigate={onNavigate} compact />
          ))}
        </div>
      )}
    </SummaryShell>
  )
}

export function CNPGDatabaseRoleSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const ns = resource?.metadata?.namespace ?? ''
  const clusterUnavailable = relationUnavailable(workspace, 'clusters', ns, 'Clusters')
  const cluster = clusterUnavailable ? null : targetCluster(resource, clustersIn(workspace))
  const f = cnpgDatabaseRoleFacts(resource, cluster)
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'DatabaseRole')} onNavigate={onNavigate} />

      <SectionHeading>Declared</SectionHeading>
      <FactGrid>
        <FactRow label="PostgreSQL role">{f.pgName ? <span className="font-mono">{f.pgName}</span> : <NotReported text="Not set" />}</FactRow>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Login">{f.login ? 'Allowed' : 'Not allowed'}{f.superuser ? ' · superuser' : ''}</FactRow>
        <FactRow label="Password">
          {f.passwordDisabled ? (
            'Disabled'
          ) : f.passwordSecret ? (
            <span>
              From Secret <span className="font-mono">{f.passwordSecret}</span>
            </span>
          ) : (
            <span className="text-theme-text-secondary">No password Secret declared</span>
          )}
          {f.passwordValidUntil && (
            <div className="text-xs text-theme-text-secondary">
              Valid until {f.passwordValidUntil} (PostgreSQL VALID UNTIL; the operator does not rotate it)
            </div>
          )}
        </FactRow>
        <FactRow label="Client certificate">
          {f.clientCertificate ? (
            <span>
              Operator-issued in Secret <span className="font-mono">{f.clientCertificate.secret}</span>
              <span className="text-theme-text-secondary">
                {' · '}
                {f.clientCertificate.expiration ? `expires ${f.clientCertificate.expiration}` : 'expiry not reported yet'}
              </span>
              {f.clientCertificate.message && <div className="text-xs text-theme-text-secondary">{f.clientCertificate.message}</div>}
            </span>
          ) : (
            <span className="text-theme-text-secondary">Not requested</span>
          )}
        </FactRow>
        <ReclaimRow resource={resource} />
        <FactRow label="Declared in">
          <DeclaredIn resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
      </FactGrid>

      <Reconciled
        resource={resource}
        extra={
          <FactRow label="Cluster spec">
            {f.overriddenByCluster === null ? (
              <NotReported text={clusterUnavailable ?? 'Target Cluster not visible, so whether its spec.managed.roles overrides this role is unknown'} />
            ) : f.overriddenByCluster ? (
              <span className={toneTextClass('degraded')}>
                The Cluster declares “{f.pgName}” in spec.managed.roles, which takes precedence: this DatabaseRole is not reconciled while that entry exists
              </span>
            ) : (
              <span className="text-theme-text-secondary">No spec.managed.roles entry for this role</span>
            )}
          </FactRow>
        }
      />
    </SummaryShell>
  )
}

export function CNPGSubscriptionSummary({
  resource,
  workspace,
  onNavigate,
  logicalPath,
}: SummaryProps & {
  /** The path and slot reading; derived from the workspace (slot not read) when omitted. */
  logicalPath?: CNPGLogicalPathReading
}) {
  const reading: Partial<CNPGLogicalPathReading> = logicalPath ?? { path: workspacePaths(workspace, [resource])[0] }
  const pub = resource?.spec?.publicationName
  const ext = resource?.spec?.externalClusterName
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Subscription')} onNavigate={onNavigate} />

      <SectionHeading>Declared</SectionHeading>
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
          <DeclaredIn resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
      </FactGrid>

      <Reconciled resource={resource} />

      {reading.path && (
        <>
          <SectionHeading>Replication path</SectionHeading>
          <CNPGLogicalPathView path={reading.path} slot={reading.slot} notice={reading.notice} onNavigate={onNavigate} />
        </>
      )}
    </SummaryShell>
  )
}
