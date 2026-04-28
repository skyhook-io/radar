import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  parseCNPGExpiryDate,
  getCNPGClusterCertificateExpirations,
} from './resource-utils-cnpg'

describe('parseCNPGExpiryDate', () => {
  it('parses Go default time format without fractional seconds', () => {
    const d = parseCNPGExpiryDate('2026-07-27 08:27:41 +0000 UTC')
    expect(d.toISOString()).toBe('2026-07-27T08:27:41.000Z')
  })

  it('parses Go default time format with nanoseconds (truncates to ms)', () => {
    const d = parseCNPGExpiryDate('2026-07-27 08:27:41.123456789 +0000 UTC')
    expect(d.toISOString()).toBe('2026-07-27T08:27:41.123Z')
  })

  it('handles non-UTC offsets', () => {
    const d = parseCNPGExpiryDate('2026-07-27 08:27:41 -0700 PDT')
    expect(d.toISOString()).toBe('2026-07-27T15:27:41.000Z')
  })

  it('parses ISO 8601 strings as well', () => {
    const d = parseCNPGExpiryDate('2026-07-27T08:27:41Z')
    expect(d.toISOString()).toBe('2026-07-27T08:27:41.000Z')
  })

  it('returns Invalid Date for unparseable input', () => {
    expect(isNaN(parseCNPGExpiryDate('not a date').getTime())).toBe(true)
  })
})

describe('getCNPGClusterCertificateExpirations', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-04-28T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('regression: future Go-format dates are not flagged as expired (issue #554)', () => {
    const resource = {
      status: {
        certificates: {
          expirations: {
            'mycluster-ca': '2026-07-27 08:27:41 +0000 UTC',
            'mycluster-server': '2026-07-27 08:27:41 +0000 UTC',
            'mycluster-replication': '2026-07-27 08:27:41 +0000 UTC',
          },
        },
      },
    }
    const certs = getCNPGClusterCertificateExpirations(resource)
    expect(certs).toHaveLength(3)
    for (const cert of certs) {
      expect(cert.daysUntilExpiry).toBeGreaterThan(0)
      expect(cert.daysUntilExpiry).toBeLessThanOrEqual(91)
    }
  })

  it('flags genuinely expired certificates as negative', () => {
    const resource = {
      status: {
        certificates: {
          expirations: {
            'mycluster-ca': '2026-04-27 08:27:41 +0000 UTC',
          },
        },
      },
    }
    const [cert] = getCNPGClusterCertificateExpirations(resource)
    expect(cert.daysUntilExpiry).toBeLessThan(0)
  })

  it('returns empty list when no expirations are present', () => {
    expect(getCNPGClusterCertificateExpirations({})).toEqual([])
    expect(getCNPGClusterCertificateExpirations({ status: {} })).toEqual([])
  })
})
