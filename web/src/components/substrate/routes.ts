import type { RadarFeatureSupport } from '../../api/radarFeatures'
import type { SubstratePoolRef, SubstrateRef } from '../../api/substrateWorkspace'

export const SUBSTRATE_GROUP = 'ate.dev'

/** The Resources sidebar category Substrate's kinds are listed under (api-resources.ts). */
export const SUBSTRATE_CATEGORY = 'Agent Substrate'

export const SUBSTRATE_PATH = '/substrate'

/** The namespace picker's tooltip on every Substrate screen: actors live in atespaces, which are not namespaces. */
export const SUBSTRATE_NAMESPACE_FILTER_TOOLTIP = 'Agent Substrate keeps actors in atespaces, not namespaces — the namespace filter doesn’t apply here. Use the Atespace filter instead.'

export const SUBSTRATE_WORKERPOOL_KIND = { name: 'workerpools', kind: 'WorkerPool', group: SUBSTRATE_GROUP }
export const SUBSTRATE_POOLS_PATH = `/resources/workerpools?apiGroup=${SUBSTRATE_GROUP}`

export type SubstrateScreen = 'overview' | 'actors' | 'templates' | 'workers'

export const SUBSTRATE_SCREENS: Record<SubstrateScreen, { label: string; title: string; path: string }> = {
  overview: { label: 'Overview', title: 'Agent Substrate overview', path: SUBSTRATE_PATH },
  actors: { label: 'Actors', title: 'Agent Substrate actors', path: `${SUBSTRATE_PATH}/actors` },
  templates: { label: 'Templates', title: 'Agent Substrate templates', path: `${SUBSTRATE_PATH}/templates` },
  workers: { label: 'Workers', title: 'Agent Substrate workers', path: `${SUBSTRATE_PATH}/workers` },
}

export interface SubstrateRoute {
  screen: SubstrateScreen
  /** An actor's page: /substrate/actors/<atespace>/<name>. */
  actor?: SubstrateRef
  /** A template's page: /substrate/templates/<atespace>/<name>. */
  template?: SubstrateRef
  /** A WorkerPool's page: /substrate/workerpools/<namespace>/<name>. */
  pool?: SubstratePoolRef
}

function segments(pathname: string): string[] {
  return pathname.replace(/^\/+/, '').replace(/\/+$/, '').split('/').map((s) => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  })
}

export function parseSubstrateRoute(pathname: string): SubstrateRoute {
  const seg = segments(pathname)
  if (seg[0] !== 'substrate') return { screen: 'overview' }
  const [, screen, a, b] = seg
  if (screen === 'actors') return a && b ? { screen: 'actors', actor: { atespace: a, name: b } } : { screen: 'actors' }
  if (screen === 'templates') return a && b ? { screen: 'templates', template: { atespace: a, name: b } } : { screen: 'templates' }
  if (screen === 'workers') return { screen: 'workers' }
  if (screen === 'workerpools' && a && b) return { screen: 'overview', pool: { namespace: a, name: b } }
  return { screen: 'overview' }
}

function withParams(path: string, params: Record<string, string | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v) qs.set(k, v)
  const s = qs.toString()
  return s ? `${path}?${s}` : path
}

const enc = encodeURIComponent

/** An actor's page; `ctx` pins the Kubernetes context it was read through. */
export function substrateActorPath(ref: SubstrateRef, ctx?: string, tab?: string): string {
  return withParams(`${SUBSTRATE_PATH}/actors/${enc(ref.atespace)}/${enc(ref.name)}`, { ctx, tab })
}

export function substrateTemplatePath(ref: SubstrateRef, ctx?: string): string {
  return withParams(`${SUBSTRATE_PATH}/templates/${enc(ref.atespace)}/${enc(ref.name)}`, { ctx })
}

export function substratePoolPath(ref: SubstratePoolRef, ctx?: string, tab?: string): string {
  return withParams(`${SUBSTRATE_PATH}/workerpools/${enc(ref.namespace)}/${enc(ref.name)}`, { ctx, tab })
}

/** The Actors screen narrowed by the /actors query parameters (state, atespace, template, pool, worker, q). */
export function substrateActorsPath(filter: Record<string, string | undefined> = {}): string {
  return withParams(SUBSTRATE_SCREENS.actors.path, filter)
}

/**
 * What the WorkerPool kind's Resources route shows: Radar's pool table when
 * this Radar serves the Substrate views; a loader while capabilities load for
 * the first time; otherwise the generic table.
 */
export function substrateKindListMode(support: RadarFeatureSupport, capabilitiesPending: boolean): 'view' | 'wait' | 'table' {
  if (support === 'supported') return 'view'
  return support === 'unknown' && capabilitiesPending ? 'wait' : 'table'
}

/** The drawer's search parameter for an actor: `<atespace>/<name>`. Actors are not Kubernetes objects, so they have their own drawer. */
export const ACTOR_DRAWER_PARAM = 'actor'

export function encodeActorParam(ref: SubstrateRef): string {
  return `${ref.atespace}/${ref.name}`
}

export function decodeActorParam(value: string | null): SubstrateRef | null {
  if (!value) return null
  const i = value.indexOf('/')
  if (i <= 0 || i === value.length - 1) return null
  return { atespace: value.slice(0, i), name: value.slice(i + 1) }
}
