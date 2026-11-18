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
import { getArgoApplicationSync } from './resource-utils-argo'
import { getResourceSlicePool } from './resource-utils-dra'
import { getGitRepositoryUrl, getHelmRepositoryUrl, getOCIRepositoryUrl } from './resource-utils-flux'
import { getEC2NodeClassStatus, getNodeClaimNodeName, getNodePoolDisruptionPolicy, getNodePoolLimits, getNodePoolStatus } from './resource-utils-karpenter'
import { getClusterQueueCohort } from './resource-utils-kueue'
import { getCrossplanePauseState, isComposite, isManagedResource } from './resource-utils-crossplane'
import { getPodGpuCount } from '../../utils/extended-resources'

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
// A short value — a badge, an enum, a version, an object's name — is useless
// cut, so a column flagged fit sizes to its longest one. Past this it is not
// short any more, and the cell cuts it like any other.
const SHORT_VALUE_MAX_PX = 400

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
  /** grow: share of the room left over. fit: share of it while reaching from its 95th percentile to its longest value. */
  weight?: number
  max?: number
  /** Name: sized to its longest value (not its 95th percentile) and never given back when room is short. */
  keep?: boolean
  /** When room is short, lower ranks give way first (see ColumnLayoutInput). */
  yieldRank?: number
  /** fit: size to the longest value instead of the 95th percentile. */
  longest?: boolean
}

export function columnSizingSpec(col: SizableColumn): ColumnSizingSpec {
  if (col.sizing === 'grow') return { mode: 'grow', weight: col.growWeight ?? 1, max: GROW_MAX_PX }
  if (col.sizing === 'exact') return { mode: 'exact' }
  // Name is what a reader scans for, so it fits its longest name (up to the
  // cap) and holds that width: the table scrolls sideways before a name that
  // fits under the cap is cut. Other fit columns size to their 95th
  // percentile, so a few outliers (one long namespace) don't set the width,
  // and reach for their longest value from the room left over.
  switch (col.key) {
    case 'name': return { mode: 'fit', max: NAME_MAX_PX, keep: true }
    case 'namespace': return { mode: 'fit', max: NAMESPACE_MAX_PX, weight: 1 }
    // Node is worth showing but is the first to give way when room is short.
    case 'node': return { mode: 'fit', max: NODE_MAX_PX, weight: 1, yieldRank: 1 }
  }
  // A column flagged fit holds short values (see SHORT_VALUE_MAX_PX), which
  // have no outliers worth trimming: one ExternalName among a hundred
  // ClusterIPs still needs its whole badge. When room is short it gives way
  // after the exact columns, down to its curated width.
  if (col.sizing === 'fit') return { mode: 'fit', max: SHORT_VALUE_MAX_PX, longest: true }
  return { mode: 'exact' }
}

type CellWidth = (resource: any, measure: MeasureText, limit: number) => number
const text = (read: (resource: any) => string | null | undefined, font: CellFont = 'body'): CellWidth =>
  (resource, measure, limit) => measure(read(resource) || '-', font, limit)
// A cell that is one .badge holding the label.
const badge = (label: (resource: any) => string | null | undefined): CellWidth =>
  (resource, measure, limit) => badgeWidth(label(resource), measure, limit)
function badgeWidth(label: string | null | undefined, measure: MeasureText, limit = Infinity): number {
  return measure(label || '-', 'badge', limit) + BADGE_CHROME_PX
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
  'events:reason': text(r => r.reason, 'medium'),
  'nodepools:status': badge(r => getNodePoolStatus(r).text),
  'nodepools:limits': text(getNodePoolLimits),
  'nodepools:disruption': text(getNodePoolDisruptionPolicy),
  'nodeclaims:nodeName': text(getNodeClaimNodeName),
  'ec2nodeclasses:status': badge(r => getEC2NodeClassStatus(r).text),
  'resourceslices:pool': text(getResourceSlicePool),
  'clusterqueues:status': badge(r => getCellFilterValue(r, 'status', 'clusterqueues')),
  'clusterqueues:cohort': text(getClusterQueueCohort),
  'applications:sync': badge(r => getArgoApplicationSync(r).status),
}

// What an exact column's cells show, for the kinds whose tables most often run
// short of room. Measured so the column can shrink to its content when the
// table is too narrow; an exact column without a reader here never shrinks
// below its curated width, because its content is unknown. Keyed
// `${plural}:${column}`, or `*:${column}` for a cell every kind renders alike.
const age: CellWidth = (resource, measure) => {
  const created = resource?.metadata?.creationTimestamp
  return measure(created ? formatAge(created) : '-', 'body')
}
const workloadReady: CellWidth = (r, measure) =>
  measure(`${Math.min(r.status?.readyReplicas || 0, r.spec?.replicas ?? 1)}/${r.spec?.replicas ?? 1}`, 'medium') + PROBLEM_ICON_PX
const EXACT_CELL_WIDTHS: Record<string, CellWidth> = {
  '*:age': age,
  'pods:status': (r, measure) => badgeWidth(getPodStatus(r).text, measure) + (getPodProblems(r).length > 1 ? PROBLEM_ICON_PX : 0),
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
}

// Columns drawn as a picture rather than text shrink no further than this when
// the table is short (cell padding included). The usage bars stay readable —
// the bar and its request marker scale with the cell — down to about this
// width; below it the "used / limit" label is cut.
const USAGE_BAR_MIN_PX = 120
const VISUAL_MIN_PX: Record<string, number> = {
  'pods:cpu': USAGE_BAR_MIN_PX,
  'pods:memory': USAGE_BAR_MIN_PX,
  'nodes:cpu': USAGE_BAR_MIN_PX,
  'nodes:memory': USAGE_BAR_MIN_PX,
}

/**
 * The narrowest an exact column's content needs, cell padding included, or
 * undefined when Radar doesn't know what its cells show.
 */
export function exactContentReader(plural: string, column: string): CellWidth | undefined {
  return EXACT_CELL_WIDTHS[`${plural}:${column}`] ?? EXACT_CELL_WIDTHS[`*:${column}`]
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
  if (col.key === 'name') return (r, m, limit) => nameCellWidth(r, m, limit, ctx)
  if (col.key === 'namespace') return text(r => r?.metadata?.namespace)
  if (col.getFilterValue) return text(col.getFilterValue)
  return CURATED_CELL_WIDTHS[`${ctx.plural}:${col.key}`]
    ?? text(r => getCellFilterValue(r, col.key, ctx.cellFilterKind))
}

export interface MeasuredContent {
  /** grow, exact, Name and flagged fit columns: the longest value; Namespace and Node: the 95th percentile. Padding included. */
  content: number
  /** fit only: its longest value, which it may grow to from the room left over. */
  stretch?: number
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
      const visual = VISUAL_MIN_PX[`${ctx.plural}:${col.key}`]
      if (visual !== undefined) {
        result[col.key] = { content: visual }
        continue
      }
      const read = col.getFilterValue ? undefined : exactContentReader(ctx.plural, col.key)
      if (!read) continue
      let longest = 0
      for (const item of items) longest = Math.max(longest, safeWidth(read, item, measure, Infinity))
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
      continue
    }
    const read = cellWidthReader(col, ctx)
    const limit = (spec.max ?? Infinity) - CELL_PADDING_PX
    if (spec.mode === 'fit' && (spec.keep || spec.longest)) {
      let longest = 0
      for (const item of items) longest = Math.max(longest, safeWidth(read, item, measure, limit))
      result[col.key] = { content: Math.ceil(longest) + CELL_PADDING_PX }
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
