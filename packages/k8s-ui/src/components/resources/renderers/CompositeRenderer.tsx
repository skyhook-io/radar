import { clsx } from 'clsx'
import { Layers, GitBranch, Box, ChevronRight, Pause } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { Tooltip } from '../../ui/Tooltip'
import {
  getCrossplaneStatus,
  getCrossplaneStatusReason,
  getCompositionRef,
  getCompositionRevisionRef,
  getCompositionUpdatePolicy,
  getCrossplaneResourceRefs,
  getBoundXRRef,
  isClaim,
  isCrossplanePaused,
  type CrossplaneResourceRef,
} from '../resource-utils-crossplane'
import { kindToPlural } from '../../../utils/navigation'
import type { StatusBadge } from '../resource-utils'

/**
 * Status of a composed resource, fetched by the host wrapper. The package
 * renderer is pure — the host fans out per-ref queries (React Query) and
 * passes the result down here.
 *
 * `missing` and `error` are split because they read differently to operators:
 * `missing` (404) means the composed resource genuinely doesn't exist yet
 * (reconciliation hasn't created it), which is a normal state for a
 * freshly-applied Composite. Any other failure (401/403/500/network) means
 * Radar can't tell the operator what they want to know, and "missing" would
 * mislead them into thinking the resource is absent.
 */
export interface ComposedRefStatus {
  ref: CrossplaneResourceRef
  /** undefined means we haven't fetched yet (or the host doesn't support it) */
  status?: StatusBadge
  /** true while the fetch is in flight */
  loading?: boolean
  /** 404 — composed resource not created yet (normal during reconciliation) */
  missing?: boolean
  /** any non-404 fetch failure (auth, network, server error); message in errorMessage */
  error?: boolean
  errorMessage?: string
}

interface CompositeRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
  /** Per-composed-ref status, indexed by `${apiVersion}/${kind}/${namespace}/${name}`.
   * apiVersion is part of the key because two providers can ship CRDs with
   * identical kind plurals across different groups — without it, the
   * status of a Bucket from s3.aws.upbound.io would overwrite the status
   * of an unrelated Bucket from another provider. Populated by the host
   * wrapper. When absent, the renderer shows refs without status badges. */
  composedRefStatuses?: Map<string, ComposedRefStatus>
}

function makeRefKey(ref: CrossplaneResourceRef): string {
  return `${ref.apiVersion}/${ref.kind}/${ref.namespace ?? ''}/${ref.name}`
}

// Crossplane refs carry full apiVersion ("s3.aws.upbound.io/v1beta1"); the
// renderer needs the group ("s3.aws.upbound.io") to disambiguate plurals
// across providers.
function groupFromApiVersion(apiVersion: string | undefined): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}

export function CompositeRenderer({ data, onNavigate, composedRefStatuses }: CompositeRendererProps) {
  const status = getCrossplaneStatus(data)
  const reason = getCrossplaneStatusReason(data)
  const compositionRef = getCompositionRef(data)
  const compositionRevisionRef = getCompositionRevisionRef(data)
  const compositionUpdatePolicy = getCompositionUpdatePolicy(data)
  const resourceRefs = getCrossplaneResourceRefs(data)
  const claim = isClaim(data)
  const boundXR = claim ? getBoundXRRef(data) : null
  const paused = isCrossplanePaused(data)
  const alertVariant = status.level === 'unhealthy' || status.level === 'alert' ? status.level : null

  return (
    <>
      {paused && (
        <AlertBanner
          variant="warning"
          icon={Pause}
          title="Reconciliation paused"
          message="The crossplane.io/paused annotation is set. Composed resources will not be reconciled until the annotation is removed."
        />
      )}

      {alertVariant && reason && (
        <AlertBanner
          variant={alertVariant === 'unhealthy' ? 'error' : 'warning'}
          title={status.text}
          message={reason}
        />
      )}

      <Section title={claim ? 'Claim' : 'Composite Resource'} icon={Layers} defaultExpanded>
        <PropertyList>
          {compositionRef && (
            <Property
              label="Composition"
              value={
                <ResourceLink
                  name={compositionRef.name}
                  kind="compositions"
                  namespace=""
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          {compositionRevisionRef && (
            <Property
              label="Composition Revision"
              value={
                <ResourceLink
                  name={compositionRevisionRef.name}
                  kind="compositionrevisions"
                  namespace=""
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          {compositionUpdatePolicy && (
            <Property label="Update Policy" value={compositionUpdatePolicy} />
          )}
          {boundXR && (
            <Property
              label="Bound Composite"
              value={
                <ResourceLink
                  name={boundXR.name}
                  kind={kindToPlural(boundXR.kind)}
                  namespace={boundXR.namespace || ''}
                  group={groupFromApiVersion(boundXR.apiVersion) || undefined}
                  onNavigate={onNavigate}
                />
              }
            />
          )}
        </PropertyList>
      </Section>

      <Section
        title={`Composed Resources${resourceRefs.length > 0 ? ` (${resourceRefs.length})` : ''}`}
        icon={GitBranch}
        defaultExpanded
      >
        {resourceRefs.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary py-2">
            No composed resources yet — the composition has not produced any managed resources.
          </div>
        ) : (
          <div className="space-y-1">
            {resourceRefs.map((ref) => (
              <ComposedRefRow
                key={makeRefKey(ref)}
                ref_={ref}
                statusEntry={composedRefStatuses?.get(makeRefKey(ref))}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        )}
      </Section>

      <ConditionsSection conditions={data?.status?.conditions} />
    </>
  )
}

function ComposedRefRow({
  ref_,
  statusEntry,
  onNavigate,
}: {
  ref_: CrossplaneResourceRef
  statusEntry?: ComposedRefStatus
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const kindPlural = kindToPlural(ref_.kind)
  const group = groupFromApiVersion(ref_.apiVersion)
  return (
    <div className="flex items-center gap-2 py-1 px-2 rounded hover:bg-theme-hover text-sm">
      <Box className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
      <span className="text-theme-text-tertiary font-mono shrink-0 w-32 truncate">{ref_.kind}</span>
      <span className="flex-1 min-w-0 truncate">
        <ResourceLink
          name={ref_.name}
          kind={kindPlural}
          namespace={ref_.namespace || ''}
          group={group || undefined}
          onNavigate={onNavigate}
        />
      </span>
      {ref_.namespace && (
        <span className="text-xs text-theme-text-tertiary truncate w-32 text-right">{ref_.namespace}</span>
      )}
      <StatusCellInline statusEntry={statusEntry} />
      <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
    </div>
  )
}

function StatusCellInline({ statusEntry }: { statusEntry?: ComposedRefStatus }) {
  if (!statusEntry || statusEntry.loading) {
    return <span className="badge-sm bg-theme-elevated text-theme-text-tertiary w-20 text-center">…</span>
  }
  if (statusEntry.missing) {
    return <span className="badge-sm bg-theme-elevated text-theme-text-tertiary w-20 text-center">missing</span>
  }
  if (statusEntry.error) {
    const tip = statusEntry.errorMessage || 'Could not fetch composed resource status'
    return (
      <Tooltip content={tip}>
        <span className="badge-sm status-unhealthy w-20 text-center">error</span>
      </Tooltip>
    )
  }
  if (!statusEntry.status) {
    return <span className="badge-sm bg-theme-elevated text-theme-text-tertiary w-20 text-center">-</span>
  }
  return (
    <span className={clsx('badge-sm w-24 text-center truncate', statusEntry.status.color)}>
      {statusEntry.status.text}
    </span>
  )
}
