// How wide each automatically sized column's content is, measured from the
// data of every item rather than from the DOM: the table is virtualized, so the
// DOM only ever holds the rows on screen.

import type { ColumnSizingMode } from './column-sizing'
import { percentile } from './column-sizing'
import {
  getArgoAppProjectDescription,
  getArgoApplicationRepo,
  getCellFilterValue,
  getConfigMapKeys,
  getFluxHelmReleaseMessage,
  getGatewayClassDescription,
  getHPATarget,
  getIngressHosts,
  getNodeVersion,
  getIngressRules,
  getReplicaSetOwner,
  getServiceEndpointsStatus,
  getServicePorts,
  getServiceSelector,
  getServiceStatus,
  getWorkloadImages,
} from './resource-utils'
import { getNodePoolDisruptionPolicy, getNodePoolLimits, getNodePoolNodeClassRef, getNodePoolStatus } from './resource-utils-karpenter'
import { getGitRepositoryUrl, getHelmRepositoryUrl, getOCIRepositoryUrl } from './resource-utils-flux'
import { getCrossplanePauseState, isComposite, isManagedResource } from './resource-utils-crossplane'

// Cell text styles, matching the classes the cells render with (text-sm /
// text-xs in the app font). measureText takes a CSS font shorthand.
const FONT_FAMILY = "'DM Sans Variable', 'DM Sans', system-ui, sans-serif"
export const CELL_FONTS = {
  body: `400 14px ${FONT_FAMILY}`,
  medium: `500 14px ${FONT_FAMILY}`,
  small: `400 12px ${FONT_FAMILY}`,
  badge: `500 12px ${FONT_FAMILY}`,
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

export const NAME_MIN_PX = 200
export const NAME_MAX_PX = 480
const NAMESPACE_MAX_PX = 320
const NODE_MAX_PX = 400
// Grow columns stop at their longest value or this, whichever is less. Past it a
// line is easier to read in the drawer, and the room is worth more to the
// columns after it, which a very wide column would push under the drawer.
export const GROW_MAX_PX = 960
// A short value — a badge, an enum, a version, an object's name — is useless
// cut, so its column fits the longest one. Past this it is not short any more,
// and the cell cuts it like any other.
const SHORT_VALUE_MAX_PX = 400
// .badge: px-2 and a 0.5px border on each side; .badge-sm: px-1.5.
const BADGE_CHROME_PX = 17
const BADGE_SM_CHROME_PX = 13

// ---------------------------------------------------------------------------
// Text measurement
// ---------------------------------------------------------------------------

// Each character is measured once per font and a string's width is the sum.
// That skips kerning: measured against the rendered text it came out 0.3–0.8%
// wider on average (at most ~15px, on a 2000px Event message). It makes a 50k-row pass a few lookups per character
// instead of a canvas call per string, so there is no per-string cache to bound.
// Without a canvas (SSR, jsdom) this falls back to an average glyph width.
const FALLBACK_CHAR_PX: Record<CellFont, number> = { body: 7.5, medium: 7.8, small: 6.5, badge: 6.8 }
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
  /** fit: size to the longest value instead of the 95th percentile. */
  longest?: boolean
  /** grow: share of the room left over. fit: share of it while reaching from its 95th percentile to its longest value. */
  weight?: number
  max?: number
}

export function columnSizingSpec(col: SizableColumn): ColumnSizingSpec {
  if (col.sizing === 'grow') return { mode: 'grow', weight: col.growWeight ?? 1, max: GROW_MAX_PX }
  if (col.sizing === 'exact') return { mode: 'exact' }
  // Fit columns size to their 95th percentile, so a few outliers (one long
  // namespace, one generated name) don't set the width, and reach for their
  // longest value from the room left over. Name takes twice the share there:
  // a cut name costs more than any other cut cell.
  switch (col.key) {
    case 'name': return { mode: 'fit', max: NAME_MAX_PX, weight: 2 }
    case 'namespace': return { mode: 'fit', max: NAMESPACE_MAX_PX, weight: 1 }
    case 'node': return { mode: 'fit', max: NODE_MAX_PX, weight: 1 }
  }
  // A column flagged fit holds short values (see SHORT_VALUE_MAX_PX), which
  // have no outliers worth trimming: one ExternalName among a hundred
  // ClusterIPs still needs its whole badge.
  if (col.sizing === 'fit') return { mode: 'fit', max: SHORT_VALUE_MAX_PX, longest: true }
  return { mode: 'exact' }
}

