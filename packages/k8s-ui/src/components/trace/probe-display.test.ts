import { describe, it, expect } from 'vitest'
import { collapseSkipRows } from './probe-display'
import type { ProbeResult } from './types'

const skip = (overrides: Partial<ProbeResult>): ProbeResult => ({
  layer: 'http',
  target: 'port 53',
  vantage: 'local',
  ok: false,
  skipped: true,
  reason: 'Port named "dns" looks non-HTTP.',
  ...overrides,
})

const ok = (overrides: Partial<ProbeResult>): ProbeResult => ({
  layer: 'http',
  target: 'port 80',
  vantage: 'local',
  ok: true,
  ...overrides,
})

describe('collapseSkipRows', () => {
  it('returns empty for empty input', () => {
    expect(collapseSkipRows([])).toEqual([])
  })

  it('passes non-skipped rows through unchanged', () => {
    const rows = [ok({ target: '10.0.0.1:80' }), ok({ target: '10.0.0.2:80' })]
    expect(collapseSkipRows(rows)).toEqual(rows)
  })

  it('collapses identical-reason skip rows into one with count', () => {
    const rows = [skip({ target: 'pod-a port 53' }), skip({ target: 'pod-b port 53' }), skip({ target: 'pod-c port 53' })]
    const out = collapseSkipRows(rows)
    expect(out).toHaveLength(1)
    expect(out[0].target).toBe('3 probes skipped')
    expect(out[0].skipped).toBe(true)
  })

  it('keeps single skip rows untouched', () => {
    const rows = [skip({ target: 'pod-a port 53' })]
    const out = collapseSkipRows(rows)
    expect(out).toHaveLength(1)
    expect(out[0].target).toBe('pod-a port 53')
  })

  it('keeps skip rows with different reasons separate', () => {
    const rows = [
      skip({ reason: 'Port named "dns" looks non-HTTP.' }),
      skip({ reason: 'Port named "dns-tcp" looks non-HTTP.' }),
    ]
    expect(collapseSkipRows(rows)).toHaveLength(2)
  })

  it('keeps skip rows with different paths separate', () => {
    const rows = [
      skip({ path: 'apiserver', target: 'pod-a port 53' }),
      skip({ path: 'data', target: 'pod-a port 53' }),
    ]
    expect(collapseSkipRows(rows)).toHaveLength(2)
  })

  it('preserves order: collapsed row stays at the position of the first occurrence', () => {
    const rows = [
      ok({ target: '10.0.0.1:80' }),
      skip({ target: 'pod-a port 53' }),
      ok({ target: '10.0.0.2:80' }),
      skip({ target: 'pod-b port 53' }),
    ]
    const out = collapseSkipRows(rows)
    expect(out).toHaveLength(3)
    expect(out[0].target).toBe('10.0.0.1:80')
    expect(out[1].target).toBe('2 probes skipped')
    expect(out[2].target).toBe('10.0.0.2:80')
  })

  it('keeps skip rows with the same reason but different commands separate', () => {
    // Per-pod port-forward commands differ even when the reason is identical;
    // collapsing would surface only the first pod's command for all of them.
    const rows = [
      skip({ target: 'pod-a port 6379', command: 'kubectl port-forward pod/pod-a -n ns 6379:6379' }),
      skip({ target: 'pod-b port 6379', command: 'kubectl port-forward pod/pod-b -n ns 6379:6379' }),
    ]
    expect(collapseSkipRows(rows)).toHaveLength(2)
  })

  it('collapses skip rows that share reason AND command', () => {
    const rows = [
      skip({ target: 'a', command: 'kubectl auth can-i get services/proxy -n ns' }),
      skip({ target: 'b', command: 'kubectl auth can-i get services/proxy -n ns' }),
    ]
    const out = collapseSkipRows(rows)
    expect(out).toHaveLength(1)
    expect(out[0].target).toBe('2 probes skipped')
    expect(out[0].command).toBe('kubectl auth can-i get services/proxy -n ns')
  })

  it('does not dedupe a skip row with no reason (defensive)', () => {
    const rows = [
      skip({ target: 'pod-a port 53', reason: undefined }),
      skip({ target: 'pod-b port 53', reason: undefined }),
    ]
    const out = collapseSkipRows(rows)
    expect(out).toHaveLength(2)
  })
})
