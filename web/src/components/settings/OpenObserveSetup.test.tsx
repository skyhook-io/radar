// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { OpenObserveConnection, OpenObserveState } from '../../api/openobserveSetup'
import { MetricsBackendSwitch } from './MetricsBackendSwitch'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type Reply = { status?: number; body: unknown }
interface Call { method: string; path: string; body?: Record<string, unknown> }

let container: HTMLDivElement
let root: Root
let calls: Call[]
let replies: Record<string, Reply[]>

const connection = (changes: Partial<OpenObserveConnection> = {}): OpenObserveConnection => ({
  baseUrl: 'https://oo.example.com',
  org: 'default',
  user: 'radar@example.com',
  tokenSource: 'saved',
  clusters: [{ context: 'kind-oo-b', by: 'id' }],
  ...changes,
})
const unmatched: OpenObserveState = { connection: null, cluster: { uid: 'uid-123' } }
const matched = (changes: Partial<OpenObserveConnection> = {}): OpenObserveState => ({
  connection: connection(changes),
  cluster: { uid: 'uid-123', match: { label: 'k8s_cluster_uid', value: 'uid-123', by: 'id' } },
})

const reply = (key: string, ...items: Reply[]) => {
  replies[key] = [...(replies[key] ?? []), ...items]
}

beforeEach(() => {
  calls = []
  replies = {}
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const path = url.replace(/^.*\/api/, '')
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const queue = replies[`${method} ${path}`]
    const next = queue && queue.length > 1 ? queue.shift()! : queue?.[0]
    if (!next) throw new Error(`unexpected ${method} ${path}`)
    return new Response(JSON.stringify(next.body), { status: next.status ?? 200 })
  }))
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

const flush = () => act(async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)) })

async function mount(state: OpenObserveState, props: { locked?: boolean; onChanged?: () => void; onDirtyChange?: (dirty: boolean) => void } = {}) {
  reply('GET /integrations/openobserve', { body: state })
  act(() => root.render(
    <MetricsBackendSwitch
      locked={props.locked}
      onChanged={props.onChanged ?? vi.fn()}
      onDirtyChange={props.onDirtyChange}
      status={<span>Status block</span>}
      prometheus={({ status }) => <div data-testid="prometheus-form">Prometheus form {status}</div>}
    />
  ))
  await flush()
}

const radio = (name: string) =>
  Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find((b) => b.textContent?.includes(name))!
const button = (text: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('button')).find((b) => b.textContent?.trim() === text && !b.closest('.hidden'))
const field = (label: string) => {
  const element = Array.from(container.querySelectorAll('label')).find((l) => l.textContent === label)!
  return document.getElementById(element.htmlFor) as HTMLInputElement
}
const type = (input: HTMLInputElement, value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
})
const click = async (element: HTMLElement | undefined) => {
  expect(element).toBeTruthy()
  act(() => element!.click())
  await flush()
}
const visibleText = () => Array.from(container.children).map((el) => (el as HTMLElement).innerText ?? el.textContent).join(' ')
const posts = () => calls.filter((c) => c.method === 'POST')

