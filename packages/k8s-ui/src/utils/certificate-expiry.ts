import type { CertificateExpiryWindows } from '../types'

export function certificateExpiryHealth(remaining: number, windows?: CertificateExpiryWindows): 'healthy' | 'degraded' | 'unhealthy' | 'unknown' {
  if (!Number.isFinite(remaining)) return 'unknown'
  const warning = (windows?.warningSeconds ?? 30 * 24 * 60 * 60) * 1000
  const high = (windows?.highSeconds ?? 7 * 24 * 60 * 60) * 1000
  if (remaining <= 0 || remaining < high) return 'unhealthy'
  if (remaining < warning) return 'degraded'
  return 'healthy'
}

export function resolveCertificateExpiryWindows(windows: CertificateExpiryWindows | undefined, notBefore: string | undefined, notAfter: string | undefined): CertificateExpiryWindows | undefined {
  if (!windows?.automatic) return windows
  const lifetime = Date.parse(notAfter ?? '') - Date.parse(notBefore ?? '')
  if (!Number.isFinite(lifetime) || lifetime <= 0) return windows
  const warningSeconds = Math.min(30 * 24 * 60 * 60, windows.warningPercent ? lifetime * windows.warningPercent / 100_000 : lifetime / 3000)
  return { warningSeconds, highSeconds: warningSeconds * 7 / 30, liveWarningSeconds: Math.min(24 * 60 * 60, warningSeconds) }
}
