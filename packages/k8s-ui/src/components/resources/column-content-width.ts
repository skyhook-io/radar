// How wide each automatically sized column's content is, measured from the
// data of every item rather than from the DOM: the table is virtualized, so the
// DOM only ever holds the rows on screen.

import type { ColumnSizingMode } from './column-sizing'
import { percentile } from './column-sizing'
import {
  formatAge,
  getArgoAppProjectDescription,
  getArgoApplicationRepo,
  getCellFilterValue,
  getConfigMapKeys,
  getConfigMapSize,
  getContainerSquareStates,
  getFluxHelmReleaseMessage,
  getGatewayClassDescription,
  getHPATarget,
  getIngressHosts,
  getIngressRules,
  getPodProblems,
  getPodRestarts,
  getPodStatus,
  getReplicaSetOwner,
  getServiceSelector,
  getWorkloadImages,
} from './resource-utils'
import { getGitRepositoryUrl, getHelmRepositoryUrl, getOCIRepositoryUrl } from './resource-utils-flux'
import { getCrossplanePauseState, isComposite, isManagedResource } from './resource-utils-crossplane'
import { getPodGpuCount } from '../../utils/extended-resources'
import { getCRDColumnValue } from './resource-utils-crd'

// Cell text styles, matching the classes the cells render with (text-sm /
// text-xs in the app font). measureText takes a CSS font shorthand.
const FONT_FAMILY = "'DM Sans Variable', 'DM Sans', system-ui, sans-serif"
const MONO_FAMILY = "'DM Mono', ui-monospace, SFMono-Regular, Menlo, monospace"
export const CELL_FONTS = {
  body: `400 14px ${FONT_FAMILY}`,
  medium: `500 14px ${FONT_FAMILY}`,
  small: `400 12px ${FONT_FAMILY}`,
  badge: `500 12px ${FONT_FAMILY}`,
  // The usage bars' labels: text-sm / text-xs / text-[10px] font-mono.
  monoSm: `400 14px ${MONO_FAMILY}`,
  monoXs: `400 12px ${MONO_FAMILY}`,
  mono10: `400 10px ${MONO_FAMILY}`,
  tiny: `400 10px ${FONT_FAMILY}`,
} as const
export type CellFont = keyof typeof CELL_FONTS

/** Width of `text` in `font`, in px; may stop counting once past `limit`. */
export type MeasureText = (text: string, font: CellFont, limit?: number) => number

export const CELL_PADDING_PX = 32 // px-4 on both sides
// The copy button that sits beside every name: gap-1.5 + p-0.5 around a w-3 icon.
const NAME_COPY_BUTTON_PX = 22
// The Terminating chip beside a deleting resource's name, with its gap.
const NAME_TERMINATING_CHIP_PX = 104
// The audit-findings count beside a name, with its gap.
const NAME_AUDIT_BADGE_PX = 36
// A Crossplane pause chip beside a name, besides its label: gap, padding, icon.
const NAME_PAUSE_CHIP_CHROME_PX = 6 + 12 + 12 + 4
// A .badge around its label: px-2 on both sides and a 1px border.
const BADGE_CHROME_PX = 18
// The problems icon beside a status badge or a ready count, with its gap.
const PROBLEM_ICON_PX = 8 + 14

export const NAME_MIN_PX = 200
export const NAME_MAX_PX = 480
const NAMESPACE_MAX_PX = 320
const NODE_MAX_PX = 400
// Grow columns stop at their longest value or this, whichever is less. Past it a
// line is easier to read in the drawer, and the room is worth more to the
// columns after it, which a very wide column would push under the drawer.
export const GROW_MAX_PX = 960
const FIT_DEFAULT_MAX_PX = 400

// ---------------------------------------------------------------------------
// Text measurement
// ---------------------------------------------------------------------------

