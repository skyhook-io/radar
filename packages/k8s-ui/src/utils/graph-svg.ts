import { midTruncate } from './format'
import type { GraphBox, GraphGroupTone, GraphLine, GraphModel, GraphRect, GraphStatus } from './graph-model'

export type GraphSvgTheme = 'light' | 'dark'

export interface GraphSvgOptions {
  name: string
  theme: GraphSvgTheme
  /** Page color behind the graph, or null for a transparent background. */
  background: string | null
  /** Area to show, in graph coordinates, such as the visible part of the graph.
   *  Anything outside it is cut off at its edge. Defaults to the whole graph with padding. */
  frame?: GraphRect
}

interface Paint {
  color: string
  opacity?: number
}

interface Palette {
  surface: string
  border: Paint
  text: string
  textSecondary: string
  textTertiary: string
  /** Port text in a row. */
  code: string
  groups: Record<Exclude<GraphGroupTone, 'addon'>, { border: Paint; header: Paint; label: string; icon: string }>
}

// Values of the theme tokens in theme/variables.css (light-dark pairs).
const PALETTES: Record<GraphSvgTheme, Palette> = {
  light: {
    surface: '#F8FBFE',
    border: { color: '#000000', opacity: 0.12 },
    text: '#1A1C20',
    textSecondary: '#555860',
    textTertiary: '#6B7080',
    code: '#2563EB',
    groups: {
      namespace: { border: { color: '#3B82F6', opacity: 0.3 }, header: { color: '#3B82F6', opacity: 0.15 }, label: '#1D4ED8', icon: '#2563EB' },
      app: { border: { color: '#10B981', opacity: 0.3 }, header: { color: '#10B981', opacity: 0.15 }, label: '#047857', icon: '#059669' },
      label: { border: { color: '#F59E0B', opacity: 0.3 }, header: { color: '#F59E0B', opacity: 0.15 }, label: '#B45309', icon: '#D97706' },
    },
  },
  dark: {
    surface: '#14192A',
    border: { color: '#FFFFFF', opacity: 0.12 },
    text: '#F5F6F8',
    textSecondary: '#D8DAE0',
    textTertiary: '#B0B5C0',
    code: '#93C5FD',
    groups: {
      namespace: { border: { color: '#3B82F6', opacity: 0.4 }, header: { color: '#3B82F6', opacity: 0.2 }, label: '#93C5FD', icon: '#60A5FA' },
      app: { border: { color: '#10B981', opacity: 0.4 }, header: { color: '#10B981', opacity: 0.2 }, label: '#6EE7B7', icon: '#34D399' },
      label: { border: { color: '#F59E0B', opacity: 0.4 }, header: { color: '#F59E0B', opacity: 0.2 }, label: '#FCD34D', icon: '#FBBF24' },
    },
  },
}

// Port text on a colored fill, and figures on a hot path (Tailwind cyan-300, orange-400).
const CODE_ON_FILL = '#67E8F9'
const HOT_TEXT = '#FB923C'

const ADDON = { border: '#A855F7', fill: '#3B0764', label: '#D8B4FE', labelBg: '#A855F7' }

// Left bar color per status, and the outline + wash of the statuses that have one.
const STATUS_BAR: Record<GraphStatus, string | null> = {
  healthy: '#22C55E',
  unknown: '#64748B',
  degraded: null,
  unhealthy: null,
  neutral: null,
}
// Status dot colors (SEVERITY_DOT in badge-colors).
const STATUS_DOT: Record<GraphStatus, string> = {
  healthy: '#22C55E',
  degraded: '#F59E0B',
  unhealthy: '#EF4444',
  neutral: '#38BDF8',
  unknown: '#64748B',
}
const STATUS_OUTLINE: Partial<Record<GraphStatus, { stroke: Paint; wash: Paint }>> = {
  degraded: { stroke: { color: '#EAB308', opacity: 0.6 }, wash: { color: '#FB923C', opacity: 0.12 } },
  unhealthy: { stroke: { color: '#EF4444', opacity: 0.7 }, wash: { color: '#F87171', opacity: 0.15 } },
  neutral: { stroke: { color: '#38BDF8', opacity: 0.55 }, wash: { color: '#38BDF8', opacity: 0.1 } },
}

const FONT = `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif`
const PADDING = 24
// Average glyph width as a share of the font size. Generous, so text stays inside its box.
const CHAR_RATIO = 0.6
const UPPER_RATIO = 0.72

function esc(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&apos;')
}

function n(value: number): string {
  return String(Math.round(value * 10) / 10)
}

function fill(p: Paint): string {
  return `fill="${esc(p.color)}"${p.opacity !== undefined ? ` fill-opacity="${p.opacity}"` : ''}`
}

