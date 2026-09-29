import type { ReactNode } from 'react'
import { getCNPGDeclarativeMessage, getCNPGReclaimPolicy } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { cnpgDatabaseRoleFacts } from './databaseRole'
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
  if (!src) return <NotReported text="GitOps source not recorded" />
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

export function CNPGDatabaseRoleSummary({ resource, workspace, onNavigate }: SummaryProps) {
  const ns = resource?.metadata?.namespace ?? ''
  const clusterUnavailable = relationUnavailable(workspace, 'clusters', ns, 'Clusters')
  const cluster = clusterUnavailable ? null : targetCluster(resource, clustersIn(workspace))
  const f = cnpgDatabaseRoleFacts(resource, cluster)
  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'DatabaseRole')} onNavigate={onNavigate} />

      <SummaryHeading>Declared</SummaryHeading>
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
          <DeclaredIn resource={resource} />
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
