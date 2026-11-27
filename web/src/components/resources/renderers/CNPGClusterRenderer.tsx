import type { CertificateExpiryWindows } from '@skyhook-io/k8s-ui/types'
import { Database } from 'lucide-react'
import { CNPGClusterRenderer as BaseCNPGClusterRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/CNPGClusterRenderer'
import { Section, RelationshipGroup, getCNPGDeclarativeStatus } from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useResources } from '../../../api/client'

const CNPG_GROUP = 'postgresql.cnpg.io'

/**
 * Host wrapper adding the reverse lookup: which Databases, Publications and
 * Subscriptions are declared against this cluster.
 *
 * Each of those objects links forward to its cluster. Without this the
 * relationship is one-way, so an operator reading a cluster has no way to see
 * that three databases were declared against it and one of them never applied.
 */
export function CNPGClusterRenderer({
  data,
  certificateExpiryPolicy,
  onNavigate,
}: {
  data: any
  certificateExpiryPolicy?: CertificateExpiryWindows
  onNavigate?: (ref: ResourceRef) => void
}) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const enabled = !!namespace && !!name

  const databases = useResources<any>('databases', namespace, CNPG_GROUP, { enabled })
  const publications = useResources<any>('publications', namespace, CNPG_GROUP, { enabled })
  const subscriptions = useResources<any>('subscriptions', namespace, CNPG_GROUP, { enabled })

  const mine = (items: any[] | undefined) =>
    (items ?? []).filter((o) => o?.spec?.cluster?.name === name)

  const dbs = mine(databases.data)
  const pubs = mine(publications.data)
  const subs = mine(subscriptions.data)
  const total = dbs.length + pubs.length + subs.length

  const loading = databases.isLoading || publications.isLoading || subscriptions.isLoading
  // Three independent lookups, so "it failed" is not all-or-nothing: two can
  // return rows while the third does not, and every count below is then drawn
  // from a population smaller than the real one.
  const lookupErrors = [databases.error, publications.error, subscriptions.error]
  const failed = lookupErrors.some(Boolean)

  // Not applied is the state worth surfacing here: the manifest exists and
  // PostgreSQL does not have the object. Counting it on the cluster is how an
  // operator notices without opening each one.
  const notApplied = [...dbs, ...pubs, ...subs].filter(
    (o) => getCNPGDeclarativeStatus(o).level === 'unhealthy',
  ).length

  const refs = (items: any[], kind: string) =>
    items.map((o) => ({
      kind,
      namespace: o?.metadata?.namespace ?? '',
      name: o?.metadata?.name ?? '',
      group: CNPG_GROUP,
    }))

  return (
    <>
      <BaseCNPGClusterRenderer
        data={data}
        certificateExpiryPolicy={certificateExpiryPolicy}
        onNavigate={onNavigate}
        declared={
          <Section title="Declared Objects" icon={Database} defaultExpanded={notApplied > 0}>
            {loading ? (
              <div className="text-sm text-theme-text-tertiary">Looking for declared objects…</div>
            ) : total === 0 ? (
              // "None declared" and "could not check" are different answers, and
              // only one of them means the cluster has no declarative objects.
              failed ? (
                <LookupFailureNote
                  errors={lookupErrors}
                  what="which objects are declared against this cluster"
                />
              ) : (
                <div className="text-sm text-theme-text-tertiary">
                  No Database, Publication or Subscription is declared against this cluster.
                </div>
              )
            ) : (
              <div className="space-y-3">
                {notApplied > 0 && (
                  <div className="text-sm text-warning-text">
                    {`${notApplied} of ${total} could not be applied to PostgreSQL — the manifest exists, the object does not.`}
                  </div>
                )}
                {/* Rows arrived, so the failure is not "nothing found" — it is
                    that both counts above are drawn from less than the cluster
                    has, which is the reading they would otherwise invite. */}
                <LookupFailureNote
                  errors={lookupErrors}
                  what="which objects are declared against this cluster"
                  incomplete
                />
                {dbs.length > 0 && (
                  <RelationshipGroup label="Databases" refs={refs(dbs, 'Database')} onNavigate={onNavigate} />
                )}
                {pubs.length > 0 && (
                  <RelationshipGroup label="Publications" refs={refs(pubs, 'Publication')} onNavigate={onNavigate} />
                )}
                {subs.length > 0 && (
                  <RelationshipGroup label="Subscriptions" refs={refs(subs, 'Subscription')} onNavigate={onNavigate} />
                )}
              </div>
            )}
          </Section>
        }
      />
    </>
  )
}
