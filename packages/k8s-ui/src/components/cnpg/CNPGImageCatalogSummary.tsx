import { CNPG_GROUP, getCNPGImageCatalogEntries } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { FactGrid, FactRow, RefLink, SummaryHeading, toneTextClass, type CNPGNavigate } from './primitives'
import { NotReported, Note, ObjectProblems, SummaryShell } from './CNPGSharedSummary'
import { clustersIn, clustersUsingCatalog, refOf, relationUnavailable } from './relations'

export function CNPGImageCatalogSummary({
  resource,
  workspace,
  onNavigate,
}: {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: CNPGNavigate
}) {
  const clusterScoped = resource?.kind === 'ClusterImageCatalog'
  const ns = resource?.metadata?.namespace ?? ''
  const entries = getCNPGImageCatalogEntries(resource)
  const majors = new Set(entries.map((e) => e.major))
  const unavailable = relationUnavailable(workspace, 'clusters', clusterScoped ? undefined : ns, 'Clusters')
  const users = unavailable ? [] : clustersUsingCatalog(resource, clustersIn(workspace))

  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, resource?.kind ?? 'ImageCatalog')} onNavigate={onNavigate} />

      <SummaryHeading>Images</SummaryHeading>
      {entries.length === 0 ? (
        <div className="text-sm">
          <NotReported text="No images listed" />
        </div>
      ) : (
        <FactGrid>
          {entries.map((e) => (
            <FactRow key={e.major} label={`PostgreSQL ${e.major}`}>
              <span className="font-mono">{e.image}</span>
            </FactRow>
          ))}
        </FactGrid>
      )}

      <SummaryHeading>Used by</SummaryHeading>
      {unavailable ? (
        <div className="text-sm">
          <NotReported text={unavailable} />
        </div>
      ) : users.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">No visible cluster uses this catalog</div>
      ) : (
        <FactGrid>
          {users.map((u) => (
            <FactRow
              key={`${u.cluster.metadata?.namespace}/${u.cluster.metadata?.name}`}
              label={
                <RefLink refTo={refOf(u.cluster, 'Cluster', CNPG_GROUP)} onNavigate={onNavigate} mono>
                  {clusterScoped ? `${u.cluster.metadata?.namespace}/${u.cluster.metadata?.name}` : u.cluster.metadata?.name}
                </RefLink>
              }
            >
              {u.major === null ? (
                <NotReported text="Major not set" />
              ) : majors.has(u.major) ? (
                `Requests PostgreSQL ${u.major}`
              ) : (
                <span className={toneTextClass('unhealthy')}>Requests PostgreSQL {u.major} · not in this catalog</span>
              )}
            </FactRow>
          ))}
        </FactGrid>
      )}
      {clusterScoped && !unavailable && (
        <Note>Among clusters you can see; clusters in namespaces you cannot read are not listed</Note>
      )}
    </SummaryShell>
  )
}