type CellWidth = (resource: any, measure: MeasureText, limit: number, ctx: ContentContext) => number
type CellText = (resource: any) => string | null | undefined
const text = (read: CellText, font: CellFont = 'body'): CellWidth =>
  (resource, measure, limit) => measure(read(resource) || '-', font, limit)
// A cell that is one .badge (or .badge-sm) holding the text.
const badge = (read: CellText, size: 'badge' | 'badge-sm' = 'badge'): CellWidth =>
  (resource, measure, limit) => badgeWidth(read(resource), measure, limit, size)
function badgeWidth(label: string | null | undefined, measure: MeasureText, limit: number, size: 'badge' | 'badge-sm' = 'badge'): number {
  if (!label) return measure('-', 'body')
  return size === 'badge'
    ? measure(label, 'badge', limit) + BADGE_CHROME_PX
    : measure(label, 'small', limit) + BADGE_SM_CHROME_PX
}

// What the cells a curated kind flags as grow or fit show, where it differs
// from getCellFilterValue's text: other text, or a badge's padding around it.
// Keyed `${plural}:${column}`.
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

  // fit
  'services:type': badge(r => getServiceStatus(r).text),
  // An audit finding replaces the badge with "No endpoints", as the cell does.
  'services:endpoints': (r, measure, limit, ctx) => {
    const audit = ctx.auditBadges?.[`${r?.metadata?.namespace || ''}/${r?.metadata?.name}`]
    return badgeWidth(audit && audit.danger + audit.warning > 0 ? 'No endpoints' : getServiceEndpointsStatus(r).status, measure, limit)
  },
  'services:ports': text(getServicePorts),
  // A skewed version renders medium weight; measure every one that way.
  'nodes:version': text(getNodeVersion, 'medium'),
  'events:reason': text(r => r.reason, 'medium'),
  'nodepools:status': badge(r => getNodePoolStatus(r).text),
  'nodepools:nodeClass': text(getNodePoolNodeClassRef),
  'nodepools:limits': text(getNodePoolLimits),
  'nodepools:disruption': text(getNodePoolDisruptionPolicy),
}

/** Whether a curated grow or fit column has its own reader of what its cell shows (tested for drift). */
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
  if (col.key === 'name') return nameCellWidth
  if (col.key === 'namespace') return text(r => r?.metadata?.namespace)
  if (col.getFilterValue) return text(col.getFilterValue)
  return CURATED_CELL_WIDTHS[`${ctx.plural}:${col.key}`]
    ?? text(r => getCellFilterValue(r, col.key, ctx.cellFilterKind))
}

export interface MeasuredContent {
  /** What the column sizes to: grow, its longest value; fit, its 95th percentile. Padding included. */
  content: number
  /** fit only: its longest value, which it may grow to from the room left over. */
  stretch?: number
}

/**
 * Content widths of the automatically sized columns over every item. Exact
 * columns are not measured. Each value stops being counted once past the
 * column's cap, so an Event message thousands of pixels long costs no more
 * than one at the cap.
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
    if (spec.mode === 'exact') continue
    const read = cellWidthReader(col, ctx)
    const limit = (spec.max ?? Infinity) - CELL_PADDING_PX
    if (spec.mode === 'fit' && spec.longest) {
      let longest = 0
      for (const item of items) {
        const w = safeWidth(read, item, measure, limit, ctx)
        if (w > longest) longest = w
      }
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
    } else if (spec.mode === 'fit') {
      const widths = new Array<number>(items.length)
      let longest = 0
      for (let i = 0; i < items.length; i++) {
        widths[i] = safeWidth(read, items[i], measure, limit, ctx)
        if (widths[i] > longest) longest = widths[i]
      }
      result[col.key] = {
        content: Math.ceil(percentile(widths, 0.95)) + CELL_PADDING_PX,
        stretch: Math.ceil(longest) + CELL_PADDING_PX,
      }
    } else {
      let longest = 0
      for (const item of items) {
        const w = safeWidth(read, item, measure, limit, ctx)
        if (w > longest) longest = w
      }
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
    }
  }
  return result
}

// A reader that throws on an odd object must not take the table down; that
// cell just doesn't count toward the width.
function safeWidth(read: CellWidth, item: any, measure: MeasureText, limit: number, ctx: ContentContext): number {
  try {
    return read(item, measure, limit, ctx)
  } catch {
    return 0
  }
}
