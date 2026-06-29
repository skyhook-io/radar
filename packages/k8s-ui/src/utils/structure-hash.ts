// FNV-1a 32-bit string hash. Cheap, well-distributed, no allocations.
export function fnv1a32(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

// foldHash returns an order-independent fingerprint of the given items as a
// "<xor>.<sum>" string. Used by TopologyGraph's structureKey change-detection
// — at thousands of nodes, sort+join over IDs allocated tens of KB of string
// every render; this is O(n) with two uint32 accumulators.
//
// Two independent commutative folds (XOR and 32-bit additive sum) are combined
// because a single collision in the structure key is a false negative on the
// exact path that serves big, high-churn graphs: the layout effect bails on an
// unchanged key, so a real shape change would silently skip setNodes/setEdges.
// A collision now requires BOTH folds to collide simultaneously across the
// difference set, which is vanishingly unlikely. Combine with element count
// (caller composes "count.xor.sum") for full structural identity.
//
// Order independence is intentional: pure reorders of the same node/edge set
// produce an identical graph and shouldn't trigger an ELK relayout.
export function foldHash<T>(items: ArrayLike<T>, keyOf: (item: T) => string): string {
  let xor = 0
  let sum = 0
  for (let i = 0; i < items.length; i++) {
    const h = fnv1a32(keyOf(items[i]))
    xor ^= h
    sum = (sum + h) >>> 0
  }
  return `${xor >>> 0}.${sum}`
}