function stroke(p: Paint): string {
  return `stroke="${esc(p.color)}"${p.opacity !== undefined ? ` stroke-opacity="${p.opacity}"` : ''}`
}

function fit(text: string, width: number, size: number, ratio = CHAR_RATIO): string {
  return midTruncate(text, Math.max(3, Math.floor(width / (size * ratio))))
}

function text(x: number, y: number, value: string, attrs: string): string {
  return `<text x="${n(x)}" y="${n(y)}" ${attrs}>${esc(value)}</text>`
}

function icon(key: string | undefined, x: number, y: number, size: number, color: string): string {
  if (!key) return ''
  return `<use xlink:href="#${key}" href="#${key}" x="${n(x)}" y="${n(y)}" width="${size}" height="${size}" color="${esc(color)}"/>`
}

function groupColors(box: GraphBox, p: Palette) {
  return p.groups[box.tone === 'app' || box.tone === 'label' ? box.tone : 'namespace']
}

function drawGroup(box: GraphBox, p: Palette): string {
  const { x, y, w, h } = box
  if (box.tone === 'addon') {
    const label = 'Cluster Addons'
    const pillW = 34 + label.length * 12 * 0.58
    return `<g><rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="12" fill="${ADDON.fill}" fill-opacity="0.4" stroke="${ADDON.border}" stroke-opacity="0.5" stroke-width="2" stroke-dasharray="6 4"/>` +
      `<rect x="${n(x + 12)}" y="${n(y + 8)}" width="${n(pillW)}" height="24" rx="6" fill="${ADDON.labelBg}" fill-opacity="0.3"/>` +
      icon(box.icon, x + 20, y + 13, 14, ADDON.label) +
      text(x + 40, y + 24, box.name, `font-size="12" font-weight="500" fill="${ADDON.label}"`) + '</g>'
  }
  const c = groupColors(box, p)
  const header = 56
  const count = box.count !== undefined ? String(box.count) : ''
  const countW = count ? count.length * 7 + 12 : 0
  const name = fit(box.name, w - 72 - countW - 16, 24)
  const countX = x + 56 + name.length * 24 * CHAR_RATIO + 12
  return `<g><rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="12" fill="${p.surface}" fill-opacity="0.4"/>` +
    `<path d="M${n(x)} ${n(y + header)}V${n(y + 12)}Q${n(x)} ${n(y)} ${n(x + 12)} ${n(y)}H${n(x + w - 12)}Q${n(x + w)} ${n(y)} ${n(x + w)} ${n(y + 12)}V${n(y + header)}Z" ${fill(c.header)}/>` +
    `<rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="12" fill="none" ${stroke(c.border)} stroke-width="2"/>` +
    icon(box.icon, x + 16, y + 14, 28, c.icon) +
    text(x + 56, y + 37, name, `font-size="24" font-weight="700" fill="${c.label}"`) +
    (count ? `<rect x="${n(countX)}" y="${n(y + 18)}" width="${countW}" height="20" rx="4" fill="${p.surface}" fill-opacity="0.5"/>` +
      text(countX + countW / 2, y + 32, count, `font-size="12" text-anchor="middle" fill="${p.textSecondary}"`) : '') +
    '</g>'
}

function drawCollapsedGroup(box: GraphBox, p: Palette): string {
  const { x, y, w, h } = box
  const addon = box.tone === 'addon'
  const c = groupColors(box, p)
  const border: Paint = addon ? { color: ADDON.border, opacity: 0.5 } : c.border
  const header: Paint = addon ? { color: ADDON.fill, opacity: 0.4 } : c.header
  const labelColor = addon ? ADDON.label : c.label
  const count = box.count !== undefined ? ` (${box.count})` : ''
  return `<g><rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="12" fill="${p.surface}"/>` +
    `<rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="12" ${fill(header)} ${stroke(border)} stroke-width="2"/>` +
    icon(box.icon, x + 12, y + 12, 16, addon ? ADDON.label : c.icon) +
    text(x + 34, y + 25, fit(`${box.name}${count}`, w - 46 - (box.status ? 14 : 0), 14), `font-size="14" font-weight="700" fill="${labelColor}"`) +
    (box.status ? `<circle cx="${n(x + w - 16)}" cy="${n(y + 20)}" r="4" fill="${STATUS_DOT[box.status]}"/>` : '') +
    (box.detail && h >= 44 ? text(x + 12, y + 46, fit(box.detail, w - 24, 11), `font-size="11" fill="${p.textSecondary}"`) : '') +
    '</g>'
}

