import { describe, expect, it } from 'vitest'
import { formatCPUString, formatMemoryString, parseMemoryToBytes, parseQuantityToNumber } from './format'

describe('formatCPUString', () => {
  it('renders exact zero as zero, not a fabricated near-zero', () => {
    expect(formatCPUString('0')).toBe('0 cores')
  })

  it('preserves the sign of negative quantities (over-limit headroom)', () => {
    expect(formatCPUString('-2')).toBe('-2 cores')
    expect(formatCPUString('-500m')).toBe('-0.50 cores')
  })

  it('keeps sub-centicore positives as an honest bound', () => {
    expect(formatCPUString('5m')).toBe('<0.01 cores')
  })
})

describe('formatMemoryString', () => {
  it('preserves the sign of negative quantities', () => {
    expect(formatMemoryString('-4Gi')).toBe('-4.00 GiB')
    expect(formatMemoryString('-256Mi')).toBe('-256 MiB')
  })

  it('renders zero as zero', () => {
    expect(formatMemoryString('0')).toBe('0 B')
  })
})

describe('parseMemoryToBytes', () => {
  it('parses signed quantities instead of silently zeroing them', () => {
    expect(parseMemoryToBytes('-1Ki')).toBe(-1024)
    expect(parseMemoryToBytes('1Ki')).toBe(1024)
  })
})

describe('parseQuantityToNumber', () => {
  // The reported bug. A Quantity's canonical form collapses trailing zeros into
  // an SI suffix, so a node started with --max-pods=1000 reports "1k". Read as
  // a raw integer that is 1, and a node running 216 pods rendered as 216 / 1
  // with the usage bar pegged at 21600%.
  it('reads a pod capacity of 1000 as 1000, not 1', () => {
    expect(parseQuantityToNumber('1k')).toBe(1000)
    expect((216 / parseQuantityToNumber('1k')) * 100).toBeCloseTo(21.6)
  })

  it('only collapses zeros in groups of three, so nearby counts are unchanged', () => {
    expect(parseQuantityToNumber('2k')).toBe(2000)
    expect(parseQuantityToNumber('1020')).toBe(1020)
    expect(parseQuantityToNumber('990')).toBe(990)
    expect(parseQuantityToNumber('216')).toBe(216)
  })

  it('handles the sub-unit decimal suffixes a count can legally carry', () => {
    expect(parseQuantityToNumber('3000m')).toBe(3)
    expect(parseQuantityToNumber('1500m')).toBe(1.5)
    expect(parseQuantityToNumber('1000000u')).toBe(1)
    expect(parseQuantityToNumber('1000000000n')).toBe(1)
  })

  it('handles the whole decimal SI range', () => {
    expect(parseQuantityToNumber('5M')).toBe(5e6)
    expect(parseQuantityToNumber('5G')).toBe(5e9)
    expect(parseQuantityToNumber('5T')).toBe(5e12)
    expect(parseQuantityToNumber('5P')).toBe(5e15)
    expect(parseQuantityToNumber('5E')).toBe(5e18)
  })

  it('handles binary suffixes in powers of 1024', () => {
    expect(parseQuantityToNumber('1Ki')).toBe(1024)
    expect(parseQuantityToNumber('2Gi')).toBe(2 * 1024 ** 3)
    expect(parseQuantityToNumber('1Ei')).toBe(1024 ** 6)
  })

  // "1E3" is the exponent form (1000), while "1E" is exa. Deciding the exponent
  // first is what keeps the two apart.
  it('reads the decimal-exponent form without confusing it for exa', () => {
    expect(parseQuantityToNumber('1e3')).toBe(1000)
    expect(parseQuantityToNumber('1E3')).toBe(1000)
    expect(parseQuantityToNumber('1.5e3')).toBe(1500)
    expect(parseQuantityToNumber('1e-3')).toBe(0.001)
    expect(parseQuantityToNumber('1E')).toBe(1e18)
  })

  it('preserves the sign', () => {
    expect(parseQuantityToNumber('-1k')).toBe(-1000)
    expect(parseQuantityToNumber('+1k')).toBe(1000)
  })

  // A number this cannot read is reported as unknown so the caller can fall back,
  // rather than becoming a plausible-looking wrong number on screen.
  it('yields zero for input it cannot parse rather than guessing', () => {
    expect(parseQuantityToNumber('1z')).toBe(0)
    expect(parseQuantityToNumber('1KB')).toBe(0)
    expect(parseQuantityToNumber('abc')).toBe(0)
    expect(parseQuantityToNumber('')).toBe(0)
    expect(parseQuantityToNumber('   ')).toBe(0)
    expect(parseQuantityToNumber(null)).toBe(0)
    expect(parseQuantityToNumber(undefined)).toBe(0)
  })

  it('passes through numbers the API already decoded, and rejects non-finite ones', () => {
    expect(parseQuantityToNumber(216)).toBe(216)
    expect(parseQuantityToNumber(0)).toBe(0)
    expect(parseQuantityToNumber(Number.NaN)).toBe(0)
    expect(parseQuantityToNumber(Number.POSITIVE_INFINITY)).toBe(0)
  })
})
