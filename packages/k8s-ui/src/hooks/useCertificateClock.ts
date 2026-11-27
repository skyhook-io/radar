import { useEffect, useState } from 'react'

// Certificates can cross a warning boundary without a Kubernetes update.
export function useCertificateClock(enabled = true): number {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!enabled) return
    const timer = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(timer)
  }, [enabled])
  return now
}