function drawInternet(box: GraphBox, p: Palette): string {
  const { x, y, w, h } = box
  const accent = box.accent ?? '#0EA5E9'
  return `<g><rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="${n(h / 2)}" fill="${p.surface}"/>` +
    `<rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="${n(h / 2)}" fill="${esc(accent)}" fill-opacity="0.18" stroke="${esc(accent)}" stroke-opacity="0.5"/>` +
    icon(box.icon, x + 16, y + h / 2 - 10, 20, accent) +
    text(x + 42, y + h / 2 + 5, fit(box.name, w - 54, 14), `font-size="14" font-weight="500" fill="${p.text}"`) +
    '</g>'
}

function drawResource(box: GraphBox, p: Palette): string {
  const { x, y, w, h } = box
  const outline = box.status ? STATUS_OUTLINE[box.status] : undefined
  const bar = box.accent ?? (box.status ? STATUS_BAR[box.status] : null)
  const onFill = box.fill !== undefined
  const primary = onFill ? box.textColor ?? '#FFFFFF' : p.text
  const secondary = onFill ? primary : p.textSecondary
  const tertiary = onFill ? primary : p.textTertiary
  const dim = onFill ? ' fill-opacity="0.75"' : ''
  const padX = 12
  const textW = w - padX * 2 - 4
  const parts = [
    `<rect x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="8" fill="${esc(box.fill ?? p.surface)}" ${outline ? `${stroke(outline.stroke)} stroke-width="2"` : `${stroke(p.border)}`}/>`,
  ]
  if (box.tint) parts.push(`<rect x="${n(x + 0.5)}" y="${n(y + 0.5)}" width="${n(w - 1)}" height="${n(h - 1)}" rx="7.5" ${fill(box.tint)}/>`)
  const compact = h < 70
  const kindY = y + (compact ? 17 : 20)
  const nameY = y + (compact ? 35 : 41)
  const detailY = y + (compact ? 50 : 60)
  if (outline) parts.push(`<rect x="${n(x + 1)}" y="${n(y + 1)}" width="${n(w - 2)}" height="${n(h - 2)}" rx="7" ${fill(outline.wash)}/>`)
  if (bar) parts.push(`<rect x="${n(x + 1)}" y="${n(y + 6)}" width="3" height="${n(h - 12)}" rx="1.5" fill="${esc(bar)}"/>`)
  if (box.nameFirst) {
    drawNameFirst(box, parts, { primary, tertiary, dim, padX, textW, p })
    return `<g>${parts.join('')}</g>`
  }
  if (box.status) parts.push(`<circle cx="${n(x + w - padX - 3)}" cy="${n(kindY - 3.5)}" r="3" fill="${STATUS_DOT[box.status]}"/>`)
  let kindX = x + padX
  if (box.icon) {
    parts.push(icon(box.icon, kindX, kindY - 10, 13, tertiary))
    kindX += 19
  }
  if (box.kindLabel) {
    parts.push(text(kindX, kindY, fit(box.kindLabel.toUpperCase(), x + w - padX - kindX - (box.status ? 12 : 0), 10, UPPER_RATIO), `font-size="10" font-weight="500" letter-spacing="0.4" fill="${tertiary}"${dim}`))
  }
  parts.push(text(x + padX, nameY, fit(box.name, textW, compact ? 13 : 14), `font-size="${compact ? 13 : 14}" font-weight="500" fill="${primary}"`))
  if (box.detail && detailY + 4 <= y + h) {
    parts.push(text(x + padX, detailY, fit(box.detail, textW, compact ? 11 : 12), `font-size="${compact ? 11 : 12}" fill="${secondary}"${dim}`))
  }
  return `<g>${parts.join('')}</g>`
}

// Live Traffic card: icon and name, detail, then rows such as ":80   230". Baselines follow the
// card's 8px top padding, 20px name line, 16px small lines and 18px row pitch.
function drawNameFirst(
  box: GraphBox,
  parts: string[],
  c: { primary: string; tertiary: string; dim: string; padX: number; textW: number; p: Palette },
) {
  const { x, y, w, h } = box
  const nameY = y + 23
  let nameX = x + c.padX
  if (box.icon) {
    parts.push(icon(box.icon, nameX, nameY - 11, 13, c.tertiary))
    nameX += 19
  }
  parts.push(text(nameX, nameY, fit(box.name, x + w - c.padX - nameX, 14), `font-size="14" font-weight="500" fill="${c.primary}"`))
  let lineY = nameY
  if (box.detail) {
    lineY += 17
    parts.push(text(x + c.padX, lineY, fit(box.detail, c.textW, 12), `font-size="12" fill="${c.tertiary}"${c.dim}`))
  }
  lineY += 4
  const onFill = box.fill !== undefined
  for (const row of box.rows ?? []) {
    lineY += 18
    if (lineY + 4 > y + h) break
    const figure = row.hot ? HOT_TEXT : c.tertiary
    const figureDim = row.hot ? '' : c.dim
    const valueW = row.value ? row.value.length * 12 * CHAR_RATIO + 6 : 0
    const textAttrs = row.code
      ? `font-size="12" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" fill="${onFill ? CODE_ON_FILL : c.p.code}"`
      : `font-size="12"${row.hot ? ' font-weight="500"' : ''} fill="${figure}"${row.muted ? ' fill-opacity="0.75"' : figureDim}`
    parts.push(text(x + c.padX, lineY, fit(row.text, c.textW - valueW, 12), textAttrs))
    if (row.value) parts.push(text(x + w - c.padX, lineY, row.value, `font-size="12" text-anchor="end" fill="${figure}"${figureDim}`))
  }
}

