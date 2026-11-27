// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { CertificateRenderer } from './CertificateRenderer'
import { CertificateCell } from './certmanager-cells'
import { KnativeCertificateCell } from './knative-cells'
import { getCertificateExpiry } from '../resource-utils-certmanager'

const windows = { warningSeconds: 180, highSeconds: 60, liveWarningSeconds: 30 }
const now = Date.parse('2026-11-25T12:00:00Z')
const certificate = (remaining: number) => ({
  spec: { secretName: 'web' },
  status: { notAfter: new Date(now + remaining).toISOString(), conditions: [{ type: 'Ready', status: 'True' }] },
})
afterEach(() => vi.useRealTimers())

describe('configured cert-manager expiry', () => {
  it('applies the percentage override to raw certificate screens', () => {
    vi.useFakeTimers().setSystemTime(now)
    const automatic = { warningSeconds: 2592000, highSeconds: 604800, liveWarningSeconds: 86400, automatic: true }
    const data = { ...certificate(16 * 60_000), status: { ...certificate(16 * 60_000).status, notBefore: new Date(now - 44 * 60_000).toISOString() } }
    expect(getCertificateExpiry(data, automatic).level).toBe('degraded')
    const selected = { ...automatic, warningPercent: 20 }
    expect(getCertificateExpiry(data, selected).level).toBe('healthy')
    expect(renderToString(<CertificateCell resource={data} column="expires" certificateExpiryPolicy={selected} />)).toContain('text-green-400')
    expect(renderToString(<KnativeCertificateCell resource={data} column="expires" certificateExpiryPolicy={selected} />)).toContain('status-healthy')
    expect(renderToString(<CertificateRenderer data={data} certificateExpiryPolicy={selected} />)).not.toContain('Certificate expires in')
  })
  it('keeps newly issued one-hour certificates healthy with automatic defaults', () => {
    vi.useFakeTimers().setSystemTime(now)
    const automatic = { warningSeconds: 2592000, highSeconds: 604800, liveWarningSeconds: 86400, automatic: true }
    const data = { ...certificate(3_600_000), status: { ...certificate(3_600_000).status, notBefore: new Date(now).toISOString() } }
    expect(getCertificateExpiry(data, automatic).level).toBe('healthy')
    expect(renderToString(<CertificateCell resource={data} column="expires" certificateExpiryPolicy={automatic} />)).toContain('text-green-400')
    expect(renderToString(<KnativeCertificateCell resource={data} column="expires" certificateExpiryPolicy={automatic} />)).toContain('status-healthy')
    expect(renderToString(<CertificateRenderer data={data} certificateExpiryPolicy={automatic} />)).not.toContain('Certificate expires in')
  })
  it('uses resolved Secret windows for Knative rows without a reported start date', () => {
    vi.useFakeTimers().setSystemTime(now)
    const resolved = { warningSeconds: 1200, highSeconds: 280, liveWarningSeconds: 1200 }
    expect(renderToString(<KnativeCertificateCell resource={certificate(3_600_000)} column="expires" certificateExpiryPolicy={resolved} />)).toContain('status-healthy')
  })
  it('updates through warning, High and expiry while open, then clears on renewal', async () => {
    vi.useFakeTimers().setSystemTime(now)
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    const element = document.createElement('div')
    document.body.append(element)
    const root = createRoot(element)
    try {
      await act(async () => root.render(<CertificateRenderer data={certificate(240_000)} certificateExpiryPolicy={windows} />))
      expect(element.textContent).not.toContain('Certificate expires in')
      await act(async () => vi.advanceTimersByTime(90_000))
      expect(element.querySelector('div.text-amber-700')?.textContent).toContain('Certificate expires in')
      await act(async () => vi.advanceTimersByTime(120_000))
      expect(element.querySelector('div.text-red-700')?.textContent).toContain('Certificate expires in')
      await act(async () => vi.advanceTimersByTime(30_000))
      expect(element.textContent).toContain('Certificate has expired')
      await act(async () => root.render(<CertificateRenderer data={certificate(3_600_000)} certificateExpiryPolicy={windows} />))
      expect(element.textContent).not.toContain('Certificate has expired')
      expect(element.textContent).not.toContain('Certificate expires in')
    } finally {
      await act(async () => root.unmount())
      element.remove()
    }
  })
  it.each([
    [3_600_000, 'healthy', 'text-green-400'],
    [90_000, 'degraded', 'text-yellow-400'],
    [30_000, 'unhealthy', 'text-red-400'],
    [0, 'unhealthy', 'text-red-400'],
  ])('agrees across the list and drawer with %i ms left', (remaining, level, color) => {
    vi.useFakeTimers().setSystemTime(now)
    const data = certificate(remaining)
    expect(getCertificateExpiry(data, windows).level).toBe(level)
    expect(renderToString(<CertificateCell resource={data} column="expires" certificateExpiryPolicy={windows} />)).toContain(color)
    const drawer = renderToString(<CertificateRenderer data={data} certificateExpiryPolicy={windows} />)
    expect(drawer.includes('Certificate expires in')).toBe(remaining > 0 && level !== 'healthy')
    expect(drawer.includes('Certificate has expired')).toBe(remaining === 0)
    expect(drawer).not.toContain('0d remaining')
  })

  it('keeps renewal failures visible outside the expiry window', () => {
    vi.useFakeTimers().setSystemTime(now)
    const data = certificate(3_600_000)
    const drawer = renderToString(<CertificateRenderer data={{ ...data, status: { ...data.status, failedIssuanceAttempts: 2 } }} certificateExpiryPolicy={windows} />)
    expect(drawer).toContain('2 failed issuance attempts')
    expect(drawer).not.toContain('Certificate expires in')
  })

  it('shows unknown rather than a healthy or expired verdict for malformed expiry', () => {
    expect(getCertificateExpiry({ status: { notAfter: 'invalid' } }, windows)).toEqual({ text: '-', level: 'unknown' })
  })
})
