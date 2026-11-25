import type { ReactNode } from 'react'
import { Badge } from '../ui/Badge'
import { SearchBox } from '../ui/SearchBox'
import { SelectMenu } from '../ui/SelectMenu'
import { pluralize } from '../../utils/pluralize'
import { RIGHTSIZING_ACTION_SEVERITY } from './presentation'
import type { ClassFilter, ScanClass } from './results-model'

const ACTION_META: Record<
  'increase' | 'reduction' | 'review',
  { label: string; severity: 'warning' | 'info' | 'neutral'; helper: string }
> = {
  increase: {
    label: 'Increase or add',
    severity: RIGHTSIZING_ACTION_SEVERITY.increase,
    helper: 'Includes proposed increases that need review',
  },
  review: {
    label: 'Review first',
    severity: RIGHTSIZING_ACTION_SEVERITY.review,
    helper: 'Check safety signals or workloads with no replicas',
  },
  reduction: {
    label: 'Reduce requests',
    severity: RIGHTSIZING_ACTION_SEVERITY.reduction,
    helper: 'Reclaim meaningful capacity',
  },
}

export function RightsizingActionFilters({ counts, selected, defaultClass, onSelect, coverage }: {
  counts: Record<ScanClass, number>
  selected: ClassFilter
  defaultClass: 'actions' | 'all'
  onSelect: (value: ClassFilter) => void
  coverage?: ReactNode
}) {
  return (
    <>
      <div className="grid gap-2 md:grid-cols-3">
        {(Object.keys(ACTION_META) as Array<keyof typeof ACTION_META>).map((key) => {
          const meta = ACTION_META[key]
          return (
            <button
              key={key}
              aria-pressed={selected === key}
              type="button"
              onClick={() => onSelect(selected === key ? defaultClass : key)}
              className={`rounded-lg border p-3 text-left transition-colors ${selected === key ? 'border-accent bg-accent-muted' : 'border-theme-border bg-theme-base/50 hover:bg-theme-hover'}`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm font-medium text-theme-text-primary">{meta.label}</span>
                <Badge severity={meta.severity} size="sm">
                  {pluralize(counts[key], 'container')}
                </Badge>
              </div>
              <p className="mt-1 text-xs text-theme-text-tertiary">{meta.helper}</p>
            </button>
          )
        })}
      </div>
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-theme-text-tertiary">
        {coverage}
        <span className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            aria-pressed={selected === 'in_range'}
            onClick={() => onSelect(selected === 'in_range' ? defaultClass : 'in_range')}
            className="hover:text-theme-text-primary"
          >
            {pluralize(counts.in_range, 'container')} · no meaningful change
          </button>
          <button
            type="button"
            aria-pressed={selected === 'need_data'}
            onClick={() => onSelect(selected === 'need_data' ? defaultClass : 'need_data')}
            className="hover:text-theme-text-primary"
          >
            {pluralize(counts.need_data, 'container')} · needs evidence
          </button>
          {selected !== defaultClass && (
            <button
              type="button"
              onClick={() => onSelect(defaultClass)}
              className="font-medium text-accent-text hover:underline"
            >
              {defaultClass === 'all' ? 'All results' : 'Recommended actions'}
            </button>
          )}
        </span>
      </div>
    </>
  )
}

export function RightsizingResultFilters(props: {
  search: string
  searchShortcutEnabled?: boolean
  onSearch: (value: string) => void
  kind: string
  kinds: string[]
  onKind: (value: string) => void
  namespace: string
  namespaces: string[]
  onNamespace: (value: string) => void
  includeSystem: boolean
  hasSystem: boolean
  onScope: (value: string) => void
  shown: number
  total: number
  active: boolean
  onClear: () => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-theme-border p-3">
      <span className="text-xs font-medium text-theme-text-secondary">Filter results</span>
      <SearchBox
        value={props.search}
        onChange={props.onSearch}
        scope="global"
        shortcutId="rightsizing-search"
        shortcutEnabled={props.searchShortcutEnabled}
        placeholder="Search workloads…"
        ariaLabel="Search sizing results"
        className="mr-auto w-60 2xl:w-72"
      />
      <SelectMenu
        ariaLabel="Filter results by namespace"
        value={props.namespace}
        onChange={props.onNamespace}
        className="w-52"
        options={[
          { value: '', label: 'All scanned namespaces' },
          ...[...new Set([...props.namespaces, ...(props.namespace ? [props.namespace] : [])])].map((value) => ({ value, label: value })),
        ]}
      />
      <SelectMenu
        ariaLabel="Filter by workload kind"
        value={props.kind}
        onChange={props.onKind}
        className="w-36"
        options={[
          { value: '', label: 'All kinds' },
          ...[...new Set([...props.kinds, ...(props.kind ? [props.kind] : [])])].map((value) => ({ value, label: value })),
        ]}
      />
      {(props.hasSystem || props.includeSystem) && (
        <SelectMenu
          ariaLabel="Choose workload scope"
          value={props.includeSystem ? 'all' : 'applications'}
          onChange={props.onScope}
          className="w-44 2xl:w-48"
          options={[
            { value: 'applications', label: 'Hide system workloads' },
            {
              value: 'all',
              label: 'Include system workloads',
            },
          ]}
        />
      )}
      <span className="shrink-0 text-xs tabular-nums text-theme-text-tertiary">
        {props.shown} of {props.total} containers match
      </span>
      {props.active && (
        <button
          type="button"
          onClick={props.onClear}
          className="shrink-0 text-xs text-accent-text hover:underline"
        >
          Reset view
        </button>
      )}
    </div>
  )
}