// Each character is measured once per font and a string's width is the sum.
// That skips kerning: measured against the rendered text it came out 0.3–0.8%
// wider on average (at most ~15px, on a 2000px Event message). It makes a 50k-row pass a few lookups per character
// instead of a canvas call per string, so there is no per-string cache to bound.
// Without a canvas (SSR, jsdom) this falls back to an average glyph width.
const FALLBACK_CHAR_PX: Record<CellFont, number> = { body: 7.5, medium: 7.8, small: 6.5, badge: 6.8, monoSm: 8.4, monoXs: 7.2, mono10: 6, tiny: 5.5 }
let measureCtx: CanvasRenderingContext2D | null | undefined
let charWidths: Partial<Record<CellFont, Map<number, number>>> = {}

/** Drop cached glyph widths, e.g. once the web font has replaced its fallback. */
export function resetCellTextMeasure(): void {
  measureCtx = undefined
  charWidths = {}
}

export const measureCellText: MeasureText = (text, font, limit = Infinity) => {
  if (measureCtx === undefined) {
    measureCtx = typeof document === 'undefined' ? null : document.createElement('canvas').getContext('2d')
  }
  let cache = charWidths[font]
  if (!cache) {
    cache = new Map()
    charWidths[font] = cache
  }
  // The cell renders on one line, where runs of whitespace (newlines in an
  // Event message, say) collapse to a single space.
  const shown = /\s\s|[\t\n\r\f]|^\s|\s$/.test(text) ? text.replace(/\s+/g, ' ').trim() : text
  let width = 0
  for (let i = 0; i < shown.length; i++) {
    // By code point, so an emoji or other astral character is one glyph.
    const code = shown.codePointAt(i)!
    const glyph = code > 0xffff ? String.fromCodePoint(code) : shown[i]
    if (code > 0xffff) i++
    let w = cache.get(code)
    if (w === undefined) {
      if (measureCtx) {
        measureCtx.font = CELL_FONTS[font]
        w = measureCtx.measureText(glyph).width
      } else {
        w = FALLBACK_CHAR_PX[font]
      }
      cache.set(code, w)
    }
    width += w
    if (width > limit) return width
  }
  return width
}

// ---------------------------------------------------------------------------
// Which columns size themselves, and how wide a cell's content is
// ---------------------------------------------------------------------------

export interface SizableColumn {
  key: string
  sizing?: ColumnSizingMode
  growWeight?: number
}

export interface ColumnSizingSpec {
  mode: ColumnSizingMode
  /** grow: share of the room left over. fit: share of it while reaching from its 95th percentile to its longest value. */
  weight?: number
  max?: number
  /** Name: sized to its longest value (not its 95th percentile); when room is short it
   *  gives way last, and only down to the 90th percentile of its names. */
  keep?: boolean
  /** When room is short, lower ranks give way first (see ColumnLayoutInput). */
  yieldRank?: number
}

export function columnSizingSpec(col: SizableColumn): ColumnSizingSpec {
  if (col.sizing === 'grow') return { mode: 'grow', weight: col.growWeight ?? 1, max: GROW_MAX_PX }
  if (col.sizing === 'exact') return { mode: 'exact' }
  // Name is what a reader scans for, so it fits its longest name (up to the
  // cap) and is the last column to give way when room is short — and then
  // only down to the 90th percentile of its names, so just the longest are
  // cut before the table scrolls sideways. Other fit columns size to their 95th
  // percentile, so a few outliers (one long namespace) don't set the width,
  // and reach for their longest value from the room left over.
  switch (col.key) {
    case 'name': return { mode: 'fit', max: NAME_MAX_PX, keep: true, yieldRank: 4 }
    case 'namespace': return { mode: 'fit', max: NAMESPACE_MAX_PX, weight: 1 }
    // Node is worth showing but is the first to give way when room is short.
    case 'node': return { mode: 'fit', max: NODE_MAX_PX, weight: 1, yieldRank: 1 }
  }
  if (col.sizing === 'fit') return { mode: 'fit', max: FIT_DEFAULT_MAX_PX, weight: 1 }
  return { mode: 'exact' }
}

