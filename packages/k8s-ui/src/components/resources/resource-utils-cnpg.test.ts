import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { getCNPGClusterCertificateExpirations } from './resource-utils-cnpg'

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

  it('maps unparseable values to the -1 sentinel (renders as "expired")', () => {
    const resource = {
      status: { certificates: { expirations: { 'mycluster-ca': 'garbage' } } },
    }
    const [cert] = getCNPGClusterCertificateExpirations(resource)
    expect(cert.daysUntilExpiry).toBe(-1)
  })

  it('returns empty list when no expirations are present', () => {
    expect(getCNPGClusterCertificateExpirations({})).toEqual([])
    expect(getCNPGClusterCertificateExpirations({ status: {} })).toEqual([])
  })
})
