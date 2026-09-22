import { Info, SlidersHorizontal } from 'lucide-react'
import { Section, ResourceLink } from '../../ui/drawer-components'
import { Tooltip } from '../../ui/Tooltip'
import { LookupFailureNote } from './LookupFailureNote'
import { isForbiddenError } from '../../../types/fetch-error'

// A LimitRange declares the defaults and constraints the API server applies to
// pods, containers and PVCs admitted into a namespace. It has no status, so
// there is nothing to diagnose here — only the declared rules, shown as
// written.

interface LimitRangeFacet {
  key: 'min' | 'max' | 'defaultRequest' | 'default' | 'maxLimitRequestRatio'
  label: string
  hint: string
}

const FACETS: LimitRangeFacet[] = [
  { key: 'min', label: 'Min', hint: 'Lowest value accepted at admission.' },
  { key: 'max', label: 'Max', hint: 'Highest value accepted at admission.' },
  { key: 'defaultRequest', label: 'Default request', hint: 'Request applied when the resource omits one.' },
  { key: 'default', label: 'Default limit', hint: 'Limit applied when the resource omits one.' },
  {
    key: 'maxLimitRequestRatio',
    label: 'Max limit/request',
    hint: 'Largest allowed limit ÷ request. A ratio, not a quantity — it carries no unit.',
  },
]

const TYPE_SCOPE: Record<string, string> = {
  Container: 'each container, individually',
  Pod: 'the pod’s resource requests and limits',
  PersistentVolumeClaim: 'each PersistentVolumeClaim',
}

type QuantityMap = Record<string, unknown>

function facetMap(item: any, key: LimitRangeFacet['key']): QuantityMap {
  const value = item?.[key]
  return value && typeof value === 'object' ? (value as QuantityMap) : {}
}

/** Resource names this entry mentions anywhere, so a resource constrained by
 *  only one facet still gets a row. */
function entryResourceNames(item: any): string[] {
  const names = new Set<string>()
  for (const facet of FACETS) {
    for (const name of Object.keys(facetMap(item, facet.key))) names.add(name)
  }
  return [...names].sort()
}

/** Facets this entry actually declares. An absent facet is not a zero, so it is
 *  dropped rather than rendered as an empty column. */
function entryFacets(item: any): LimitRangeFacet[] {
  return FACETS.filter(facet => Object.keys(facetMap(item, facet.key)).length > 0)
}

function limitRangeItems(data: any): any[] {
  const limits = data?.spec?.limits
  return Array.isArray(limits) ? limits : []
}

export function LimitRangeRenderer({ data }: { data: any }) {
  const items = limitRangeItems(data)

  return (
    <Section title="Defaults & Constraints" icon={SlidersHorizontal}>
      {items.length === 0 ? (
        <div className="text-xs text-theme-text-secondary">
          This LimitRange declares no entries, so it constrains nothing.
        </div>
      ) : (
        <div className="space-y-3">
          {items.map((item: any, index: number) => (
            <LimitRangeEntry key={`${item?.type ?? 'entry'}-${index}`} item={item} />
          ))}
        </div>
      )}
    </Section>
  )
}