function drawBox(box: GraphBox, p: Palette): string {
  switch (box.shape) {
    case 'group': return drawGroup(box, p)
    case 'collapsed-group': return drawCollapsedGroup(box, p)
    case 'internet': return drawInternet(box, p)
    default: return drawResource(box, p)
  }
}

function drawLine(line: GraphLine, markerId: string | null): string {
  return `<path d="${esc(line.path)}" fill="none" stroke="${esc(line.color)}" stroke-width="${line.width}"` +
    `${line.dash ? ` stroke-dasharray="${esc(line.dash)}"` : ''}${markerId ? ` marker-end="url(#${markerId})"` : ''}/>`
}

function drawLineLabel(line: GraphLine, p: Palette): string {
  if (!line.label) return ''
  const s = line.labelStyle ?? {}
  const size = s.fontSize ?? 10
  const width = line.label.length * size * CHAR_RATIO + 8
  const height = size + 6
  const bg = s.background ?? p.surface
  return `<g><rect x="${n(line.labelX - width / 2)}" y="${n(line.labelY - height / 2)}" width="${n(width)}" height="${n(height)}" rx="2" fill="${esc(bg)}" fill-opacity="${s.backgroundOpacity ?? 0.9}"/>` +
    text(line.labelX, line.labelY + size * 0.35, line.label, `font-size="${size}" text-anchor="middle"${s.fontWeight ? ` font-weight="${s.fontWeight}"` : ''} fill="${esc(s.color ?? p.textSecondary)}"`) +
    '</g>'
}

/** Writes a graph as a standalone SVG made of shapes and text only. */
export function writeSvg(model: GraphModel, opts: GraphSvgOptions): string {
  const p = PALETTES[opts.theme]
  // The root viewport clips its content, so a frame cuts groups and lines at its edge.
  const view = opts.frame ?? {
    x: model.bounds.x - PADDING,
    y: model.bounds.y - PADDING,
    width: model.bounds.width + PADDING * 2,
    height: model.bounds.height + PADDING * 2,
  }
  const vx = view.x
  const vy = view.y
  const vw = view.width
  const vh = view.height

  // React Flow's closed arrowhead, sized in stroke widths.
  const markers = new Map<string, string>()
  const markerFor = (line: GraphLine): string | null => {
    if (line.arrow === null) return null
    const key = `${line.color}|${line.arrow}`
    let id = markers.get(key)
    if (!id) {
      id = `a${markers.size}`
      markers.set(key, id)
    }
    return id
  }
  const lines = model.lines.map(line => drawLine(line, markerFor(line)))
  const markerDefs = [...markers].map(([key, id]) => {
    const [color, size] = key.split('|')
    return `<marker id="${id}" viewBox="-10 -10 20 20" markerWidth="${size}" markerHeight="${size}" markerUnits="strokeWidth" orient="auto-start-reverse" refX="0" refY="0">` +
      `<polyline points="-5,-4 0,0 -5,4 -5,-4" fill="${esc(color)}" stroke="${esc(color)}" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"/></marker>`
  })
  const symbols = Object.entries(model.icons).map(([key, markup]) =>
    `<symbol id="${key}" viewBox="0 0 24 24"><g fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${markup}</g></symbol>`)

  const containers = model.boxes.filter(b => b.shape === 'group')
  const others = model.boxes.filter(b => b.shape !== 'group')

  return [
    `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${n(vw)}" height="${n(vh)}" viewBox="${n(vx)} ${n(vy)} ${n(vw)} ${n(vh)}" font-family="${esc(FONT)}">`,
    `<title>${esc(opts.name)}</title>`,
    `<defs>${symbols.join('')}${markerDefs.join('')}</defs>`,
    opts.background ? `<rect x="${n(vx)}" y="${n(vy)}" width="${n(vw)}" height="${n(vh)}" fill="${esc(opts.background)}"/>` : '',
    ...containers.map(b => drawBox(b, p)),
    `<g>${lines.join('')}</g>`,
    ...others.map(b => drawBox(b, p)),
    `<g>${model.lines.map(l => drawLineLabel(l, p)).join('')}</g>`,
    '</svg>',
    '',
  ].filter(Boolean).join('\n')
}
