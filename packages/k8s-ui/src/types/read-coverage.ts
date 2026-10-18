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
 * combination of its present dimensions; an absent dimension is not narrowed
 * (the gap may reach any kind or namespace of the answer), never "none".
 */
export interface ReadGap {
  code: string
  kinds?: { count: number; names?: ReadKindRef[] }
  namespaces?: ReadNameSet
  objects?: { count: number; names?: ReadObjectRef[] }
}

/** Why an implicitly broad read covers less than everything. */
export type ReadNarrowing = 'user_access' | 'namespace_pin' | 'namespace_selection' | 'namespace_collection' | 'kind_collection' | (string & {})

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