export type CellWidth = (resource: any, measure: MeasureText, limit: number) => number
const text = (read: (resource: any) => string | null | undefined, font: CellFont = 'body'): CellWidth =>
  (resource, measure, limit) => measure(read(resource) || '-', font, limit)

// Display text of the cells a curated kind flags as grow, where it differs from
// getCellFilterValue. Keyed `${plural}:${column}`.
const workloadImages = text(r => workloadImagesText(getWorkloadImages(r)))
const CURATED_CELL_WIDTHS: Record<string, CellWidth> = {
  'events:message': text(r => r.message),
  'events:object': text(r => r.involvedObject ? `${r.involvedObject.kind}/${r.involvedObject.name}` : '-'),
  'services:selector': text(getServiceSelector),
  'configmaps:keys': (r, measure, limit) => {
    const { count, preview } = getConfigMapKeys(r)
    const countWidth = measure(String(count), 'body')
    return count > 0 ? countWidth + 8 + measure(`(${preview})`, 'small', limit) : countWidth
  },
  'deployments:images': workloadImages,
  'statefulsets:images': workloadImages,
  'daemonsets:images': workloadImages,
  'rollouts:images': workloadImages,
  'replicasets:owner': text(getReplicaSetOwner),
  'persistentvolumeclaims:volume': text(r => r.spec?.volumeName),
  'ingresses:hosts': text(getIngressHosts),
  'ingresses:rules': text(getIngressRules),
  'hpas:target': text(getHPATarget),
  'horizontalpodautoscalers:target': text(getHPATarget),
  'helmreleases:message': text(getFluxHelmReleaseMessage),
  'gitrepositories:url': text(getGitRepositoryUrl),
  'ocirepositories:url': text(getOCIRepositoryUrl),
  'helmrepositories:url': text(getHelmRepositoryUrl),
  'applications:repo': text(getArgoApplicationRepo),
  'appprojects:description': text(getArgoAppProjectDescription),
  'gatewayclasses:description': text(getGatewayClassDescription),
  'priorityclasses:description': text(r => r.description),
}

// What an exact column's cells show, for the kinds whose tables most often run
// short of room. Measured so the column can shrink to its content when the
// table is too narrow; an exact column without a reader here never shrinks
// below its curated width, because its content is unknown. Keyed
// `${plural}:${column}`, or `*:${column}` for a cell every kind renders alike.
const badge = (label: (resource: any) => string | null | undefined): CellWidth =>
  (resource, measure, limit) => measure(label(resource) || '-', 'badge', limit) + BADGE_CHROME_PX
const age: CellWidth = (resource, measure) => {
  const created = resource?.metadata?.creationTimestamp
  return measure(created ? formatAge(created) : '-', 'body')
}
const workloadReady: CellWidth = (r, measure) =>
  measure(`${Math.min(r.status?.readyReplicas || 0, r.spec?.replicas ?? 1)}/${r.spec?.replicas ?? 1}`, 'medium') + PROBLEM_ICON_PX
