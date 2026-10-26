// Coverage of a Radar read, as Radar's pkg/resourceapi serializes it. Codes
// come from Radar's pkg/readreason.

/**
 * A known, finite set of `count` members. `names` lists all of them, sorted,
 * when there are few enough for the response; otherwise it is absent and which
 * members are in the set is unknown.
 */
export interface ReadNameSet {
  count: number
  names?: string[]
}

/**
 * How far a gap reaches along one axis when it can't list it: `all` of the
 * answer's scope, or an `unknown` part of it. A value a client doesn't know
 * reads as unknown.
 */
export type ReadExtent = 'all' | 'unknown' | (string & {})

/** A gap's set along one axis: listed like a ReadNameSet, or an extent. */
export type ReadGapSet<T> = { count: number; names?: T[]; extent?: undefined } | { extent: ReadExtent; count?: undefined; names?: undefined }

/** A kind; an omitted group is the core group. */
export interface ReadKindRef {
  kind: string
  group?: string
}

export interface ReadObjectRef {
  kind: string
  group?: string
  namespace?: string
  name: string
}

/**
 * Part of an answer Radar did not read, and why. A gap covers every
 * combination of its axes. An axis that applies is always present, listed or
 * as an extent; an absent axis doesn't apply to the gap (namespaces on a
 * cluster-scoped kind). Within one answer a namespace is in at most one gap.
 * `action` is what the code calls for, so a code the client doesn't know still
 * says what resolves it.
 */
export interface ReadGap {
  code: string
  action?: string
  kinds?: ReadGapSet<ReadKindRef>
  namespaces?: ReadGapSet<string>
  objects?: { count: number; names?: ReadObjectRef[] }
}

/** Why an implicitly broad read covers less than everything. */
export type ReadNarrowing = 'user_access' | 'namespace_pin' | 'namespace_collection' | 'kind_collection' | (string & {})

/**
 * A search's coverage: `partial` when something the query named couldn't be
 * searched or a read failed; `narrowedBy` when a broad query searched only
 * what the caller's access and Radar's collection allow.
 */
export interface SearchCoverage {
  status: 'complete' | 'partial'
  narrowedBy?: ReadNarrowing[]
  gaps: ReadGap[]
}
