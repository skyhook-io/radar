import { describe, it, expect } from 'vitest'
import { formatCadence, formatUpdatedAgo, formatLastUpdatedBucket, msToNextBucket } from './format'

describe('formatCadence', () => {
  it('renders sub-minute intervals in seconds', () => {
    expect(formatCadence(30_000)).toBe('30s')
    expect(formatCadence(45_000)).toBe('45s')
  })
  it('renders minute intervals', () => {
    expect(formatCadence(60_000)).toBe('1m')
    expect(formatCadence(120_000)).toBe('2m')
  })
  it('renders hour intervals', () => {
    expect(formatCadence(3_600_000)).toBe('1h')
  })
})

describe('formatUpdatedAgo', () => {
  it('collapses the first minute to "just now"', () => {
    expect(formatUpdatedAgo(0)).toBe('just now')
    expect(formatUpdatedAgo(59_000)).toBe('just now')
  })
  it('renders minutes and hours with an "ago" suffix', () => {
    expect(formatUpdatedAgo(60_000)).toBe('1m ago')
    expect(formatUpdatedAgo(3_600_000)).toBe('1h ago')
  })
  it('collapses anything past a day to "over a day ago"', () => {
    expect(formatUpdatedAgo(25 * 3_600_000)).toBe('over a day ago')
  })
})

describe('formatLastUpdatedBucket', () => {
  it('buckets by the coarsest unit', () => {
    expect(formatLastUpdatedBucket(0)).toBe('just now')
    expect(formatLastUpdatedBucket(60_000)).toBe('1m')
    expect(formatLastUpdatedBucket(3_600_000)).toBe('1h')
    expect(formatLastUpdatedBucket(86_400_000)).toBe('1d')
  })
})

describe('msToNextBucket', () => {
  it('schedules the next re-render exactly on the bucket boundary', () => {
    expect(msToNextBucket(0)).toBe(60_000)
    expect(msToNextBucket(30_000)).toBe(30_000)
    // 90s elapsed → 30s until the "2m" bucket flips.
    expect(msToNextBucket(90_000)).toBe(30_000)
  })
})