const podStatusBadge = badge(p => getPodStatus(p).text)
const EXACT_CELL_WIDTHS: Record<string, CellWidth> = {
  '*:age': age,
  'pods:status': (r, measure, limit) => podStatusBadge(r, measure, limit) + (getPodProblems(r).length > 1 ? PROBLEM_ICON_PX : 0),
  'pods:restarts': text(r => String(getPodRestarts(r)), 'medium'),
  'pods:gpu': text(r => String(getPodGpuCount(r) || '-')),
  'events:type': badge(r => r.type || 'Normal'),
  'events:count': text(r => String(r.count || 1), 'medium'),
  'events:lastSeen': text(r => {
    const t = r.lastTimestamp || r.metadata?.creationTimestamp
    return t ? formatAge(t) : '-'
  }),
  'deployments:ready': workloadReady,
  'statefulsets:ready': workloadReady,
  'deployments:upToDate': text(r => String(r.status?.updatedReplicas || 0)),
  'deployments:available': text(r => String(r.status?.availableReplicas || 0)),
  'statefulsets:upToDate': text(r => String(r.status?.updatedReplicas || 0)),
  'replicasets:ready': text(r => `${r.status?.readyReplicas || 0}/${r.spec?.replicas || 0}`, 'medium'),
  'replicasets:status': badge(r => ((r.spec?.replicas || 0) > 0 ? 'Active' : 'Old')),
  'configmaps:size': text(getConfigMapSize),
  'pods:containers': (r) => containerSquaresWidth(getContainerSquareStates(r)),
  'customresourcedefinitions:kind': text(r => getCRDColumnValue(r, 'kind')),
  'customresourcedefinitions:scope': text(r => getCRDColumnValue(r, 'scope')),
}

// The Containers cell: a 10px square per container, 4px apart, with a divider
// (1px and its margins, plus a gap) between init and regular containers.
export function containerSquaresWidth(squares: { isInit?: boolean }[]): number {
  if (squares.length === 0) return 0
  let dividers = 0
  for (let i = 1; i < squares.length; i++) if (!!squares[i].isInit !== !!squares[i - 1].isInit) dividers++
  return squares.length * 10 + (squares.length - 1) * 4 + dividers * (5 + 4)
}

/** What a usage bar shows, for measuring: "used / limit" and a percentage, or a bare usage and a tag. */
export type UsageLabel = { used: string; total: string; percent: string } | { usage: string; tag: string }

/** Width of a usage bar's label, as ResourceBar and the no-limit cell lay it out. */
export function usageLabelWidth(label: UsageLabel | null, measure: MeasureText): number {
  if (!label) return measure('-', 'body')
  if ('usage' in label) return measure(label.usage, 'monoSm') + 6 + measure(label.tag, 'tiny')
  return measure(`${label.used} / ${label.total}`, 'monoXs') + 4 + measure(label.percent, 'mono10')
}

/**
 * The narrowest an exact column's content needs, cell padding included, or
 * undefined when Radar doesn't know what its cells show.
 */
export function exactContentReader(plural: string, column: string): CellWidth | undefined {
  return EXACT_CELL_WIDTHS[`${plural}:${column}`] ?? EXACT_CELL_WIDTHS[`*:${column}`]
}

/** Whether a curated grow column has its own display-text reader (tested for drift). */
export function hasCuratedCellWidth(plural: string, column: string): boolean {
  return `${plural}:${column}` in CURATED_CELL_WIDTHS
}

/** The images cell: the first image in full, and how many more. */
export function workloadImagesText(images: string[]): string {
  if (images.length === 0) return ''
  return images.length === 1 ? images[0] : `${images[0]} +${images.length - 1}`
}

export interface ContentColumn extends SizableColumn {
  getFilterValue?: (resource: any) => string
}

export interface ContentContext {
  /** Normalized plural, as the curated column sets are keyed. */
  plural: string
  /** The kind getCellFilterValue expects (generic kinds are prefixed). */
  cellFilterKind: string
  /** Audit findings keyed "namespace/name", which add a badge beside the name. */
  auditBadges?: Record<string, { danger: number; warning: number }>
  /** Readers for cells drawn from data outside the resource (metrics), keyed by column. */
  hostCellWidths?: Record<string, CellWidth>
}