function LimitRangeEntry({ item }: { item: any }) {
  const type = typeof item?.type === 'string' && item.type ? item.type : 'Unknown'
  const scope = TYPE_SCOPE[type]
  const facets = entryFacets(item)
  const resources = entryResourceNames(item)

  return (
    <div className="card-inner">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 mb-2">
        <span className="text-xs font-medium text-theme-text-primary">{type}</span>
        {scope && <span className="text-xs text-theme-text-tertiary">applies to {scope}</span>}
      </div>
      {resources.length === 0 ? (
        <div className="text-xs text-theme-text-secondary">No resources constrained by this entry.</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary text-left">
                <th className="font-normal pr-3 pb-1">Resource</th>
                {facets.map(facet => (
                  <th key={facet.key} className="font-normal pr-3 pb-1 text-right whitespace-nowrap">
                    <Tooltip content={facet.hint} delay={150}>
                      <span className="border-b border-dotted border-theme-text-tertiary cursor-help">{facet.label}</span>
                    </Tooltip>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {resources.map(name => (
                <tr key={name} className="border-t border-theme-border">
                  <td className="pr-3 py-1 text-theme-text-secondary break-all">{name}</td>
                  {facets.map(facet => {
                    const raw = facetMap(item, facet.key)[name]
                    const set = raw !== undefined && raw !== null
                    return (
                      <td
                        key={facet.key}
                        className="pr-3 py-1 text-right tabular-nums whitespace-nowrap text-theme-text-primary"
                      >
                        {set ? String(raw) : <span className="text-theme-text-tertiary" title="Not set">—</span>}
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ============================================================================
// NAMESPACE SECTION
// ============================================================================
// Namespace-level summary: which LimitRanges exist and what each one declares,
// linking out to the full tables. Reached from the namespace an operator is
// already troubleshooting, which is where the question "what gets applied to
// pods here?" actually comes up.

export function NamespaceLimitRangesSection({
  limitRanges,
  loading,
  error,
  namespace,
  onNavigate,
}: {
  limitRanges?: any[]
  loading?: boolean
  error?: unknown
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  const denied = isForbiddenError(error)
  const showCached = !!error && !denied && !!limitRanges?.length
  const empty = !error && !loading && limitRanges?.length === 0

  return (
    <Section
      key={`${namespace}-${empty ? 'empty' : 'rules'}`}
      title={empty ? 'Limit Ranges (0)' : 'Limit Ranges'}
      icon={SlidersHorizontal}
      defaultExpanded={!empty}
    >
      {error != null && (
        <div className="mb-2">
          <LookupFailureNote errors={[error]} what="this namespace’s LimitRanges" />
          {showCached && (
            <div className="mt-1 text-xs text-theme-text-secondary">
              Showing previously loaded rules. They may have changed.
            </div>
          )}
        </div>
      )}
      {error && !showCached ? null : !limitRanges ? (
        <div className="text-xs text-theme-text-secondary">
          {loading ? 'Loading limit ranges…' : 'Limit ranges have not been read yet.'}
        </div>
      ) : limitRanges.length === 0 ? (
        <div className="text-xs text-theme-text-secondary">
          No LimitRanges found in this namespace. Other admission policies may still apply.
        </div>
      ) : (
        <div className="space-y-2">
          {limitRanges.map((lr: any, index: number) => {
            const name = lr?.metadata?.name ?? `limitrange-${index}`
            const items = limitRangeItems(lr)
            return (
              <div key={name} className="card-inner">
                <div className="text-xs font-medium text-theme-text-primary">
                  <ResourceLink
                    name={name}
                    kind="limitranges"
                    namespace={lr?.metadata?.namespace || namespace}
                    onNavigate={onNavigate}
                  />
                </div>
                {items.length === 0 ? (
                  <div className="mt-1 text-xs text-theme-text-tertiary">No entries.</div>
                ) : (
                  <div className="mt-1 space-y-0.5">
                    {items.map((item: any, itemIndex: number) => {
                      const facets = entryFacets(item)
                      return (
                        <div key={`${item?.type ?? 'entry'}-${itemIndex}`} className="text-xs text-theme-text-tertiary">
                          <span className="text-theme-text-secondary">{item?.type || 'Unknown'}</span>
                          {facets.length > 0 && ` · ${facets.map(f => f.label.toLowerCase()).join(', ')}`}
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </Section>
  )
}

// ============================================================================
// CONTEXTUAL LINK (Pod / workload resource sections)
// ============================================================================

const SCOPE_CAVEAT: Record<'pod' | 'workload', string> = {
  pod: 'The namespace’s current rules for admitting pods — not a record of what was applied to this pod. Other admission plugins may also change a pod’s resources.',
  workload:
    'The namespace’s current rules for admitting this workload’s pods, not the workload object itself. Other admission plugins may also change a pod’s resources.',
}

/** Small pointer from a resource's requests/limits to the namespace rules that
 *  govern them. Renders nothing until the host has names to show, so a pending
 *  or unreadable lookup makes no claim either way. */
export function NamespaceLimitRangeLink({
  namespace,
  names,
  scope,
  onNavigate,
}: {
  namespace: string
  names?: string[] | null
  scope: 'pod' | 'workload'
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  if (!names?.length) return null

  return (
    <div className="mt-3 pt-2 border-t border-theme-border text-xs text-theme-text-tertiary">
      <div>
        Namespace defaults &amp; constraints:{' '}
        {names.map((name, index) => (
          <span key={name}>
            {index > 0 && ', '}
            <ResourceLink name={name} kind="limitranges" namespace={namespace} onNavigate={onNavigate} />
          </span>
        ))}
        {' '}
        <Tooltip content={SCOPE_CAVEAT[scope]}>
          <button
            type="button"
            aria-label="About namespace defaults and constraints"
            className="inline-flex align-middle text-theme-text-tertiary hover:text-theme-text-secondary"
          >
            <Info className="w-3 h-3" />
          </button>
        </Tooltip>
      </div>
    </div>
  )
}
