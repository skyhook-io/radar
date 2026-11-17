// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { guardRadarFeature, type RadarFeatureSupport } from '../../api/radarFeatures'
import type { VeleroRunContentsResponse, VeleroRunLogResponse } from '../../api/velero'

const state = vi.hoisted(() => ({
  support: 'supported' as RadarFeatureSupport,
  fetch: vi.fn(),
}))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useRadarFeature: () => ({
    gatedKey: [state.support],
    guard: <T,>(request: () => Promise<T>) => guardRadarFeature('veleroRunArtifacts', state.support, { currentVersion: 'v1.16.0' }, request),
  }),
  fetchJSON: (path: string, init: RequestInit) => state.fetch(path, init),
}))

import { ApiError } from '../../api/client'
import { VeleroRunContents, VeleroRunLog } from './VeleroRunArtifacts'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let host: HTMLDivElement
let root: Root
let client: QueryClient
const props = { kind: 'restores' as const, namespace: 'velero', name: 'recovery' }
const contents = (patch: Partial<VeleroRunContentsResponse> = {}): VeleroRunContentsResponse => ({
  groups: [{ kind: 'v1/ConfigMap', count: 6001, matched: 6001, items: [{ namespace: 'app', name: 'first', action: 'created' }] }],
  total: 6002, matched: 6002, namespaces: ['app', 'empty-app'], actions: { created: 6001, failed: 1 }, truncated: true, ...patch,
})
const log = (patch: Partial<VeleroRunLogResponse> = {}): VeleroRunLogResponse => ({
  lines: [{ number: 42, level: 'warning', text: 'level=warning msg="storage retry"' }],
  counts: { error: 0, warning: 1, info: 9999, debug: 7, other: 2 }, matched: 1, truncated: false, partial: false, decodedBytes: 4096, ...patch,
})
async function mount(node: ReactNode) {
  await act(async () => { root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>) })
}
async function settle(test: () => boolean) {
  for (let i = 0; i < 50 && !test(); i++) await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)) })
  expect(test()).toBe(true)
}
function button(label: string) {
  return [...host.querySelectorAll('button')].find((b) => b.textContent?.startsWith(label))!
}
async function search(label: string, text: string) {
  const input = host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => { input.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
}
function lastParams() { return new URL(state.fetch.mock.calls.at(-1)![0], 'http://localhost').searchParams }

beforeEach(() => {
  state.support = 'supported'
  state.fetch.mockReset()
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  host.remove()
})

for (const [label, Component] of [['contents', VeleroRunContents], ['log', VeleroRunLog]] as const) {
  describe(label, () => {
    it('loads only when mounted, using a POST', async () => {
      state.fetch.mockImplementation(() => new Promise(() => {}))
      expect(state.fetch).not.toHaveBeenCalled()
      await mount(<Component {...props} />)
      expect(host.textContent).toContain(`Loading ${label}…`)
      expect(state.fetch.mock.calls[0][0]).toContain(`/velero/restores/velero/recovery/${label}?`)
      expect(state.fetch.mock.calls[0][1].method).toBe('POST')
    })

    it('fetches again when the user reopens the tab', async () => {
      state.fetch.mockResolvedValue(label === 'contents' ? contents() : log())
      await mount(<Component {...props} />)
      await settle(() => state.fetch.mock.calls.length === 1 && !host.textContent!.includes('Loading'))
      await mount(null)
      await mount(<Component {...props} />)
      await settle(() => state.fetch.mock.calls.length === 2)
    })

    it.each([['Velero has no '+label+' for this run', 404], ['no access to this Velero Restore', 403], ['the object store refused it', 503]])('shows the server answer: %s', async (message, status) => {
      state.fetch.mockRejectedValue(new ApiError(message, status))
      await mount(<Component {...props} />)
      await settle(() => host.textContent!.includes(message))
      expect(host.textContent).toContain('Retry')
      expect(host.textContent).not.toContain('No matching')
      expect(state.fetch).toHaveBeenCalledTimes(1)
    })

    it('gates an older Radar without posting', async () => {
      state.support = 'unsupported'
      await mount(<Component {...props} />)
      await settle(() => host.textContent!.includes('needs a newer Radar'))
      expect(state.fetch).not.toHaveBeenCalled()
    })
  })
}

describe('Contents rows and filters', () => {
  it('shows groups, whole-file counts, empty namespaces and truncation without live links', async () => {
    state.fetch.mockResolvedValue(contents())
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes('6,000') || host.textContent!.includes('6,002') || host.textContent!.includes('6002 items'))
    expect(host.textContent).toContain('v1/ConfigMap')
    expect(host.textContent).toContain('Counts and filters cover the whole file')
    expect(host.querySelector('option[value="empty-app"]')).not.toBeNull()
    expect(host.querySelector('a')).toBeNull()
    expect([...host.querySelectorAll('[role="tablist"] button')].map((el) => el.textContent)).toEqual(['All actions6002', 'Failed1', 'Created6001', 'Updated0', 'Skipped0'])
  })

  it('posts action, namespace and text filters and displays a late failed result', async () => {
    state.fetch.mockResolvedValue(contents())
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes('first'))
    state.fetch.mockResolvedValue(contents({ matched: 1, truncated: false, groups: [{ kind: 'v1/ConfigMap', count: 6001, matched: 1, items: [{ namespace: 'app', name: 'late-failure', action: 'failed' }] }] }))
    await act(async () => button('Failed').click())
    await settle(() => host.textContent!.includes('late-failure'))
    expect(lastParams().get('action')).toBe('failed')
    await search('Filter contents', 'late-failure')
    await settle(() => lastParams().get('q') === 'late-failure' && !!host.querySelector('option[value="app"]'))
    const select = host.querySelector('select')!
    await act(async () => { select.value = 'app'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    await settle(() => lastParams().get('namespace') === 'app')
    expect(lastParams().get('action')).toBe('failed')
    expect(lastParams().get('q')).toBe('late-failure')
  })

  it('reads failed entries of a restore that settled without errors as with warnings, in their own segment', async () => {
    state.fetch.mockResolvedValue(contents({
      total: 9, matched: 9, truncated: false, actions: { warned: 6, created: 3 },
      groups: [{ kind: 'v1/ConfigMap', count: 9, matched: 9, items: [{ namespace: 'demo-copy', name: 'settings', action: 'warned' }, { namespace: 'demo-copy', name: 'fresh', action: 'created' }] }],
    }))
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes('settings'))
    const tabs = [...host.querySelectorAll('[role="tablist"] button')].map((el) => el.textContent)
    expect(tabs).toEqual(['All actions9', 'Failed0', 'With warnings6', 'Created3', 'Updated0', 'Skipped0'])
    const row = [...host.querySelectorAll('tr')].find((tr) => tr.textContent!.includes('settings'))!
    expect(row.textContent).toContain('With warnings')
    expect(row.textContent).not.toContain('Failed')
    expect(host.textContent).toContain('finished with no errors')
    expect(host.textContent).toContain('the restore’s warning count is Velero’s messages, and the Log counts log lines')
  })

  it('keeps Velero’s failed entries and explains its vocabulary otherwise', async () => {
    state.fetch.mockResolvedValue(contents({
      total: 2, matched: 2, truncated: false, actions: { failed: 1, created: 1 },
      groups: [{ kind: 'v1/ConfigMap', count: 2, matched: 2, items: [{ namespace: 'demo-copy', name: 'settings', action: 'failed' }, { namespace: 'demo-copy', name: 'fresh', action: 'created' }] }],
    }))
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes('settings'))
    const tabs = [...host.querySelectorAll('[role="tablist"] button')].map((el) => el.textContent)
    expect(tabs).toEqual(['All actions2', 'Failed1', 'Created1', 'Updated0', 'Skipped0'])
    expect(host.textContent).toContain('Velero records an item as failed when any warning or error was reported for it.')
    expect(host.textContent).not.toContain('finished with no errors')
  })

  it('counts in the singular when there is one', async () => {
    state.fetch.mockResolvedValue(contents({ total: 1, matched: 1, namespaces: ['app'], truncated: false, groups: [{ kind: 'v1/ConfigMap', count: 1, matched: 1, items: [{ namespace: 'app', name: 'first', action: 'created' }] }] }))
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes('1 item · 1 namespace · 1 matching item'))
    expect(host.textContent).not.toContain('1 namespaces')
  })

  it.each([true, false])('distinguishes an empty file from an empty filter (empty file: %s)', async (empty) => {
    state.fetch.mockResolvedValue(contents({ total: empty ? 0 : 6002, matched: 0, groups: [], truncated: false }))
    await mount(<VeleroRunContents {...props} />)
    await settle(() => host.textContent!.includes(empty ? 'Velero recorded no items' : 'No contents match these filters'))
  })
})

