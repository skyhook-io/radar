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
  return { rows, rolloutInProgress: false, partial: false, partialReasons: [], ...overrides }
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
    expect(text()).toContain("doesn't report which revision")
    expect(element.querySelector('tbody tr:nth-child(2)')?.textContent).toMatch(/Live(?!\?)/)
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

  it('names a rollout only when the workload is rolling out', async () => {
    await renderDeploys({ deploys: deploys([row({})]) })
    expect(text()).not.toContain('Rollout in progress')

    await renderDeploys({ deploys: deploys([row({})], { rolloutInProgress: true }) })
    expect(text()).toContain('Rollout in progress')
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

  it('offers no rollback when the host gives none', async () => {
    await renderDeploys({ deploys: deploys([row({})]) })

    expect(buttons('Roll back')).toHaveLength(0)
    expect(element.querySelector('a')).toBeNull()
  })

  it('offers an inline rollback on older revisions only', async () => {
    const onRollback = vi.fn()
    const older = row({ id: 'old' })
    await renderDeploys({ deploys: deploys([row({ id: 'new', revision: 3, newest: true }), older]), onRollback })

    expect(buttons('Roll back')).toHaveLength(1)
    await click(buttons('Roll back')[0])
    expect(onRollback).toHaveBeenCalledWith(older)
  })

  it('links to the rollback when the host can only link', async () => {
    await renderDeploys({ deploys: deploys([row({})]), rollbackHref: (r) => `/c/one/apps?revision=${r.revision}` })

    const link = element.querySelector('a')
    expect(link?.textContent).toBe('Roll back in cluster view')
    expect(link?.getAttribute('href')).toBe('/c/one/apps?revision=2')
  })

  it('narrows to one workload but keeps source rows', async () => {
    await renderDeploys({
      workload,
      deploys: deploys([
        row({ id: 'mine', version: 'mine' }),
        row({ id: 'other', version: 'other', workload: { ...workload, name: 'worker' } }),
        row({ id: 'helm', version: 'chart', source: 'helm', revision: undefined, workload: { kind: 'HelmRelease', namespace: 'prod', name: 'x' } }),
      ]),
    })

    expect(text()).toContain('mine')
    expect(text()).toContain('chart')
    expect(text()).not.toContain('other')
  })

  it('shows the workload column only for multi-workload apps', async () => {
    await renderDeploys({ deploys: deploys([row({})]) })
    expect(text()).not.toContain(workload.name)

    await renderDeploys({
      deploys: deploys([row({ id: 'a' }), row({ id: 'b', workload: { ...workload, name: 'worker' } })]),
    })
    expect(text()).toContain(workload.name)
  })

  it('caps long histories until asked', async () => {
    const rows = Array.from({ length: 45 }, (_, i) => row({ id: `r${i}`, revision: i + 1, version: `version-${i}` }))
    await renderDeploys({ deploys: deploys(rows) })

    expect(text()).not.toContain('version-40')
    await click(buttons('Show all 45 deploys')[0])
    expect(text()).toContain('version-40')
  })
})
