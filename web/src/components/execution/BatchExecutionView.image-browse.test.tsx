import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { BatchExecutionFullscreen } from './BatchExecutionView'

const auth = vi.hoisted(() => ({ enabled: false }))

vi.mock('../../api/client', () => ({
  useResource: () => ({}),
  useWorkloadPods: () => ({}),
  useJobSetResources: () => ({ data: undefined, isLoading: false }),
  useWorkloadRuns: () => ({ isLoading: false, error: undefined, data: { collection: 'runs', runs: [], total: 0, truncated: false } }),
  useImageMetadata: () => ({ data: undefined, isLoading: false, error: undefined }),
  ApiError: class ApiError extends Error {},
}))

vi.mock('../../contexts/CapabilitiesContext', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../contexts/CapabilitiesContext')>()),
  useIsAuthEnabled: () => auth.enabled,
}))

const image = 'trafex/php-nginx:3.6.0'

function render() {
  return renderToStaticMarkup(
    <BatchExecutionFullscreen
      kind="CronJob" apiKind="cronjobs" namespace="legacy" name="nightly-invoice"
      resource={{ spec: { schedule: '0 3 * * *', jobTemplate: { spec: { template: { spec: { containers: [{ name: 'invoice', image }] } } } } }, status: {} }}
    />,
  )
}

describe('template image in the run view', () => {
  it('opens the image browser when auth is off', () => {
    auth.enabled = false
    const html = render()
    expect(html).toMatch(new RegExp(`<button[^>]*>${image}</button>`))
    expect(html).not.toContain('decoration-dotted')
  })

  it('stays text and marks the explanation when auth is on, where a template has no Pod to browse through', () => {
    auth.enabled = true
    const html = render()
    expect(html).not.toMatch(new RegExp(`<button[^>]*>${image}</button>`))
    expect(html).toMatch(new RegExp(`<span class="[^"]*decoration-dotted[^"]*" tabindex="0">${image}<span class="sr-only">. With sign-in on, browse an image&#x27;s files from one of its Pods.</span></span>`))
  })
})