describe('Log rows and filters', () => {
  it('opens on errors and warnings, with counts from all levels', async () => {
    state.fetch.mockResolvedValue(log())
    await mount(<VeleroRunLog {...props} />)
    await settle(() => host.textContent!.includes('storage retry'))
    expect(lastParams().get('levels')).toBe('error,warning')
    expect(button('Info').textContent).toBe('Info9999')
  })

  it('opens a run with no errors or warnings on every level', async () => {
    const clean = log({ counts: { error: 0, warning: 0, info: 3, debug: 0, other: 0 }, lines: [], matched: 0 })
    state.fetch.mockResolvedValueOnce(clean)
    state.fetch.mockResolvedValueOnce({ ...clean, lines: [{ number: 1, level: 'info', text: 'level=info msg="backup completed"' }], matched: 3 })
    await mount(<VeleroRunLog {...props} />)
    await settle(() => host.textContent!.includes('backup completed'))
    expect(lastParams().get('levels')).toBe('error,warning,info,debug,other')
    expect(button('Show all levels').getAttribute('aria-pressed')).toBe('true')
    state.fetch.mockResolvedValue(clean)
    await act(async () => button('Errors + warnings').click())
    await settle(() => lastParams().get('levels') === 'error,warning' && host.textContent!.includes('No matching log lines'))
    expect(state.fetch).toHaveBeenCalledTimes(3)
  })

  it('posts level and text filters and wraps on demand', async () => {
    state.fetch.mockResolvedValue(log())
    await mount(<VeleroRunLog {...props} />)
    await settle(() => host.textContent!.includes('storage retry'))
    await act(async () => button('Debug').click())
    await settle(() => lastParams().get('levels') === 'debug')
    await act(async () => button('Show all levels').click())
    await settle(() => lastParams().get('levels') === 'error,warning,info,debug,other')
    await search('Search log', 'retry')
    await settle(() => lastParams().get('q') === 'retry')
    const checkbox = host.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    expect(checkbox.checked).toBe(true)
    await act(async () => checkbox.click())
    expect(checkbox.checked).toBe(false)
    expect(host.querySelector('td div.whitespace-pre')).not.toBeNull()
  })

  it('names the decoded prefix separately from the matching-row cap', async () => {
    state.fetch.mockResolvedValue(log({ partial: true, truncated: true, decodedBytes: 64 << 20, limit: '64 MiB decoded limit' }))
    await mount(<VeleroRunLog {...props} />)
    await settle(() => host.textContent!.includes('Counts and search cover the first 64.00 MiB'))
    expect(host.textContent).toContain('first 5,000 matching lines')
    expect(host.textContent).toContain('the decoded prefix')
    expect(button('Error').textContent).toBe('Errors + warnings')
    expect([...host.querySelectorAll('[role="tab"]')].some((el) => el.textContent === 'Error≥0')).toBe(true)
  })

  it.each([true, false])('gives an honest empty state (partial: %s)', async (partial) => {
    state.fetch.mockResolvedValue(log({ lines: [], matched: 0, partial, limit: partial ? '64 KiB line limit' : undefined }))
    await mount(<VeleroRunLog {...props} />)
    await settle(() => host.textContent!.includes('No matching log lines'))
    expect(host.textContent).toContain(partial ? 'in the decoded prefix' : 'Try another level or search')
  })
})
