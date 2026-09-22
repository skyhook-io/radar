import { describe, expect, it } from 'vitest'
import {
  PROM_STATUS_POLL_DISCOVERING_MS,
  PROM_STATUS_POLL_IDLE_MS,
  prometheusStatusRefetchInterval,
} from './client'

describe('prometheusStatusRefetchInterval', () => {
  it('polls quickly while discovery is in flight or has not reported yet', () => {
    expect(prometheusStatusRefetchInterval(undefined)).toBe(PROM_STATUS_POLL_IDLE_MS)
    // Read before the server's run started: transient, keep polling.
    expect(prometheusStatusRefetchInterval({ available: false, connected: false })).toBe(PROM_STATUS_POLL_DISCOVERING_MS)
    // A run that ended without a connection carries its outcome.
    expect(prometheusStatusRefetchInterval({ available: false, connected: false, error: 'no Prometheus service found in cluster' })).toBe(
      PROM_STATUS_POLL_IDLE_MS,
    )
    expect(prometheusStatusRefetchInterval({ available: false, connected: false, discovering: true })).toBe(
      PROM_STATUS_POLL_DISCOVERING_MS,
    )
    expect(prometheusStatusRefetchInterval({ available: true, connected: true, address: 'http://localhost:1' })).toBe(
      PROM_STATUS_POLL_IDLE_MS,
    )
  })
})
