// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { FluxValuesReference } from '@skyhook-io/k8s-ui/components/resources/renderers/FluxHelmReleaseRenderer'
import { FluxHelmReleaseRenderer } from './FluxHelmReleaseRenderer'

const query = vi.hoisted(() => ({
  data: undefined as 'present' | 'missing' | 'unchecked' | undefined,
  error: null as Error & { status?: number } | null,
  isLoading: false,
  isFetching: false,
  refetch: vi.fn(),
}))
const lookup = vi.hoisted(() => vi.fn())
vi.mock('../../../api/client', () => ({
  useResourceAvailability: (...args: unknown[]) => { lookup(...args); return query },
  isForbiddenError: (error: unknown) => error instanceof Error && 'status' in error && error.status === 403,
}))

let container: HTMLDivElement
let root: Root
const navigate = vi.fn()

beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  Object.assign(query, { data: undefined, error: null, isLoading: false, isFetching: false })
  vi.clearAllMocks()
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
})

async function render(reference: FluxValuesReference = { name: 'overrides', optional: true }) {
  await act(async () => root.render(<FluxHelmReleaseRenderer
    data={{ metadata: { namespace: 'apps' }, spec: { valuesFrom: [reference] } }}
    onNavigate={navigate}
  />))
}

function referenceButton() {
  return [...container.querySelectorAll('button')].find(button => button.textContent === 'overrides')
}

describe('Flux Helm values references', () => {
  it.each(['ConfigMap', 'Secret'] as const)('labels an absent optional %s and does not link it', async kind => {
    query.data = 'missing'
    await render({ name: 'overrides', kind, optional: true })
    expect(container.textContent).toContain('Optional')
    expect(container.textContent).toContain('Not found - skipped by Flux')
    expect(referenceButton()).toBeUndefined()
    expect(lookup).toHaveBeenCalledWith(kind === 'Secret' ? 'secrets' : 'configmaps', 'apps', 'overrides')
  })

  it('marks an absent required reference as required', async () => {
    query.data = 'missing'
    await render({ name: 'overrides' })
    expect(container.textContent).toContain('Not found - required by Flux')
    expect(container.textContent).not.toContain('Optional')
    expect(referenceButton()).toBeUndefined()
  })

  it.each([403, 404, 500])('does not mistake HTTP %s for an absent optional reference', async status => {
    query.error = Object.assign(new Error('read failed'), { status })
    await render()
    expect(container.textContent).toContain(status === 403 ? 'Access denied' : 'Could not check availability')
    expect(container.textContent).not.toContain('skipped by Flux')
    expect(referenceButton()).toBeUndefined()
  })

  it('waits for availability before offering navigation', async () => {
    query.isLoading = true
    await render()
    expect(container.textContent).toContain('Checking availability')
    expect(referenceButton()).toBeUndefined()
  })

  it('rechecks a missing reference and links it once available', async () => {
    query.data = 'missing'
    await render()
    const retry = container.querySelector<HTMLButtonElement>('[aria-label="Check overrides again"]')
    expect(retry).not.toBeNull()
    await act(async () => retry?.click())
    expect(query.refetch).toHaveBeenCalledOnce()
    query.error = null
    query.data = 'present'
    await render()
    expect(container.textContent).not.toContain('Not found')
    await act(async () => referenceButton()?.click())
    expect(navigate).toHaveBeenCalledWith({ kind: 'configmaps', namespace: 'apps', name: 'overrides', group: undefined })
  })

  it('does not trust stale successful data after a failed refresh', async () => {
    query.data = 'present'
    query.error = Object.assign(new Error('forbidden'), { status: 403 })
    await render()
    expect(container.textContent).toContain('Access denied')
    expect(referenceButton()).toBeUndefined()
  })

  it('keeps navigation when the connected Radar cannot check availability', async () => {
    query.data = 'unchecked'
    await render()
    expect(container.textContent).toContain('Availability not checked')
    expect(referenceButton()).toBeDefined()
    expect(container.textContent).not.toContain('Not found')
  })
})
