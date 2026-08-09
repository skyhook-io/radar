import { describe, expect, it } from 'vitest'
import { formatCPUString, formatMemoryString, parseMemoryToBytes } from './format'

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
