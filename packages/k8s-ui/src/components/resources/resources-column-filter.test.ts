import { describe, it, expect } from 'vitest'
import { isColumnFilterableByDistinctCount, SKIP_FILTER_COLUMNS } from './ResourcesView'

describe('isColumnFilterableByDistinctCount', () => {
  // Caps were tuned after a customer-visible regression where the namespace
  // filter silently vanished on clusters with >20 namespaces. Pin them so a
  // future "tidy up" of the ternary doesn't re-introduce the bug.

  describe('namespace (uncapped — scales with cluster, not kind enum)', () => {
    it('is filterable at 2 distinct values', () => {
      expect(isColumnFilterableByDistinctCount('namespace', 2)).toBe(true)
    })

    it('is filterable above the old 20-value cap', () => {
      expect(isColumnFilterableByDistinctCount('namespace', 25)).toBe(true)
    })

    it('is filterable at very large clusters (5000 namespaces)', () => {
      expect(isColumnFilterableByDistinctCount('namespace', 5000)).toBe(true)
    })

    it('is not filterable with a single distinct value (nothing to filter)', () => {
      expect(isColumnFilterableByDistinctCount('namespace', 1)).toBe(false)
    })
  })

  describe('node (cap = 200 — large but bounded)', () => {
    it('is filterable below the cap', () => {
      expect(isColumnFilterableByDistinctCount('node', 150)).toBe(true)
    })

    it('is filterable exactly at the cap', () => {
      expect(isColumnFilterableByDistinctCount('node', 200)).toBe(true)
    })

    it('is not filterable just above the cap', () => {
      expect(isColumnFilterableByDistinctCount('node', 201)).toBe(false)
    })

    it('is not filterable far above the cap', () => {
      expect(isColumnFilterableByDistinctCount('node', 500)).toBe(false)
    })
  })

  describe('generic column (cap = 30 — enum-style)', () => {
    it('is filterable at 2 distinct values', () => {
      expect(isColumnFilterableByDistinctCount('status', 2)).toBe(true)
    })

    it('is filterable exactly at the cap', () => {
      expect(isColumnFilterableByDistinctCount('status', 30)).toBe(true)
    })

    it('is not filterable just above the cap', () => {
      expect(isColumnFilterableByDistinctCount('status', 31)).toBe(false)
    })
  })

  describe('SKIP_FILTER_COLUMNS denylist', () => {
    it('skips columns in the denylist regardless of distinct count', () => {
      expect(isColumnFilterableByDistinctCount('name', 5)).toBe(false)
      expect(isColumnFilterableByDistinctCount('age', 5)).toBe(false)
      expect(isColumnFilterableByDistinctCount('message', 5)).toBe(false)
    })

    // Removed from the denylist deliberately — events benefit from filtering
    // by Reason (FailedScheduling, BackOff, Killing, …). Re-adding would
    // silently kill the Reason dropdown on the Events table.
    it('does not skip `reason` (the Events Reason filter)', () => {
      expect(SKIP_FILTER_COLUMNS.has('reason')).toBe(false)
      expect(isColumnFilterableByDistinctCount('reason', 8)).toBe(true)
    })
  })
})
