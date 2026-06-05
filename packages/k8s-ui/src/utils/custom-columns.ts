// User-defined table columns sourcing a metadata.labels[path] or
// metadata.annotations[path] value. Persisted per-kind in localStorage and
// materialized (in ResourcesView) into a self-contained ExtraColumn so they
// ride the existing render/sort/filter override rails.

export type CustomColumnSource = 'label' | 'annotation'

export interface CustomColumnDef {
  source: CustomColumnSource
  path: string
}

// Stable identity used as the React key, ExtraColumn.key, visibility-set
// membership, and dedupe key. Built-in column keys never use a `:` prefix, so
// `label:`/`annotation:` can't collide with them. Build-only — never parsed
// back into source+path, so a colon inside `path` is harmless.
export function customColumnKey(d: CustomColumnDef): string {
  return `${d.source}:${d.path}`
}

export function readCustomColumnValue(resource: any, d: CustomColumnDef): string {
  const bag = d.source === 'label' ? resource?.metadata?.labels : resource?.metadata?.annotations
  const v = bag?.[d.path]
  return typeof v === 'string' ? v : v == null ? '' : String(v)
}

// Drop anything that isn't a well-formed def — guards the localStorage load
// boundary where a corrupted/legacy/hand-edited blob could otherwise be a
// non-array (crashing a later .map) or carry empty/invalid entries (dead
// columns). TypeScript's guarantees stop at JSON.parse, so this is the one
// place the (source, non-empty path) invariant must be enforced at runtime.
export function sanitizeCustomColumnDefs(raw: unknown): CustomColumnDef[] {
  if (!Array.isArray(raw)) return []
  return raw.filter((c): c is CustomColumnDef =>
    !!c &&
    (c.source === 'label' || c.source === 'annotation') &&
    typeof c.path === 'string' &&
    c.path.trim() !== ''
  )
}
