import { describe, it, expect } from 'vitest'
import { isHelmReleaseActionable } from './badge-colors'

// Pin the membership set so adding a new Helm status becomes a
// deliberate decision here.

describe('isHelmReleaseActionable', () => {
  it('returns true for `failed`', () => {
    expect(isHelmReleaseActionable('failed')).toBe(true)
  })

  it('is case-insensitive (matches Helm SDK serialisation variants)', () => {
    expect(isHelmReleaseActionable('FAILED')).toBe(true)
    expect(isHelmReleaseActionable('Failed')).toBe(true)
  })

  it('returns FALSE for the pending-* in-flight statuses', () => {
    // These are Helm's NORMAL in-flight states during every
    // routine install/upgrade/rollback. If we treated them as
    // actionable, every routine install would briefly attach an
    // alarming chevron + tooltip to its own row. Until we have
    // release age available client-side to distinguish
    // "transient" from "stuck > N min", we give up the
    // stuck-controller signpost rather than alarm the common case.
    expect(isHelmReleaseActionable('pending-install')).toBe(false)
    expect(isHelmReleaseActionable('pending-upgrade')).toBe(false)
    expect(isHelmReleaseActionable('pending-rollback')).toBe(false)
  })

  it('returns false for the success / normal statuses', () => {
    expect(isHelmReleaseActionable('deployed')).toBe(false)
    expect(isHelmReleaseActionable('superseded')).toBe(false)
    expect(isHelmReleaseActionable('uninstalled')).toBe(false)
  })

  it('returns false for `uninstalling` (in-progress, not stuck)', () => {
    expect(isHelmReleaseActionable('uninstalling')).toBe(false)
  })

  it('returns false for null / undefined / empty', () => {
    expect(isHelmReleaseActionable(null)).toBe(false)
    expect(isHelmReleaseActionable(undefined)).toBe(false)
    expect(isHelmReleaseActionable('')).toBe(false)
  })

  it('returns false for unknown strings (defensive)', () => {
    expect(isHelmReleaseActionable('mystery-status')).toBe(false)
    expect(isHelmReleaseActionable('error')).toBe(false) // Helm uses 'failed', not 'error'
  })
})
