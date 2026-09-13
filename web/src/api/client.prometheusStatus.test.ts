import { describe, expect, it } from 'vitest'
import {
  PROM_STATUS_POLL_DISCOVERING_MS,
  PROM_STATUS_POLL_IDLE_MS,
  prometheusStatusRefetchInterval,
} from './client'

describe('prometheusStatusRefetchInterval', () => {
  it('polls quickly only while discovery is in flight', () => {
    expect(prometheusStatusRefetchInterval(undefined)).toBe(PROM_STATUS_POLL_IDLE_MS)
    expect(prometheusStatusRefetchInterval({ available: false, connected: false })).toBe(PROM_STATUS_POLL_IDLE_MS)
    expect(prometheusStatusRefetchInterval({ available: false, connected: false, discovering: true })).toBe(
      PROM_STATUS_POLL_DISCOVERING_MS,
    )
    expect(prometheusStatusRefetchInterval({ available: true, connected: true, address: 'http://localhost:1' })).toBe(
      PROM_STATUS_POLL_IDLE_MS,
    )
  })
})