function nameCellWidth(resource: any, measure: MeasureText, limit: number, ctx: ContentContext): number {
  const meta = resource?.metadata || {}
  let width = measure(meta.name || '', 'medium', limit) + NAME_COPY_BUTTON_PX
  if (meta.deletionTimestamp) width += NAME_TERMINATING_CHIP_PX
  const audit = ctx.auditBadges?.[`${meta.namespace || ''}/${meta.name}`]
  if (audit && audit.danger + audit.warning > 0) width += NAME_AUDIT_BADGE_PX
  // The same condition the name cell shows the chip under.
  const pause = isManagedResource(resource) || isComposite(resource) || getCrossplanePauseState(resource) === 'acknowledged'
    ? getCrossplanePauseState(resource)
    : null
  if (pause) width += NAME_PAUSE_CHIP_CHROME_PX + measure(pause === 'requested' ? 'Pause requested' : 'Paused', 'small')
  return width
}

function cellWidthReader(col: ContentColumn, ctx: ContentContext): CellWidth {
  if (col.key === 'name') return (r, m, limit) => nameCellWidth(r, m, limit, ctx)
  if (col.key === 'namespace') return text(r => r?.metadata?.namespace)
  if (col.getFilterValue) return text(col.getFilterValue)
  return CURATED_CELL_WIDTHS[`${ctx.plural}:${col.key}`]
    ?? text(r => getCellFilterValue(r, col.key, ctx.cellFilterKind))
}

export interface MeasuredContent {
  /** grow, exact and Name: the longest value; other fit columns: the 95th percentile. Padding included. */
  content: number
  /** fit only: its longest value, which it may grow to from the room left over. */
  stretch?: number
  /** Name only: the 90th percentile of its names, the narrowest it gives way to. */
  yieldTo?: number
}

/**
 * Content widths over every item: of the automatically sized columns, and of
 * the exact columns Radar knows the content of (how far they may shrink when
 * room is short). Each value stops being counted once past the column's cap,
 * so an Event message thousands of pixels long costs no more than one at the
 * cap.
 */
export function measureColumnContent(
  items: readonly any[],
  columns: ContentColumn[],
  ctx: ContentContext,
  measure: MeasureText = measureCellText,
): Record<string, MeasuredContent> {
  const result: Record<string, MeasuredContent> = {}
  for (const col of columns) {
    const spec = columnSizingSpec(col)
    if (spec.mode === 'exact') {
      const read = col.getFilterValue ? undefined : ctx.hostCellWidths?.[col.key] ?? exactContentReader(ctx.plural, col.key)
      if (!read) continue
      let longest = 0
      for (const item of items) longest = Math.max(longest, safeWidth(read, item, measure, Infinity))
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
      continue
    }
    const read = cellWidthReader(col, ctx)
    const limit = (spec.max ?? Infinity) - CELL_PADDING_PX
    if (spec.mode === 'fit' && spec.keep) {
      const widths = new Array<number>(items.length)
      let longest = 0
      for (let i = 0; i < items.length; i++) {
        widths[i] = safeWidth(read, items[i], measure, limit)
        if (widths[i] > longest) longest = widths[i]
      }
      result[col.key] = {
        content: Math.ceil(longest) + CELL_PADDING_PX,
        yieldTo: Math.ceil(percentile(widths, 0.9)) + CELL_PADDING_PX,
      }
    } else if (spec.mode === 'fit') {
      const widths = new Array<number>(items.length)
      let longest = 0
      for (let i = 0; i < items.length; i++) {
        widths[i] = safeWidth(read, items[i], measure, limit)
        if (widths[i] > longest) longest = widths[i]
      }
      result[col.key] = {
        content: Math.ceil(percentile(widths, 0.95)) + CELL_PADDING_PX,
        stretch: Math.ceil(longest) + CELL_PADDING_PX,
      }
    } else {
      let longest = 0
      for (const item of items) {
        const w = safeWidth(read, item, measure, limit)
        if (w > longest) longest = w
      }
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
    }
  }
  return result
}

// A reader that throws on an odd object must not take the table down; that
// cell just doesn't count toward the width.
function safeWidth(read: CellWidth, item: any, measure: MeasureText, limit: number): number {
  try {
    return read(item, measure, limit)
  } catch {
    return 0
  }
}
