import { midTruncate } from './format'
import type { GraphBox, GraphGroupTone, GraphLine, GraphModel } from './graph-model'

// draw.io's Kubernetes icon set: shape=mxgraph.kubernetes.icon2;prIcon=<name>.
const K8S_ICON_STYLE = 'aspect=fixed;sketch=0;html=1;dashed=0;verticalLabelPosition=bottom;verticalAlign=top;fillColor=#2875E2;strokeColor=#ffffff;points=[[0.005,0.63,0],[0.1,0.2,0],[0.9,0.2,0],[0.5,0,0],[0.995,0.63,0],[0.72,0.99,0],[0.5,1,0],[0.28,0.99,0]];shape=mxgraph.kubernetes.icon2;'
const NETWORK_STYLE = 'html=1;outlineConnect=0;fillColor=#CCCCCC;strokeColor=#6881B3;gradientColor=none;gradientDirection=north;strokeWidth=2;fontColor=#1F2937;verticalLabelPosition=bottom;verticalAlign=top;align=center;'

const K8S_ICONS: Record<string, string> = {
  Pod: 'pod', PodGroup: 'pod', Deployment: 'deploy', Rollout: 'deploy', DaemonSet: 'ds', StatefulSet: 'sts',
  ReplicaSet: 'rs', Job: 'job', CronJob: 'cronjob', JobSet: 'job',
  Service: 'svc', Ingress: 'ing', Gateway: 'ing', Endpoints: 'ep', EndpointSlice: 'ep',
  ConfigMap: 'cm', Secret: 'secret', SealedSecret: 'secret', ServiceAccount: 'sa',
  HorizontalPodAutoscaler: 'hpa', VerticalPodAutoscaler: 'hpa',
  PersistentVolumeClaim: 'pvc', PersistentVolume: 'pv', StorageClass: 'sc',
  Node: 'node', Namespace: 'ns',
  NetworkPolicy: 'netpol', CiliumNetworkPolicy: 'netpol', CiliumClusterwideNetworkPolicy: 'netpol', ClusterNetworkPolicy: 'netpol',
  CalicoNetworkPolicy: 'netpol', CalicoGlobalNetworkPolicy: 'netpol', CalicoStagedNetworkPolicy: 'netpol',
  CalicoStagedGlobalNetworkPolicy: 'netpol', CalicoStagedKubernetesNetworkPolicy: 'netpol',
}
// Custom resources and anything else draw.io has no icon for.
export const DRAWIO_FALLBACK_ICON = 'crd'

const NETWORK_SHAPES: Record<string, string> = {
  External: 'server',
  Host: 'server',
  Unknown: 'hub',
}

const TONES: Record<GraphGroupTone, { fill: string; stroke: string; font: string; dashed?: boolean }> = {
  namespace: { fill: '#F5F8FF', stroke: '#4A6FA5', font: '#1F3A5F' },
  app: { fill: '#F0FAF6', stroke: '#3C8F6E', font: '#1E4D3A' },
  label: { fill: '#FFF8EC', stroke: '#B7791F', font: '#6B4410' },
  addon: { fill: '#F6F0FA', stroke: '#8E5BB5', font: '#4B2A66', dashed: true },
}

const ICON_W = 50
const ICON_H = 48
const CLOUD_W = 90
const CLOUD_H = 50
const GROUP_HEADER = 40
const SMALL_BOX_H = 70
const SMALL_ICON_SCALE = 0.6
// Rough width of one 11px label character, to keep names inside their box.
const CHAR_W = 6.5

function attr(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/\n/g, '&#10;')
}

