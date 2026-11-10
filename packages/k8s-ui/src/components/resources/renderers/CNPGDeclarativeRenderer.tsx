import type React from 'react'
import { Database, AlertTriangle, Table2 } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { HEALTH_BADGE_COLORS } from '../../../utils/badge-colors'
import {
  getCNPGDeclarativeStatus,
  getCNPGDeclarativeMessage,
  getCNPGDeclarativeCluster,
  getCNPGReclaimPolicy,
  getCNPGImageCatalogEntries,
} from '../resource-utils-cnpg'

type Nav = (ref: { kind: string; namespace: string; name: string; group?: string }) => void

/**
 * Database, Publication and Subscription — CNPG's declarative objects.
 *
 * One renderer for three kinds because they share the state that matters:
 * whether the operator has applied them to PostgreSQL, and what it said if it
 * could not. They differ only in which spec fields identify the thing being
 * declared, which the caller supplies.
 *
 * The status is deliberately three-valued. `applied: false` is a failure the
 * operator can explain; an absent `applied` means it has not reconciled yet,
 * and rendering that as a failure would condemn every object in its first
 * seconds.
 */
/** Shared shell for the three declarative kinds; not part of the package API. */
function CNPGDeclarativeRenderer({
  data,
  onNavigate,
  details,
  extra,
}: {
  data: any
  onNavigate?: Nav
  /** Kind-specific rows: what this object declares. */
  details: Array<{ label: string; value: React.ReactNode }>
  /** Sections the host adds below, e.g. reverse lookups needing a data fetch. */
  extra?: React.ReactNode
}) {
  const status = getCNPGDeclarativeStatus(data)
  const message = getCNPGDeclarativeMessage(data)
  const cluster = getCNPGDeclarativeCluster(data)
  const reclaim = getCNPGReclaimPolicy(data)
  const namespace = data?.metadata?.namespace ?? ''

  return (
    <>
      {/* The operator's own words, verbatim. A failure here means the object
          exists in Kubernetes and not in PostgreSQL — a split an operator will
          not spot from the CR alone. */}
      {status.level === 'unhealthy' && message && (
        <AlertBanner variant="error" title="Not applied to PostgreSQL" message={message} />
      )}

      <Section title="Status" icon={Database} defaultExpanded>
        <PropertyList>
          <Property
            label="Applied"
            value={
              <span className={clsx('badge', HEALTH_BADGE_COLORS[status.level as keyof typeof HEALTH_BADGE_COLORS])}>
                {status.text}
              </span>
            }
          />
          {cluster && (
            <Property
              label="Cluster"
              value={
                <ResourceLink
                  name={cluster}
                  kind="clusters"
                  namespace={namespace}
                  group="postgresql.cnpg.io"
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          {details.map((d) => (
            <Property key={d.label} label={d.label} value={d.value} />
          ))}
          <Property
            label="On delete"
            value={
              reclaim.destructive ? (
                <span className="text-warning-text">
                  {`${reclaim.value} — removing this resource drops it from PostgreSQL`}
                </span>
              ) : (
                <span>{`${reclaim.value} — removing this resource leaves PostgreSQL untouched`}</span>
              )
            }
          />
        </PropertyList>
        {/* Pending is not failure, and saying nothing here would leave the
            reader to guess which of the two they are looking at. */}
        {status.level === 'unknown' && (
          <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
            The operator has not reported on this yet. It has not failed — it has not been applied.
          </div>
        )}
        {status.level !== 'unhealthy' && message && (
          <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">{message}</div>
        )}
      </Section>
      {extra}
    </>
  )
}

export function CNPGDatabaseRenderer({
  data,
  onNavigate,
  usedBy,
}: {
  data: any
  onNavigate?: Nav
  /** Filled by the host with what replicates out of this database. */
  usedBy?: React.ReactNode
}) {
  const spec = data?.spec ?? {}
  const details: Array<{ label: string; value: React.ReactNode }> = [
    { label: 'Database', value: spec.name ?? '-' },
    { label: 'Owner', value: spec.owner ?? '-' },
  ]
  if (spec.encoding) details.push({ label: 'Encoding', value: spec.encoding })
  if (spec.locale) details.push({ label: 'Locale', value: spec.locale })
  if (spec.connectionLimit != null) {
    details.push({ label: 'Connection Limit', value: String(spec.connectionLimit) })
  }
  if (spec.allowConnections === false) {
    details.push({ label: 'Connections', value: 'Not allowed' })
  }
  if (spec.isTemplate === true) details.push({ label: 'Template', value: 'Yes' })
  return <CNPGDeclarativeRenderer data={data} onNavigate={onNavigate} details={details} extra={usedBy} />
}

export function CNPGPublicationRenderer({
  data,
  onNavigate,
  links,
}: {
  data: any
  onNavigate?: Nav
  /** Resolved by the host: the PostgreSQL-side names below correspond to real
   *  CRs, and naming them without a route is a dead end. */
  links?: { database?: React.ReactNode }
}) {
  const spec = data?.spec ?? {}
  const target = spec.target ?? {}
  const scope = target.allTables
    ? 'All tables'
    : Array.isArray(target.objects)
      ? `${target.objects.length} object${target.objects.length === 1 ? '' : 's'}`
      : '-'
  return (
    <CNPGDeclarativeRenderer
      data={data}
      onNavigate={onNavigate}
      details={[
        { label: 'Publication', value: spec.name ?? '-' },
        { label: 'In Database', value: links?.database ?? spec.dbname ?? '-' },
        { label: 'Publishes', value: scope },
      ]}
    />
  )
}

export function CNPGSubscriptionRenderer({
  data,
  onNavigate,
  links,
}: {
  data: any
  onNavigate?: Nav
  links?: { database?: React.ReactNode; publication?: React.ReactNode }
}) {
  const spec = data?.spec ?? {}
  return (
    <CNPGDeclarativeRenderer
      data={data}
      onNavigate={onNavigate}
      details={[
        { label: 'Subscription', value: spec.name ?? '-' },
        { label: 'In Database', value: links?.database ?? spec.dbname ?? '-' },
        { label: 'Subscribes To', value: links?.publication ?? spec.publicationName ?? '-' },
        // The upstream is declared on the destination Cluster, not here, which
        // is why a missing one fails at apply time rather than at admission.
        { label: 'Upstream Cluster', value: spec.externalClusterName ?? '-' },
      ]}
    />
  )
}

/**
 * DatabaseRole (CNPG 1.30+) — one PostgreSQL role as its own object. The
 * Cluster's spec.managed.roles wins for the same name; the operator then
 * reports this object not applied, and that message renders verbatim above.
 */
export function CNPGDatabaseRoleRenderer({ data, onNavigate }: { data: any; onNavigate?: Nav }) {
  const spec = data?.spec ?? {}
  const details: Array<{ label: string; value: React.ReactNode }> = [
    { label: 'Role', value: spec.name ?? '-' },
    { label: 'Login', value: spec.login === true ? 'Allowed' : 'Not allowed' },
  ]
  if (spec.superuser === true) details.push({ label: 'Superuser', value: 'Yes' })
  if (spec.disablePassword === true) details.push({ label: 'Password', value: 'Disabled' })
  else if (spec.passwordSecret?.name) details.push({ label: 'Password Secret', value: spec.passwordSecret.name })
  if (spec.validUntil) details.push({ label: 'Valid Until', value: spec.validUntil })
  if (Array.isArray(spec.inRoles) && spec.inRoles.length > 0) details.push({ label: 'Member Of', value: spec.inRoles.join(', ') })
  if (spec.connectionLimit != null && spec.connectionLimit !== -1) details.push({ label: 'Connection Limit', value: String(spec.connectionLimit) })
  const cc = spec.clientCertificate
  if (cc && cc.enabled !== false) {
    const exp = data?.status?.clientCertificate?.expiration
    details.push({ label: 'Client Certificate', value: `${data?.metadata?.name}-client-cert · ${exp ? `expires ${exp}` : 'expiry not reported'}` })
  }
  return <CNPGDeclarativeRenderer data={data} onNavigate={onNavigate} details={details} />
}

/**
 * ImageCatalog and ClusterImageCatalog — the PostgreSQL images a Cluster may
 * use, pinned per major version.
 *
 * Worth its own surface because a Cluster referencing a catalog that lacks its
 * major version reports "incomplete or invalid image catalog" and stops, and
 * the catalog is where that is diagnosed.
 */
export function CNPGImageCatalogRenderer({
  data,
  usedBy,
}: {
  data: any
  /** Filled by the host with the clusters pinned to this catalog — the question
   *  an operator has when a cluster reports an invalid catalog. */
  usedBy?: React.ReactNode
}) {
  const entries = getCNPGImageCatalogEntries(data)
  return (
    <>
    <Section title={`Images (${entries.length})`} icon={Table2} defaultExpanded>
      {entries.length === 0 ? (
        <div className="flex items-start gap-2 text-sm text-theme-text-secondary">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
          <span>
            No images are pinned. A cluster pointing at this catalog cannot resolve an image and will
            not start.
          </span>
        </div>
      ) : (
        <div className="space-y-1.5">
          {entries.map((e) => (
            <div key={e.major} className="flex items-baseline gap-3 min-w-0">
              <span className="badge shrink-0">{`PostgreSQL ${e.major}`}</span>
              <span className="text-sm text-theme-text-secondary font-mono truncate" title={e.image}>
                {e.image}
              </span>
            </div>
          ))}
        </div>
      )}
      </Section>
      {usedBy}
    </>
  )
}
