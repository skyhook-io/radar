// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApplicationDeploys, type ApplicationDeploysProps } from './ApplicationDeploys'
import type { AppDeployRow, AppDeploys } from '../../utils/app-deploys'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const workload = { kind: 'Deployment', namespace: 'prod', name: 'checkout-api-production-primary-us-east1-b' }

function row(overrides: Partial<AppDeployRow>): AppDeployRow {
  return {
    id: 'row',
    workload,
    revision: 2,
    version: 'v2',
    source: 'revision',
    at: '2026-11-02T10:00:00Z',
    live: false,
    newest: false,
    ...overrides,
  }
}

function deploys(rows: AppDeployRow[], overrides: Partial<AppDeploys> = {}): AppDeploys {
  return { rows, partial: false, partialReasons: [], stillLoading: [], ...overrides }
}

describe('ApplicationDeploys', () => {
  let element: HTMLDivElement
  let root: Root

  beforeEach(() => {
    element = document.createElement('div')
    document.body.append(element)
    root = createRoot(element)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    element.remove()
  })

  const paint = (node: ReactNode) => act(async () => root.render(node))
  const renderDeploys = (props: Partial<ApplicationDeploysProps> = {}) =>
    paint(<ApplicationDeploys deploys={deploys([])} {...props} />)
  const text = () => element.textContent ?? ''
  const buttons = (name: string) =>
    [...element.querySelectorAll('button')].filter(
      (button) => (button.getAttribute('aria-label') ?? button.textContent) === name,
    )
  const click = (button: HTMLElement) => act(async () => button.click())

  it('shows a loader until the host has a result', async () => {
    await renderDeploys({ deploys: null })

    expect(text()).toContain('Loading deploys…')
  })

  it('keeps a loader until something has loaded', async () => {
    await renderDeploys({ deploys: deploys([], { stillLoading: ['Still loading revisions for 2 workloads.'] }) })

    expect(text()).toContain('Loading deploys…')
    expect(text()).not.toContain('No deploys to show.')
  })

  it('shows what has loaded and says what is left', async () => {
    await renderDeploys({ deploys: deploys([row({})], { stillLoading: ['Still loading revisions for 2 workloads.'] }) })

    expect(text()).toContain('v2')
    expect(text()).toContain('Still loading revisions for 2 workloads.')
    expect(text()).not.toContain('Loading deploys…')
  })

  it('tells the host when it is on screen', async () => {
    const onVisibleChange = vi.fn()
    await renderDeploys({ onVisibleChange })
    expect(onVisibleChange).toHaveBeenLastCalledWith(true)

    await paint(null)
    expect(onVisibleChange).toHaveBeenLastCalledWith(false)
  })

  it('says when there are no revisions', async () => {
    await renderDeploys()

    expect(text()).toContain('No deploys to show.')
    expect(text()).toContain('has none retained')
  })

  it('does not read as empty when sources are missing', async () => {
    await renderDeploys({
      deploys: deploys([], { partial: true, partialReasons: ["Helm history for checkout isn't available here."] }),
    })

    expect(text()).toContain('This history is partial')
    expect(text()).toContain("Helm history for checkout isn't available here.")
    expect(text()).toContain("couldn't be read")
    expect(text()).not.toContain('has none retained')
  })

  it('marks Live and Newest, and shows unknown Live as unknown', async () => {
    await renderDeploys({
      deploys: deploys([
        row({ id: 'a', revision: 3, version: 'v3', newest: true, live: 'unknown', ready: 2, desired: 3 }),
        row({ id: 'b', revision: 2, version: 'v2', live: true, ready: 1, desired: 1 }),
      ]),
    })

    expect(text()).toContain('Newest')
    expect(text()).toContain('Live?')
    expect(text()).toContain('2/3 ready')
    expect(text()).toContain("can't tell whether")
    expect(element.querySelector('tbody tr:nth-child(2)')?.textContent).toMatch(/Live(?!\?)/)
  })

  it('shows a repeated change-cause without reading as not recorded', async () => {
    await renderDeploys({ deploys: deploys([row({ repeatedCause: 'release 42' })]) })

    expect(text()).toContain('release 42')
    expect(text()).not.toContain('Not recorded')
  })

  it('reads a revision with no pods as scaled down', async () => {
    await renderDeploys({ deploys: deploys([row({ ready: 0, desired: 0 })]) })

    expect(text()).toContain('Scaled down')
    expect(text()).not.toContain('0/0')
  })

  it('shows no Live badge on source rows', async () => {
    await renderDeploys({
      deploys: deploys([row({ id: 'h', source: 'helm', revision: undefined, version: 'chart-1.2', via: 'Helm revision 7', live: 'unknown' })]),
    })

    expect(text()).not.toContain('Live?')
    expect(text()).toContain('Helm revision 7')
  })

  it('names a rollout the host reports, and marks one that is stuck', async () => {
    await renderDeploys({ deploys: deploys([row({})]) })
    expect(text()).not.toContain('2 updating')

    const summary = { count: 2, label: '2 updating', detail: 'api: updating · worker: updating', details: [], health: 'neutral' as const }
    await renderDeploys({ deploys: deploys([row({})]), rolloutSummary: summary })
    expect(text()).toContain('2 updating')
    expect(element.querySelector('.text-red-700, [class*="red"]')).toBeNull()

    await renderDeploys({ deploys: deploys([row({})]), rolloutSummary: { ...summary, label: '2 stalled', health: 'unhealthy' } })
    expect(text()).toContain('2 stalled')
    expect(element.querySelector('[class*="red"]')).not.toBeNull()
  })

  it('claims nothing about Live on a list it could not refresh', async () => {
    await renderDeploys({
      deploys: deploys([row({ live: 'unknown', stale: true })], { partial: true, partialReasons: ["Couldn't refresh revisions."] }),
    })

    expect(text()).not.toContain('Live')
    expect(text()).not.toContain('Newest')
    expect(text()).not.toContain("can't tell whether")
  })

  it('expands a revision to its diff against the previous one', async () => {
    await renderDeploys({
      deploys: deploys([row({ template: 'image: v2\n', previousTemplate: 'image: v1\n', previousRevision: 1 })]),
    })

    expect(text()).not.toContain('Revision #1')
    await click(buttons('Show changes in v2')[0])
    expect(text()).toContain('Revision #1')
    expect(text()).toContain('+image: v2')
  })

  it('shows the workload column only for multi-workload apps', async () => {
    await renderDeploys({ deploys: deploys([row({})]) })
    expect(text()).not.toContain(workload.name)

    await renderDeploys({
      deploys: deploys([row({ id: 'a' }), row({ id: 'b', workload: { ...workload, name: 'worker' } })]),
    })
    expect(text()).toContain(workload.name)
  })

  it('names the namespace when an app spans several', async () => {
    await renderDeploys({
      deploys: deploys([row({ id: 'a' }), row({ id: 'b', workload: { ...workload, name: 'worker' } })]),
    })
    expect(text()).not.toContain(`prod/${workload.name}`)

    await renderDeploys({
      deploys: deploys([
        row({ id: 'a', workload: { ...workload, namespace: 'prod', name: 'api' } }),
        row({ id: 'b', workload: { ...workload, namespace: 'staging', name: 'api' } }),
      ]),
    })
    expect(text()).toContain('prod/api')
    expect(text()).toContain('staging/api')
  })

  it('caps long histories until asked', async () => {
    const rows = Array.from({ length: 45 }, (_, i) => row({ id: `r${i}`, revision: i + 1, version: `version-${i}` }))
    await renderDeploys({ deploys: deploys(rows) })

    expect(text()).not.toContain('version-40')
    await click(buttons('Show all 45 deploys')[0])
    expect(text()).toContain('version-40')
  })
})