// Labels use html=1, so user text is escaped once for HTML and once more as an attribute.
function html(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

function htmlLabel(lines: (string | undefined)[]): string {
  return lines.filter((l): l is string => !!l).map(html).join('<br>')
}

// Style values are `key=value;` pairs, so a color must not carry either separator.
function styleValue(value: string): string {
  return value.replace(/[;=]/g, '')
}

function groupTone(box: GraphBox): (typeof TONES)[GraphGroupTone] {
  return TONES[box.tone ?? 'namespace']
}

function vertexStyle(box: GraphBox, hasChildren: boolean): { style: string; x: number; y: number; w: number; h: number; value: string } {
  const fit = (text: string | undefined, width: number) => text ? midTruncate(text, Math.max(6, Math.floor(width / CHAR_W))) : undefined
  if (box.shape === 'group') {
    const t = groupTone(box)
    return {
      style: `swimlane;startSize=${GROUP_HEADER};container=1;collapsible=0;html=1;whiteSpace=wrap;fillColor=${t.fill};swimlaneFillColor=${t.fill};strokeColor=${t.stroke};fontColor=${t.font};fontStyle=1;fontSize=14;align=left;spacingLeft=10;rounded=1;arcSize=2;${t.dashed ? 'dashed=1;' : ''}`,
      x: box.x, y: box.y, w: box.w, h: box.h,
      value: htmlLabel([box.count !== undefined ? `${box.name} (${box.count})` : box.name]),
    }
  }
  if (box.shape === 'collapsed-group') {
    const t = groupTone(box)
    return {
      style: `rounded=1;arcSize=8;whiteSpace=wrap;html=1;fillColor=${t.fill};strokeColor=${t.stroke};strokeWidth=2;fontColor=${t.font};align=left;verticalAlign=top;spacingLeft=10;spacingTop=4;${t.dashed ? 'dashed=1;' : ''}${hasChildren ? 'container=1;collapsible=0;' : ''}`,
      x: box.x, y: box.y, w: box.w, h: box.h,
      value: `<b>${html(box.name)}</b>${box.count !== undefined ? ` (${box.count})` : ''}${box.detail ? `<br>${html(box.detail)}` : ''}`,
    }
  }
  if (box.shape === 'internet') {
    const purple = box.kind === 'AddonInternet' ? 'fillColor=#CDA2BE;' : ''
    return {
      style: `${NETWORK_STYLE}shape=mxgraph.networks.cloud;${purple}`,
      x: box.x + (box.w - CLOUD_W) / 2, y: box.y, w: CLOUD_W, h: CLOUD_H,
      value: htmlLabel([box.name]),
    }
  }
  // A short box (a workload card) gets a smaller icon and no detail line, so its label stays inside it.
  const small = box.h < SMALL_BOX_H
  const scale = small ? SMALL_ICON_SCALE : 1
  const value = htmlLabel([fit(box.name, box.w), fit(box.kindLabel, box.w), small ? undefined : fit(box.detail, box.w)])
  const x = box.x + (box.w - ICON_W * scale) / 2
  const network = box.category === 'database' && NETWORK_SHAPES[box.kind] ? 'storage' : NETWORK_SHAPES[box.kind]
  if (network) {
    return { style: `${NETWORK_STYLE}shape=mxgraph.networks.${network};`, x, y: box.y, w: ICON_W * scale, h: ICON_W * scale, value }
  }
  return {
    style: `${K8S_ICON_STYLE}prIcon=${K8S_ICONS[box.kind] ?? DRAWIO_FALLBACK_ICON};fontSize=${small ? 10 : 11};`,
    x, y: box.y, w: ICON_W * scale, h: ICON_H * scale, value,
  }
}

function edgeStyle(line: GraphLine): string {
  const arrow = line.arrow === null ? 'endArrow=none;' : 'endArrow=block;endFill=1;'
  return `edgeStyle=orthogonalEdgeStyle;rounded=1;html=1;strokeColor=${styleValue(line.color)};strokeWidth=${line.width};${line.dash ? 'dashed=1;' : ''}${arrow}fontSize=10;labelBackgroundColor=#FFFFFF;`
}

/** Writes a graph as an uncompressed draw.io (.drawio) file. */
export function writeDrawio(model: GraphModel, opts: { name: string }): string {
  const ids = new Map<string, string>()
  model.boxes.forEach((box, i) => ids.set(box.id, `n${i + 2}`))
  const byId = new Map(model.boxes.map(b => [b.id, b]))
  const parents = new Set(model.boxes.map(b => b.parent).filter((p): p is string => !!p))
  const vertex = (box: GraphBox) => vertexStyle(box, parents.has(box.id))

  const ancestors = (id: string): string[] => {
    const chain: string[] = []
    for (let p = byId.get(id)?.parent; p && byId.has(p); p = byId.get(p)?.parent) chain.push(p)
    return chain
  }
  const commonAncestor = (a: string, b: string): string | undefined => {
    const fromA = new Set(ancestors(a))
    return ancestors(b).find(p => fromA.has(p))
  }
  // A child sits in its parent's coordinate space, below the swimlane header.
  const origin = (id: string | undefined): { x: number; y: number } => {
    const box = id ? byId.get(id) : undefined
    if (!box) return { x: 0, y: 0 }
    const v = vertex(box)
    return { x: v.x, y: v.y }
  }

  const cells: string[] = []
  for (const box of model.boxes) {
    const v = vertex(box)
    const parent = box.parent && byId.has(box.parent) ? box.parent : undefined
    const o = origin(parent)
    cells.push(
      `<mxCell id="${ids.get(box.id)}" value="${attr(v.value)}" style="${attr(v.style)}" vertex="1" parent="${parent ? ids.get(parent) : '1'}">` +
      `<mxGeometry x="${round(v.x - o.x)}" y="${round(v.y - o.y)}" width="${round(v.w)}" height="${round(v.h)}" as="geometry"/></mxCell>`,
    )
  }
  model.lines.forEach((line, i) => {
    const source = ids.get(line.from)
    const target = ids.get(line.to)
    if (!source || !target) return
    const parent = commonAncestor(line.from, line.to)
    // Corners of the on-screen route, so draw.io keeps it instead of routing its own.
    const o = origin(parent)
    const points = line.bends.length
      ? `<Array as="points">${line.bends.map(b => `<mxPoint x="${round(b.x - o.x)}" y="${round(b.y - o.y)}"/>`).join('')}</Array>`
      : ''
    cells.push(
      `<mxCell id="e${i}" value="${attr(htmlLabel([line.label]))}" style="${attr(edgeStyle(line))}" edge="1" parent="${parent ? ids.get(parent) : '1'}" source="${source}" target="${target}">` +
      `<mxGeometry relative="1" as="geometry">${points}</mxGeometry></mxCell>`,
    )
  })

  return `<?xml version="1.0" encoding="UTF-8"?>
<mxfile host="app.diagrams.net" agent="Radar">
<diagram id="radar" name="${attr(opts.name)}"><mxGraphModel grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="0" pageScale="1" math="0" shadow="0"><root>
<mxCell id="0"/><mxCell id="1" parent="0"/>
${cells.join('\n')}
</root></mxGraphModel></diagram>
</mxfile>
`
}

function round(n: number): number {
  return Math.round(n * 10) / 10
}
