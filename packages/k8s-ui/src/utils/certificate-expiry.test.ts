import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { certificateExpiryHealth, resolveCertificateExpiryWindows } from './certificate-expiry'

interface Vector {
  name: string
  warningSeconds: number
  highSeconds: number
  remainingNanoseconds: number | null
  health: string
}

const vectors: Vector[] = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../pkg/health/testdata/certificate_expiry.json', import.meta.url)), 'utf8'))

describe('certificate expiry shared with Go', () => {
  it.each(vectors)('$name', v => {
    const windows = v.warningSeconds ? { warningSeconds: v.warningSeconds, highSeconds: v.highSeconds, liveWarningSeconds: 86400 } : undefined
    expect(certificateExpiryHealth(v.remainingNanoseconds === null ? NaN : v.remainingNanoseconds / 1_000_000, windows)).toBe(v.health)
  })
})

interface AutomaticVector {
  name: string
  lifetimeSeconds: number | null
  warningPercent?: number
  warningSeconds: number
  highSeconds: number
  remainingSeconds: number
  health: string
}
const automaticVectors: AutomaticVector[] = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../pkg/health/testdata/certificate_expiry_automatic.json', import.meta.url)), 'utf8'))
describe('automatic certificate expiry shared with Go', () => {
  it.each(automaticVectors)('$name', v => {
    const notAfter = '2026-11-27T12:00:00Z'
    const notBefore = v.lifetimeSeconds === null ? undefined : new Date(Date.parse(notAfter) - v.lifetimeSeconds * 1000).toISOString()
    const warning = 2592000
    const windows = resolveCertificateExpiryWindows({ warningSeconds: warning, highSeconds: warning * 7 / 30, liveWarningSeconds: Math.min(warning, 86400), automatic: true, warningPercent: v.warningPercent }, notBefore, notAfter)
    expect(windows?.warningSeconds).toBe(v.warningSeconds)
    expect(windows?.highSeconds).toBe(v.highSeconds)
    expect(certificateExpiryHealth(v.remainingSeconds * 1000, windows)).toBe(v.health)
  })
  it('does not apply a lifetime ratio twice to resolved agent windows', () => {
    const resolved = { warningSeconds: 1200, highSeconds: 280, liveWarningSeconds: 1200 }
    expect(resolveCertificateExpiryWindows(resolved, '2026-11-27T11:00:00Z', '2026-11-27T12:00:00Z')).toEqual(resolved)
  })
})
