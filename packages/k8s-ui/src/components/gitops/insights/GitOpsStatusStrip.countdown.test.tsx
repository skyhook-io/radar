// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { GitOpsStatusStrip } from './GitOpsInsightViews'

afterEach(() => vi.useRealTimers())

it('counts the next run down while the page stays open, and hides it once due', async () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-10T10:05:00Z'))
  const element = document.createElement('div')
  const root = createRoot(element)
  const insight = { summary: { tool: 'fluxcd', kind: 'Kustomization', namespace: 'flux-system', name: 'checkout', appliedRevision: 'main@sha1:aaaaaaa1234567890', interval: '10m', lastApplied: '2026-11-10T10:00:00Z' } }
  await act(async () => { root.render(<GitOpsStatusStrip insight={insight} />) })
  expect(element.textContent).toContain('next run in ~5m')
  await act(async () => { vi.advanceTimersByTime(3 * 60_000) })
  expect(element.textContent).toContain('next run in ~2m')
  await act(async () => { vi.advanceTimersByTime(3 * 60_000) })
  expect(element.textContent).not.toContain('next run')
  await act(async () => root.unmount())
})