describe('Metrics backend choice', () => {
  it('starts on OpenObserve when this cluster has a match, else on Prometheus-compatible', async () => {
    await mount(matched())
    expect(radio('OpenObserve').getAttribute('aria-checked')).toBe('true')
    expect(radio('Prometheus-compatible').getAttribute('aria-checked')).toBe('false')
    expect(container.textContent).toContain('Prometheus, VictoriaMetrics, Thanos or Grafana Mimir.')
    expect(container.textContent).toContain('Read this cluster’s metrics from OpenObserve, filtered to this cluster.')
    act(() => root.unmount())
    root = createRoot(container)
    replies = {}
    await mount(unmatched)
    expect(radio('Prometheus-compatible').getAttribute('aria-checked')).toBe('true')
    expect(container.querySelector('[data-testid="prometheus-form"]')?.closest('.hidden')).toBeNull()
  })

  it('switches views without saving, and releases a matched cluster from the Prometheus view without a confirmation', async () => {
    const onChanged = vi.fn()
    await mount(matched(), { onChanged })
    expect(button('Use Prometheus for this cluster')).toBeUndefined()
    await click(radio('Prometheus-compatible'))
    expect(calls.filter((c) => c.method !== 'GET')).toHaveLength(0)
    expect(container.textContent).toContain('This cluster still reads from OpenObserve.')
    reply('DELETE /integrations/openobserve/cluster?clusterUid=uid-123', { body: { connection: connection(), cluster: { uid: 'uid-123' } } })
    await click(button('Use Prometheus for this cluster'))
    expect(calls.at(-1)).toMatchObject({ method: 'DELETE', path: '/integrations/openobserve/cluster?clusterUid=uid-123' })
    expect(onChanged).toHaveBeenCalled()
    expect(radio('Prometheus-compatible').getAttribute('aria-checked')).toBe('true')
    expect(container.textContent).not.toContain('This cluster still reads from OpenObserve.')
  })

  it('falls back to Prometheus on a failed load, then follows the cluster match once Retry succeeds', async () => {
    replies['GET /integrations/openobserve'] = [{ status: 500, body: { error: 'Radar is restarting' } }, { body: matched() }]
    act(() => root.render(
      <MetricsBackendSwitch onChanged={vi.fn()} status={<span>Status block</span>}
        prometheus={() => <div data-testid="prometheus-form">Prometheus form</div>} />
    ))
    await flush()
    expect(container.textContent).toContain('Radar is restarting')
    expect(radio('Prometheus-compatible').getAttribute('aria-checked')).toBe('true')
    await click(Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find((b) => b.textContent === 'Retry'))
    expect(radio('OpenObserve').getAttribute('aria-checked')).toBe('true')
    expect(container.textContent).toContain('Matched by cluster ID (k8s_cluster_uid=uid-123)')
  })

  it('shows the choice and status read-only when locked', async () => {
    await mount(matched({ clusters: [{ context: 'a', by: 'id' }, { context: 'b', by: 'name' }] }), { locked: true })
    expect(container.querySelectorAll('input, textarea, select')).toHaveLength(0)
    const buttons = Array.from(container.querySelectorAll('button'))
    expect(buttons.length).toBeGreaterThan(0)
    expect(buttons.every((b) => b.disabled)).toBe(true)
    expect(container.textContent).toContain('Status block')
    expect(container.textContent).toContain('Matched by cluster ID (k8s_cluster_uid=uid-123)')
  })
})

