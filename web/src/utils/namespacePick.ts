import type { NamespaceScope } from '../api/client'
import { createTabStorage } from './tabStorage'

// The namespace pick is view state owned by each tab. The server's remembered
// pick only seeds a tab that has none, so another tab or another person
// sharing the Kubernetes identity can't change what an open tab shows, on
// reload included.
//
// The tab keeps one pick per Radar address, tagged with the kubeconfig context
// it was made in. After a context switch the tab starts again from the
// server's pick for the new context.
//
// Under --namespace-scope the pick is the process-wide cache scope and stays
// server-owned. A server that doesn't report its context predates per-tab
// picks and keeps the shared behavior, since an older Radar may still apply
// its stored pick to reads that name no namespace.

const storage = createTabStorage()
// Picks whose sessionStorage write didn't stick (storage full). They win over
// the stored slot for the life of the page, so an accepted pick isn't undone
// by the next fetch; a reload then falls back to the stored one.
const unsaved = new Map<string, string>()

function slotKey(apiBase: string): string {
  return `radar:namespace-pick:${apiBase}`
}

export function readTabPick(apiBase: string, context: string): string[] | undefined {
  const key = slotKey(apiBase)
  const raw = unsaved.get(key) ?? storage.get(key)
  if (raw === undefined) return undefined
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || !('context' in parsed) || !('namespaces' in parsed)) return undefined
    const { context: pickedIn, namespaces } = parsed
    if (pickedIn !== context || !Array.isArray(namespaces)) return undefined
    if (namespaces.every((ns): ns is string => typeof ns === 'string')) return namespaces
  } catch { /* unreadable slot: treat as absent */ }
  return undefined
}

export function writeTabPick(apiBase: string, context: string, namespaces: string[]): void {
  const key = slotKey(apiBase)
  const value = JSON.stringify({ context, namespaces })
  storage.set(key, value)
  if (storage.get(key) === value) unsaved.delete(key)
  else unsaved.set(key, value)
}

/** Same cleaning the server applies to a submitted pick. */
export function cleanPick(namespaces: string[]): string[] {
  return [...new Set(namespaces.filter(ns => ns !== ''))]
}

export function ownsTabPick(scope: Pick<NamespaceScope, 'cacheScoped' | 'context'>): scope is NamespaceScope & { context: string } {
  return !scope.cacheScoped && !!scope.context
}

export function withActives(scope: NamespaceScope, actives: string[]): NamespaceScope {
  const mode = actives.length > 0 ? 'namespace' : scope.authoritative ? 'cluster-wide' : 'restricted'
  return { ...scope, actives, mode }
}

/**
 * Drops picked namespaces missing from the scope's list where the server drops
 * its own pick: when it says so (`accessChecked`), or on a complete non-empty
 * list. After a failed lookup, or with no list at all, the pick stays.
 */
export function pruneTabPick(scope: NamespaceScope, pick: string[]): string[] {
  const proves = scope.accessChecked || (scope.authoritative && scope.accessibleNamespaces.length > 0)
  if (!proves) return pick
  const accessible = new Set(scope.accessibleNamespaces)
  return pick.filter(ns => accessible.has(ns))
}

/**
 * Returns the server scope as this tab sees it: its own pick when it has one
 * for this context, else the server's pick, which becomes the tab's.
 */
export function applyTabPick(scope: NamespaceScope, apiBase: string): NamespaceScope {
  if (!ownsTabPick(scope)) return scope
  const stored = readTabPick(apiBase, scope.context)
  const pick = pruneTabPick(scope, stored ?? scope.actives)
  if (stored === undefined || pick.length !== stored.length) {
    writeTabPick(apiBase, scope.context, pick)
  }
  return withActives(scope, pick)
}
