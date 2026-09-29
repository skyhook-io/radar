import { useSyncExternalStore } from 'react'
import { supersedeFor, type CNPGTrackedOperation } from './model'

// Tracked operations for this browser session: they survive tab changes and
// reloads of the page, and end with the session. Storage can be unavailable
// (private windows, blocked site data); the tracker then lives in memory.

const STORAGE_KEY = 'radar.cnpg.operations.v1'
const MAX_OPS = 50
// Finished operations stay visible this long, then drop out on their own.
const KEEP_FINISHED_MS = 30 * 60_000

let ops: CNPGTrackedOperation[] = load()
const listeners = new Set<() => void>()

function load(): CNPGTrackedOperation[] {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

function save() {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(ops))
  } catch {
    // Storage unavailable: keep the in-memory copy.
  }
}

function emit(next: CNPGTrackedOperation[]) {
  const now = Date.now()
  ops = next.filter((o) => !o.finishedAt || now - o.finishedAt < KEEP_FINISHED_MS).slice(-MAX_OPS)
  save()
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

export interface TrackCNPGOperationInput {
  kind: string
  label: string
  context: string
  namespace: string
  cluster: string
  clusterUID?: string
  target?: { name: string; uid?: string }
  baseline?: Record<string, unknown>
  link?: CNPGTrackedOperation['link']
}

/**
 * Start following an operation the server accepted. Any unfinished operation
 * the new one makes moot is marked superseded.
 */
export function trackCNPGOperation(input: TrackCNPGOperationInput): string {
  const now = Date.now()
  const op: CNPGTrackedOperation = {
    id: `${now.toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
    kind: input.kind,
    label: input.label,
    context: input.context,
    namespace: input.namespace,
    cluster: input.cluster,
    clusterUID: input.clusterUID,
    target: input.target,
    startedAt: now,
    baseline: input.baseline ?? {},
    state: 'requested',
    lastProgressAt: now,
    link: input.link,
  }
  emit([...supersedeFor(ops, op), op])
  return op.id
}

export function updateCNPGOperations(update: (all: CNPGTrackedOperation[]) => CNPGTrackedOperation[]) {
  const next = update(ops)
  if (next !== ops) emit(next)
}

export function dismissCNPGOperation(id: string) {
  emit(ops.filter((o) => o.id !== id))
}

export function useCNPGOperations(filter?: { context?: string; namespace: string; cluster: string }): CNPGTrackedOperation[] {
  const all = useSyncExternalStore(subscribe, () => ops, () => ops)
  if (!filter) return all
  return all.filter((o) => o.namespace === filter.namespace && o.cluster === filter.cluster && (!filter.context || o.context === filter.context))
}

export function resetCNPGOperationsForTest() {
  ops = []
  listeners.clear()
}
