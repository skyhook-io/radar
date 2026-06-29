import { describe, it, expect } from 'vitest'
import { fnv1a32, foldHash } from './structure-hash'

describe('fnv1a32', () => {
  it('is deterministic', () => {
    expect(fnv1a32('hello')).toBe(fnv1a32('hello'))
  })

  it('distinguishes similar strings', () => {
    expect(fnv1a32('pod/default/a')).not.toBe(fnv1a32('pod/default/b'))
    expect(fnv1a32('a')).not.toBe(fnv1a32('A'))
  })

  it('handles empty string', () => {
    expect(typeof fnv1a32('')).toBe('number')
  })
})

describe('foldHash', () => {
  const id = (s: string) => s

  it('is order-independent', () => {
    const a = foldHash(['a', 'b', 'c'], id)
    const b = foldHash(['c', 'a', 'b'], id)
    expect(a).toBe(b)
  })

  it('changes when an element is added', () => {
    const before = foldHash(['a', 'b'], id)
    const after = foldHash(['a', 'b', 'c'], id)
    expect(before).not.toBe(after)
  })

  it('changes when an element is removed', () => {
    const before = foldHash(['a', 'b', 'c'], id)
    const after = foldHash(['a', 'b'], id)
    expect(before).not.toBe(after)
  })

  it('changes when an element is renamed', () => {
    const before = foldHash(['a', 'b', 'c'], id)
    const after = foldHash(['a', 'b', 'd'], id)
    expect(before).not.toBe(after)
  })

  it('returns the zero fingerprint for empty input', () => {
    expect(foldHash([], id)).toBe('0.0')
  })

  it('works with object items via keyOf', () => {
    const items = [{ id: 'x' }, { id: 'y' }]
    expect(foldHash(items, i => i.id)).toBe(foldHash(['x', 'y'], id))
  })

  it('emits a "<xor>.<sum>" shape', () => {
    expect(foldHash(['a', 'b'], id)).toMatch(/^\d+\.\d+$/)
  })

  // The dual accumulator's whole point: a swap that preserves the XOR fold
  // (a^b stays constant) must still change the fingerprint via the sum fold.
  // Single-XOR would have collided here. Construct two sets with equal XOR
  // but different members.
  it('distinguishes sets that share an XOR but differ in membership', () => {
    // {x} vs {y, z} where hash(x) === hash(y) ^ hash(z) would collide under
    // pure XOR. We can't easily force that, so instead verify the additive
    // fold breaks a known XOR-preserving transform: doubling an element.
    // ['a','a'] XOR-folds to 0 (a^a), same as [] — but sum differs.
    expect(foldHash(['a', 'a'], id)).not.toBe(foldHash([], id))
    expect(foldHash(['a', 'a'], id)).not.toBe(foldHash(['b', 'b'], id))
  })
})

// The production guard against skipped relayouts is the *composed* key
// (count + foldHash), exactly as TopologyGraph builds it. These tests pin that
// composition, not just the bare fold — a genuine same-count add/remove/rename
// must change the composed key so the layout effect doesn't short-circuit.
describe('composed structure key (count + foldHash)', () => {
  const id = (s: string) => s
  // Mirrors TopologyGraph's structureKey shape for the node portion.
  const composed = (nodeIds: string[]) => `n${nodeIds.length}:${foldHash(nodeIds, id)}`

  it('changes on a same-count rename', () => {
    expect(composed(['a', 'b', 'c'])).not.toBe(composed(['a', 'b', 'x']))
  })

  it('changes on add and on remove', () => {
    expect(composed(['a', 'b'])).not.toBe(composed(['a', 'b', 'c']))
    expect(composed(['a', 'b', 'c'])).not.toBe(composed(['a', 'b']))
  })

  it('is stable across reorder (no wasted relayout)', () => {
    expect(composed(['a', 'b', 'c'])).toBe(composed(['c', 'b', 'a']))
  })
})
