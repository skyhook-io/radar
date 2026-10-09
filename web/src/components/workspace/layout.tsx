import type { ComponentType, ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { AlertTriangle, X } from 'lucide-react'
import { Tooltip, formatUpdatedAgo, grantParts, toneTextClass, type Grant } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { sameSelectedResource } from '../../utils/drawer-trail'
import { ROW_HOVER, TABLE_HEAD, TABLE_WRAP, TBODY, TD, TH } from './table'

export function Notice({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-surface px-3 py-2 text-sm text-theme-text-secondary">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div>{children}</div>
    </div>
  );
}

/** A whole screen with nothing to show: not installed, unreadable, or empty. */
export function ScreenEmptyState({
  icon: Icon,
  title,
  detail,
  action,
}: {
  icon: ComponentType<{ className?: string }>;
  title: string;
  detail: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center bg-theme-base p-6">
      <div className="max-w-md text-center">
        <Icon className="mx-auto h-9 w-9 text-theme-text-tertiary/50" />
        <h2 className="mt-3 text-lg font-medium text-theme-text-primary">
          {title}
        </h2>
        <p className="mt-1 text-sm text-theme-text-secondary">{detail}</p>
        {action}
      </div>
    </div>
  );
}

export function ScreenBody({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
      <div className="space-y-5 px-5 pb-6 pt-3 xl:px-7">{children}</div>
    </div>
  )
}

export function FilterChips({ chips }: { chips: { label: string; onClear: () => void }[] }) {
  if (chips.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1.5">
      {chips.map((c) => (
        <span key={c.label} className="inline-flex items-center gap-1 rounded-full bg-theme-elevated px-2.5 py-0.5 text-xs text-theme-text-secondary">
          {c.label}
          <button type="button" onClick={c.onClear} aria-label={`Remove ${c.label}`} className="rounded-full p-0.5 hover:bg-theme-hover">
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
    </div>
  )
}

export function namespaceChip(namespaces: string[], onClear: () => void) {
  return namespaces.length > 0 ? [{ label: `Namespace: ${namespaces.join(', ')}`, onClear }] : []
}

export function Segments<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T
  options: { id: T; label: string; count?: number | string }[]
  onChange: (id: T) => void
  label: string
}) {
  return (
    <div role="tablist" aria-label={label} className="inline-flex rounded-lg bg-theme-elevated p-0.5">
      {options.map((o) => {
        const on = o.id === value
        return (
          <button
            key={o.id}
            type="button"
            role="tab"
            aria-selected={on}
            onClick={() => onChange(o.id)}
            className={clsx(
              'inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-sm font-medium transition-colors',
              on ? 'bg-theme-surface text-theme-text-primary shadow-theme-sm' : 'text-theme-text-secondary hover:text-theme-text-primary',
            )}
          >
            {o.label}
            {o.count !== undefined && <span className="font-mono text-xs text-theme-text-tertiary">{o.count}</span>}
          </button>
        )
      })}
    </div>
  )
}

export interface TableColumn<T> {
  header: ReactNode
  width?: string
  cell: (row: T) => ReactNode
  className?: string
}

/** A workspace table. Rows inspect in the drawer; the inspected row is highlighted. */
export function SectionTable<T>({
  title,
  subtitle,
  columns,
  rows,
  rowKey,
  rowResource,
  onInspect,
  inspected,
  empty,
  minWidth = 760,
  footer,
}: {
  title: ReactNode
  subtitle?: ReactNode
  columns: TableColumn<T>[]
  rows: T[]
  rowKey: (row: T) => string
  rowResource?: (row: T) => SelectedResource | null
  onInspect?: (resource: SelectedResource) => void
  inspected?: SelectedResource | null
  empty: ReactNode
  minWidth?: number
  footer?: ReactNode
}) {
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h2 className="text-sm font-semibold text-theme-text-primary">{title}</h2>
        {subtitle && <span className="text-xs text-theme-text-tertiary">{subtitle}</span>}
      </div>
      <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
        {rows.length === 0 ? (
          <div className="px-4 py-5 text-sm text-theme-text-tertiary">{empty}</div>
        ) : (
          <div className={TABLE_WRAP}>
            <table className="w-full table-fixed" style={{ minWidth }}>
              <colgroup>
                {columns.map((c, i) => (
                  <col key={i} style={c.width ? { width: c.width } : undefined} />
                ))}
              </colgroup>
              <thead className={TABLE_HEAD}>
                <tr>
                  {columns.map((c, i) => (
                    <th key={i} className={TH}>{c.header}</th>
                  ))}
                </tr>
              </thead>
              <tbody className={TBODY}>
                {rows.map((row) => {
                  const res = rowResource?.(row) ?? null
                  const active = !!res && sameSelectedResource(inspected, res)
                  return (
                    <tr
                      key={rowKey(row)}
                      onClick={res && onInspect ? () => onInspect(res) : undefined}
                      className={clsx(res && onInspect && 'cursor-pointer', ROW_HOVER, active && 'selection')}
                      aria-selected={res ? active : undefined}
                    >
                      {columns.map((c, i) => (
                        <td key={i} className={clsx(TD, c.className)}>{c.cell(row)}</td>
                      ))}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {footer && <div className="mt-1.5 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

/**
 * Text that wraps only after `after` ("/" for a path, "-" for a resource
 * name), never mid-word; a single segment too long for its cell is cut with an
 * ellipsis. The whole value shows on hover.
 */
export function BreakText({ value, after, className }: { value: string; after: '/' | '-'; className?: string }) {
  const parts = value.split(after === '/' ? /(?<=\/)/ : /(?<=-)/)
  return (
    <Tooltip content={value} wrapperClassName="max-w-full">
      <span className={clsx('block max-w-full', className)}>
        {parts.map((p, i) => (
          <span key={i} className="inline-block max-w-full truncate align-top">
            {p}
          </span>
        ))}
      </span>
    </Tooltip>
  )
}

/** A URL or path; see BreakText. */
export function PathText({ value, className }: { value: string; className?: string }) {
  return <BreakText value={value} after="/" className={clsx('font-mono text-[12.5px]', className)} />
}

/**
 * A grant as one unit: the verb and resource in a code span that does not
 * wrap, its scope ("cluster-wide", "in namespace pg") as plain text after it.
 */
export function GrantText({ grant }: { grant: Grant }) {
  const { what, scope } = grantParts(grant)
  return (
    <>
      <code className="whitespace-nowrap rounded bg-theme-elevated px-1 font-mono text-[12px]">{what}</code>
      {scope}
    </>
  )
}

export function Mono({ children }: { children: ReactNode }) {
  return <Tooltip content={children} wrapperClassName="min-w-0"><span className="font-mono text-[12.5px] break-normal [overflow-wrap:anywhere]">{children}</span></Tooltip>
}

export function Sub({ children }: { children: ReactNode }) {
  return <div className="mt-0.5 text-xs text-theme-text-tertiary break-words">{children}</div>
}

type RefreshableQuery = Pick<UseQueryResult<unknown>, 'isRefetchError' | 'error' | 'dataUpdatedAt'>

/**
 * A refetch failed while the last good answer stays on screen: say so, why,
 * and how old that answer is, so cached values are not read as current.
 */
export function RefreshFailedNotice({ queries, className }: { queries: RefreshableQuery[]; className?: string }) {
  const failed = queries.filter((q) => q.isRefetchError)
  if (failed.length === 0) return null
  const oldest = failed.reduce((a, b) => (b.dataUpdatedAt < a.dataUpdatedAt ? b : a))
  const reason = oldest.error instanceof Error ? oldest.error.message : 'unknown error'
  return (
    <div role="status" className={clsx('flex items-start gap-1.5 text-xs text-theme-text-secondary', className)}>
      <AlertTriangle className={clsx('mt-px h-3.5 w-3.5 shrink-0', toneTextClass('degraded'))} />
      <span>
        Last refresh failed: {reason.length > 160 ? `${reason.slice(0, 160)}…` : reason} · showing data from {formatUpdatedAgo(Date.now() - oldest.dataUpdatedAt)}
      </span>
    </div>
  )
}
