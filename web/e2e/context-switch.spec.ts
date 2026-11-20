import { test, expect, type Page } from '@playwright/test'
import type { ContextSwitchOperation } from '../src/api/contextSwitch'

async function switchFixture(page: Page, lostAcknowledgement?: 'connection' | 'body') {
  let current = 'source-cluster'
  let operation: ContextSwitchOperation | undefined
  let starts = 0
  let cancels = 0
  await page.addInitScript((initialStatusFromSSE: boolean) => {
    class TestEventSource extends EventTarget {
      static OPEN = 1
      readyState = TestEventSource.OPEN
      constructor() {
        super()
        window.addEventListener('test-context-changed', this.contextChanged)
        if (initialStatusFromSSE) window.setTimeout(() => this.dispatchEvent(new MessageEvent('connection_state', {
          data: JSON.stringify({ state: 'connected', context: 'source-cluster' }),
        })), 0)
      }
      contextChanged = () => this.dispatchEvent(new MessageEvent('context_changed', { data: JSON.stringify({ context: 'target-cluster', switchId: 'test-switch' }) }))
      close() { window.removeEventListener('test-context-changed', this.contextChanged) }
    }
    Object.defineProperty(window, 'EventSource', { value: TestEventSource })
  }, lostAcknowledgement === 'body')
  await page.route('**/api/connection?contexts=0', route => {
    if (lostAcknowledgement === 'body' && !operation) return route.fulfill({ status: 503 })
    return route.fulfill({ json: { state: operation?.phase === 'switching' ? 'connecting' : 'connected', context: current, contextSwitch: operation } })
  })
  await page.route('**/api/contexts', route => route.fulfill({
    json: ['source-cluster', 'target-cluster'].map(name => ({ name, cluster: name, isCurrent: name === current })),
  }))
  await page.route('**/api/contexts/target-cluster?track=1', route => {
    expect(route.request().method()).toBe('POST')
    starts++
    operation = { id: 'test-switch', startedAt: new Date().toISOString(), target: 'target-cluster', phase: 'preflight', progress: 'Testing cluster connectivity…' }
    if (lostAcknowledgement === 'connection') return route.abort('connectionreset')
    if (lostAcknowledgement === 'body') return route.fulfill({ status: 202, contentType: 'application/json', body: '{' })
    return route.fulfill({ status: 202, json: operation })
  })
  await page.route('**/api/contexts/target-cluster?cancelSwitch=test-switch', route => {
    cancels++
    operation = { ...operation!, phase: 'cancelled' }
    return route.fulfill({ status: 204 })
  })
  await page.route('**/api/resources/pods?*', async route => {
    const response = await route.fetch()
    const body = await response.json()
    for (const pod of body.items) pod.metadata.name += '-' + current
    await route.fulfill({ json: body })
  })
  await page.goto('/resources/pods', { waitUntil: 'domcontentloaded' })
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: /source-cluster/ }).first().click()
  await page.getByRole('button', { name: 'target-cluster', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Cancel switch' })).toBeVisible()
  return {
    get starts() { return starts },
    get cancels() { return cancels },
    warming() { current = 'target-cluster'; operation = { ...operation!, phase: 'switching', progress: 'Loading workloads…' } },
    succeed() { current = 'target-cluster'; operation = { ...operation!, phase: 'succeeded' } },
    refuse() { operation = { ...operation!, phase: 'failed', errorType: 'network', error: 'The target cluster did not answer.' } },
  }
}

test('a slow switch survives reload and completes from server status when SSE is lost', async ({ page }) => {
  const fixture = await switchFixture(page)
  fixture.warming()
  await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
  await page.clock.setSystemTime(new Date(Date.now() + 50_000))
  await expect(page.getByText(/Elapsed 5\ds/)).toBeVisible()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Dismiss', exact: true }).click()
  await expect(page.getByText('Resources will appear when the cluster is ready.')).toBeVisible()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true })).toBeHidden()
  await page.getByRole('button', { name: 'Switching to target-cluster…' }).click()
  await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
  fixture.succeed()
  await expect(page.getByRole('button', { name: /target-cluster/ }).first()).toBeVisible()
  await expect(page.getByText('Switching context', { exact: true })).toBeHidden()
  await expect(page).toHaveURL(/\/resources\/pods/)
  await expect(page.getByText('nginx-abc-111-target-cluster', { exact: true }).first()).toBeVisible()
  // A late SSE completion must preserve a resource opened after the terminal poll.
  await page.getByText('nginx-abc-111-target-cluster', { exact: true }).first().click()
  await expect(page).toHaveURL(/resource=/)
  await page.evaluate(() => window.dispatchEvent(new Event('test-context-changed')))
  await expect(page).toHaveURL(/resource=/)
  expect(fixture.starts).toBe(1)
})

test('a refused preflight returns to the running source without another switch', async ({ page }) => {
  const fixture = await switchFixture(page)
  fixture.refuse()
  await expect(page.getByText('The target cluster did not answer.', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: /Back to source-cluster/ }).click()
  await expect(page.getByRole('button', { name: /source-cluster/ }).first()).toBeVisible()
  expect(fixture.starts).toBe(1)
})

test('cancelling a preflight leaves the source available', async ({ page }) => {
  const fixture = await switchFixture(page)
  await page.getByRole('button', { name: 'Cancel switch' }).click()
  await expect(page.getByText('Switching context', { exact: true })).toBeHidden()
  await expect(page.getByRole('button', { name: /source-cluster/ }).first()).toBeVisible()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  expect(fixture.cancels).toBe(1)
  expect(fixture.starts).toBe(1)
})

for (const lost of ['connection', 'body'] as const) {
  test(`a lost ${lost} and early context event recover without submitting again`, async ({ page }) => {
    const fixture = await switchFixture(page, lost)
    fixture.warming()
    await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
    await page.evaluate(() => window.dispatchEvent(new Event('test-context-changed')))
    await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
    fixture.succeed()
    await expect(page.getByText('nginx-abc-111-target-cluster', { exact: true }).first()).toBeVisible()
    expect(fixture.starts).toBe(1)
  })
}
