import {
  CNPG_BARMAN_OBJECTSTORE_GROUP,
  CNPG_GROUP,
  getCNPGObjectStoreCredentialSecret,
  getCNPGObjectStoreDestination,
  getCNPGObjectStoreProvider,
  getCNPGObjectStoreRecoveryWindows,
  getCNPGObjectStoreRetention,
} from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { NotReported, Note, ObjectProblems, SummaryShell, TimeAgo } from './CNPGSharedSummary'
import { clustersIn, inferredObjectStoreHealth, refOf, relationUnavailable, usersOfObjectStore } from './relations'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { toneTextClass } from '../ui/status-tone'
import { FactGrid, FactRow, FactValue } from '../facts'
import { SectionHeading } from '../ui/FoldSection'

function utc(at: string | undefined): string {
  if (!at || !Number.isFinite(Date.parse(at))) return 'unknown'
  return new Date(at).toUTCString().replace(' GMT', ' UTC')
}

export function CNPGObjectStoreSummary({
  resource,
  workspace,
  onNavigate,
}: {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: NavigateToRef
}) {
  const ns = resource?.metadata?.namespace ?? ''
  const clustersUnavailable = relationUnavailable(workspace, 'clusters', ns, 'Clusters')
  const users = clustersUnavailable ? [] : usersOfObjectStore(resource, clustersIn(workspace))
  const health = inferredObjectStoreHealth(resource, users)
  const clusterForServer = new Map(users.map((u) => [u.serverName, u.cluster?.metadata?.name as string]))
  const windows = getCNPGObjectStoreRecoveryWindows(resource)
  const destination = getCNPGObjectStoreDestination(resource)
  const provider = getCNPGObjectStoreProvider(resource)
  const secret = getCNPGObjectStoreCredentialSecret(resource)
  const retention = getCNPGObjectStoreRetention(resource)
  const clusterWord = users.length === 1 ? "1 cluster's" : `${users.length} clusters'`

  return (
    <SummaryShell>
      <ObjectProblems
        issues={workspace?.issues}
        subject={refOf(resource, 'ObjectStore', CNPG_BARMAN_OBJECTSTORE_GROUP)}
        onNavigate={onNavigate}
      />

      <SectionHeading hint="inferred">Upload health</SectionHeading>
      {clustersUnavailable ? (
        <div className="text-sm">
          <NotReported text={`Unknown · ${clustersUnavailable}, which are the only evidence`} />
        </div>
      ) : (
        <>
          <div className="text-sm">
            <FactValue fact={health.summary} />
          </div>
          {users.length > 0 && (
            <Note>Inferred from {clusterWord} WAL archiving and backup results — ObjectStore has no health status</Note>
          )}
          {health.evidence.length > 0 && (
            <div className="mt-2">
              <FactGrid>
                {health.evidence.map((e) => (
                  <FactRow key={e.cluster.name} label={<RefLink refTo={e.cluster} onNavigate={onNavigate} mono />}>
                    <div>
                      <FactValue fact={e.archiving} />
                      <div className="text-xs text-theme-text-secondary">
                        {e.window ? (
                          <>
                            Last backup success <TimeAgo at={e.window.lastSuccessfulBackupTime} missing="not recorded" />
                            {e.window.lastFailedBackupTime && (
                              <span className={e.window.failingSinceLastSuccess ? toneTextClass('unhealthy') : undefined}>
                                {' · '}last failure <TimeAgo at={e.window.lastFailedBackupTime} />
                              </span>
                            )}
                          </>
                        ) : (
                          <NotReported text={`No recovery window for server ${e.serverName}`} />
                        )}
                      </div>
                    </div>
                  </FactRow>
                ))}
              </FactGrid>
            </div>
          )}
        </>
      )}

      <SectionHeading hint="ObjectStore status">Recovery window</SectionHeading>
      {windows.length === 0 ? (
        <div className="text-sm">
          <NotReported text="No server has reported a recovery window" />
        </div>
      ) : (
        <FactGrid>
          {windows.map((w) => {
            const cluster = clusterForServer.get(w.server)
            return (
              <FactRow
                key={w.server}
                label={
                  cluster ? (
                    <RefLink refTo={{ kind: 'Cluster', group: CNPG_GROUP, namespace: ns, name: cluster }} onNavigate={onNavigate} mono>
                      {w.server}
                    </RefLink>
                  ) : (
                    <span className="font-mono">{w.server}</span>
                  )
                }
              >
                <div className="space-y-0.5">
                  <div>
                    <span className="text-theme-text-secondary">First recoverability point </span>
                    {w.firstRecoverabilityPoint ? utc(w.firstRecoverabilityPoint) : <NotReported />}
                  </div>
                  <div>
                    <span className="text-theme-text-secondary">Last successful backup </span>
                    {w.lastSuccessfulBackupTime ? utc(w.lastSuccessfulBackupTime) : <NotReported text="None recorded" />}
                  </div>
                  {w.lastFailedBackupTime && (
                    <div>
                      <span className="text-theme-text-secondary">Last failed backup </span>
                      <span className={w.failingSinceLastSuccess ? toneTextClass('degraded') : undefined}>{utc(w.lastFailedBackupTime)}</span>
                    </div>
                  )}
                  {w.failingSinceLastSuccess && (
                    <Note>{w.lastSuccessfulBackupTime ? 'A backup failed after the last recorded success.' : 'No successful backup recorded.'}</Note>
                  )}
                </div>
              </FactRow>
            )
          })}
        </FactGrid>
      )}

      <SectionHeading>Destination</SectionHeading>
      <FactGrid>
        <FactRow label="Path">{destination !== '-' ? <span className="font-mono">{destination}</span> : <NotReported text="Not set" />}</FactRow>
        <FactRow label="Provider">{provider ?? <NotReported />}</FactRow>
        <FactRow label="Credentials">
          {secret ? (
            <span>
              Secret <RefLink refTo={{ kind: 'Secret', group: '', namespace: ns, name: secret }} onNavigate={onNavigate} mono />
            </span>
          ) : (
            <NotReported text="No Secret referenced" />
          )}
        </FactRow>
        <FactRow label="Retention">{retention ?? <NotReported text="Not set" />}</FactRow>
      </FactGrid>

      <SectionHeading>Used by</SectionHeading>
      {clustersUnavailable ? (
        <div className="text-sm">
          <NotReported text={clustersUnavailable} />
        </div>
      ) : users.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">
          No visible cluster uses this store
          {workspace?.coverage?.clusters?.state === 'partial' && (
            <Note>Clusters in namespaces you cannot read are not checked.</Note>
          )}
        </div>
      ) : (
        <div className="flex flex-wrap gap-x-3 text-sm">
          {users.map((u) => (
            <RefLink key={u.cluster.metadata?.name} refTo={refOf(u.cluster, 'Cluster')} onNavigate={onNavigate} mono />
          ))}
        </div>
      )}
    </SummaryShell>
  )
}