describe('OpenObserve setup', () => {
  it('tests and saves a first connection, then shows the match and notes', async () => {
    const onChanged = vi.fn()
    const onDirtyChange = vi.fn()
    await mount(unmatched, { onChanged, onDirtyChange })
    await click(radio('OpenObserve'))
    expect(container.textContent).toContain('Saved in ~/.radar/config.json, readable only by you.')
    const link = Array.from(container.querySelectorAll('a')).find((a) => a.textContent?.includes('Create a service account in OpenObserve'))!
    expect(link.target).toBe('_blank')
    expect(link.rel).toContain('noopener')
    expect(field('Organization').value).toBe('')
    expect(field('Organization').placeholder).toBe('2Zp8kQ1vXcR7mN4tLwYb9HsA3eD')
    type(field('Organization'), 'team')
    type(field('Address'), 'https://oo.example.com')
    type(field('User or service account email'), 'radar@example.com')
    type(field('Token'), 'secret')
    expect(field('Token').type).toBe('password')
    expect(onDirtyChange).toHaveBeenLastCalledWith(true)
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', clusterUid: 'uid-123', notes: { collector: true, missing: ['cpu', 'hpa'] } } })
    replies['GET /integrations/openobserve'] = [{ body: matched() }]
    await click(button('Save changes'))
    expect(posts()[0].body).toEqual({
      baseUrl: 'https://oo.example.com', org: 'team', user: 'radar@example.com', token: 'secret', caCertFile: '', clusterLabel: '', clusterUid: 'uid-123',
    })
    expect(onChanged).toHaveBeenCalled()
    expect(container.textContent).toContain('Data comes from the OpenObserve collector.')
    // The status block carries coverage once the match settles; not repeated here.
    expect(container.textContent).not.toContain('No data in OpenObserve for:')
    expect(container.textContent).toContain('Matched by cluster ID (k8s_cluster_uid=uid-123)')
    expect(onDirtyChange).toHaveBeenLastCalledWith(false)
  })

  it('asks to confirm a name match and resends the same fields with the confirmation', async () => {
    await mount(unmatched)
    await click(radio('OpenObserve'))
    type(field('Address'), 'https://oo.example.com')
    type(field('User or service account email'), 'radar@example.com')
    type(field('Token'), 'secret')
    reply('POST /integrations/openobserve/setup',
      { body: { outcome: 'confirm', reason: 'Found only by name.', suggestion: { label: 'k8s_cluster', value: 'prod' }, clusterUid: 'uid-123' } },
      { body: { outcome: 'saved', clusterUid: 'uid-123', notes: {} } })
    replies['GET /integrations/openobserve'] = [{ body: matched() }]
    await click(button('Save changes'))
    expect(container.textContent).toContain('Found only by name.')
    await click(button("Confirm 'prod' and save"))
    expect(posts()).toHaveLength(2)
    expect(posts()[1].body).toEqual({ ...posts()[0].body, confirm: { label: 'k8s_cluster', value: 'prod' } })
  })

  it('explains a cluster that was not found and saves nothing', async () => {
    const onChanged = vi.fn()
    await mount(unmatched, { onChanged })
    await click(radio('OpenObserve'))
    type(field('Address'), 'https://oo.example.com')
    type(field('User or service account email'), 'radar@example.com')
    type(field('Token'), 'secret')
    reply('POST /integrations/openobserve/setup', { body: {
      outcome: 'not_found', reason: 'No metrics for this cluster.', clusterUid: 'uid-123',
      clusterNames: [{ label: 'k8s_cluster', value: 'staging' }, { label: 'k8s_cluster', value: 'dev' }],
    } })
    await click(button('Save changes'))
    const text = container.textContent!
    expect(text).toContain('No metrics for this cluster.')
    expect(text).toContain('OpenObserve has metrics for: k8s_cluster=staging, k8s_cluster=dev.')
    expect(text).toContain('If OpenObserve names clusters with another label, set it under Advanced.')
    expect(text).toContain('radar_cluster_uid=uid-123')
    expect(text).not.toContain('Matched by')
    expect(button('Save changes')).toBeTruthy()
    expect(field('Token').value).toBe('secret')
    expect(onChanged).not.toHaveBeenCalled()
    expect(calls.map((c) => c.method)).toEqual(['GET', 'POST'])
  })

  it('does not claim OpenObserve is empty when the reason says what it found', async () => {
    await mount(unmatched)
    await click(radio('OpenObserve'))
    type(field('Address'), 'https://oo.example.com')
    type(field('User or service account email'), 'radar@example.com')
    type(field('Token'), 'secret')
    reply('POST /integrations/openobserve/setup', { body: {
      outcome: 'not_found', clusterUid: 'uid-123', clusterNames: [],
      reason: '3 sampled Pods from namespace demo are in OpenObserve, but carry none of the cluster labels cluster.',
    } })
    await click(button('Save changes'))
    const text = container.textContent!
    expect(text).toContain('carry none of the cluster labels')
    expect(text).not.toContain('OpenObserve has no cluster metrics yet.')
    expect(text).toContain('radar_cluster_uid=uid-123')
  })

  it('says OpenObserve has no cluster metrics when there is no reason', async () => {
    await mount(unmatched)
    await click(radio('OpenObserve'))
    type(field('Address'), 'https://oo.example.com')
    type(field('User or service account email'), 'radar@example.com')
    type(field('Token'), 'secret')
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'not_found', clusterUid: 'uid-123', clusterNames: [] } })
    await click(button('Save changes'))
    expect(container.textContent).toContain('OpenObserve has no cluster metrics yet.')
  })

  it('shows an HTTP error and keeps the fields', async () => {
    await mount(unmatched)
    await click(radio('OpenObserve'))
    type(field('Address'), 'https://oo.example.com')
    reply('POST /integrations/openobserve/setup', { status: 400, body: { error: 'Login failed: 401 Unauthorized' } })
    await click(button('Save changes'))
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('Login failed: 401 Unauthorized')
    expect(field('Address').value).toBe('https://oo.example.com')
  })

  it('notes a connection shared by several clusters', async () => {
    await mount(matched({ clusters: [{ context: 'kind-oo-b', by: 'id' }, { context: 'kind-oo-c', by: 'name' }] }))
    expect(visibleText()).toContain('Shared by 2 clusters. Changes apply to all of them.')
    act(() => root.unmount())
    root = createRoot(container)
    replies = {}
    await mount(matched())
    expect(container.textContent).not.toContain('Shared by')
  })

  it('offers the saved connection to an unmatched cluster', async () => {
    await mount({ connection: connection(), cluster: { uid: 'uid-123' } })
    await click(radio('OpenObserve'))
    expect(field('Address').value).toBe('https://oo.example.com')
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', notes: {} } })
    replies['GET /integrations/openobserve'] = [{ body: matched() }]
    await click(button('Use for this cluster'))
    expect(posts()[0].body).toEqual({ useSaved: true, clusterUid: 'uid-123' })
  })

  it('drops the Saved note once this cluster switches back to Prometheus', async () => {
    await mount({ connection: connection(), cluster: { uid: 'uid-123' } })
    await click(radio('OpenObserve'))
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', notes: { collector: true } } })
    replies['GET /integrations/openobserve'] = [{ body: matched() }]
    await click(button('Use for this cluster'))
    expect(container.textContent).toContain('Data comes from the OpenObserve collector.')
    await click(radio('Prometheus-compatible'))
    reply('DELETE /integrations/openobserve/cluster?clusterUid=uid-123', { body: { connection: connection(), cluster: { uid: 'uid-123' } } })
    await click(button('Use Prometheus for this cluster'))
    await click(radio('OpenObserve'))
    expect(container.textContent).not.toContain('Data comes from the OpenObserve collector.')
    expect(Array.from(container.querySelectorAll('[role="status"]')).some((el) => el.textContent?.trim() === 'Saved')).toBe(false)
  })

  it('asks before removing the saved connection, listing the clusters that use it', async () => {
    await mount(matched({ clusters: [{ context: 'kind-oo-b', by: 'id' }, { context: 'kind-oo-c', by: 'name' }] }))
    await click(button('Remove OpenObserve connection'))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    const dialog = document.body.textContent!
    expect(dialog).toContain('Remove the saved OpenObserve connection?')
    expect(dialog).toContain('These clusters go back to their Prometheus settings: kind-oo-b, kind-oo-c. Nothing is deleted in OpenObserve or Kubernetes.')
    reply('DELETE /integrations/openobserve', { body: unmatched })
    await click(button('Remove connection'))
    expect(calls.at(-1)).toMatchObject({ method: 'DELETE', path: '/integrations/openobserve' })
    expect(field('Address').value).toBe('')
    expect(radio('Prometheus-compatible').getAttribute('aria-checked')).toBe('true')
    expect(container.querySelector('[data-testid="prometheus-form"]')?.textContent).toContain('Status block')
  })

  it('shows the live status only with the source metrics come from', async () => {
    await mount(unmatched)
    await click(radio('OpenObserve'))
    expect(container.querySelector('fieldset[aria-label="OpenObserve connection settings"]')?.textContent).not.toContain('Status block')
    expect(container.querySelector('[data-testid="prometheus-form"]')?.textContent).toContain('Status block')
    act(() => root.unmount())
    root = createRoot(container)
    replies = {}
    await mount(matched())
    await click(radio('Prometheus-compatible'))
    expect(container.querySelector('[data-testid="prometheus-form"]')?.textContent).not.toContain('Status block')
    expect(container.querySelector('fieldset[aria-label="OpenObserve connection settings"]')?.textContent).toContain('Status block')
  })

  it('shows an environment token read-only and never sends a token for it', async () => {
    await mount({ connection: connection({ tokenSource: 'env', tokenEnv: 'RADAR_OO_TOKEN' }), cluster: { uid: 'uid-123' } })
    await click(radio('OpenObserve'))
    expect(container.textContent).toContain('Token from $RADAR_OO_TOKEN')
    expect(container.querySelector('input[type="password"]')).toBeNull()
    type(field('Organization'), 'team')
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', notes: {} } })
    replies['GET /integrations/openobserve'] = [{ body: matched({ tokenSource: 'env', tokenEnv: 'RADAR_OO_TOKEN' }) }]
    await click(button('Save changes'))
    expect(posts()[0].body).not.toHaveProperty('token')
    expect(posts()[0].body).toMatchObject({ baseUrl: 'https://oo.example.com', org: 'team', user: 'radar@example.com' })
  })

  it('asks for a new token when an environment-token connection points at another user', async () => {
    await mount({ connection: connection({ tokenSource: 'env', tokenEnv: 'RADAR_OO_TOKEN' }), cluster: { uid: 'uid-123' } })
    await click(radio('OpenObserve'))
    type(field('User or service account email'), 'other@example.com')
    expect(container.textContent).not.toContain('Token from $RADAR_OO_TOKEN')
    expect(container.textContent).toContain('A new address, organization or user needs its own token.')
    type(field('Token'), 'new-secret')
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', notes: {} } })
    replies['GET /integrations/openobserve'] = [{ body: matched() }]
    await click(button('Save changes'))
    expect(posts()[0].body).toMatchObject({ user: 'other@example.com', token: 'new-secret', clusterUid: 'uid-123' })
  })

  it('keeps "Saved" when the page cannot reload after a save', async () => {
    await mount({ connection: connection(), cluster: { uid: 'uid-123' } })
    await click(radio('OpenObserve'))
    reply('POST /integrations/openobserve/setup', { body: { outcome: 'saved', notes: {} } })
    replies['GET /integrations/openobserve'] = [{ status: 500, body: { error: 'boom' } }]
    await click(button('Use for this cluster'))
    expect(container.textContent).toContain("Saved, but Radar couldn't reload this page.")
    expect(container.textContent).not.toContain('boom')
  })

  it('keeps a saved token when editing without typing one', async () => {
    await mount(matched())
    expect(field('Token').placeholder).toBe('Saved — leave empty to keep')
    expect(field('Token').value).toBe('')
  })
  it('shows a saved connection filled in, saving only after a change, and discards back to it', async () => {
    const onDirtyChange = vi.fn()
    await mount(matched(), { onDirtyChange })
    expect(field('Address').value).toBe('https://oo.example.com')
    expect(field('User or service account email').value).toBe('radar@example.com')
    expect(button('Edit connection')).toBeUndefined()
    expect(button('Save changes')!.disabled).toBe(true)
    expect(button('Discard')).toBeUndefined()
    type(field('Address'), 'https://oo2.example.com')
    expect(button('Save changes')!.disabled).toBe(false)
    expect(container.textContent).toContain('Unsaved changes')
    expect(onDirtyChange).toHaveBeenLastCalledWith(true)
    await click(button('Discard'))
    expect(field('Address').value).toBe('https://oo.example.com')
    expect(button('Save changes')!.disabled).toBe(true)
    expect(onDirtyChange).toHaveBeenLastCalledWith(false)
  })
})
