import { test, expect, type Page } from '@playwright/test'
import type { ContextSwitchOperation } from '../src/api/contextSwitch'

interface SwitchFixtureOptions {
  lostAcknowledgement?: 'connection' | 'body'
  sourceDisconnected?: boolean
  synchronous?: boolean
  omnibar?: boolean
}

async function switchFixture(page: Page, { lostAcknowledgement, sourceDisconnected, synchronous, omnibar }: SwitchFixtureOptions = {}) {
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
        window.addEventListener('test-legacy-complete', this.legacyCompleted)
        if (initialStatusFromSSE) window.setTimeout(() => this.dispatchEvent(new MessageEvent('connection_state', {
          data: JSON.stringify({ state: 'connected', context: 'source-cluster' }),
        })), 0)
      }
      contextChanged = () => this.dispatchEvent(new MessageEvent('context_changed', { data: JSON.stringify({ context: 'target-cluster', switchId: 'test-switch' }) }))
      legacyCompleted = () => {
        this.dispatchEvent(new MessageEvent('context_changed', { data: JSON.stringify({ context: 'target-cluster' }) }))
        this.dispatchEvent(new MessageEvent('topology', { data: JSON.stringify({ nodes: [], edges: [] }) }))
      }
      close() {
        window.removeEventListener('test-context-changed', this.contextChanged)
        window.removeEventListener('test-legacy-complete', this.legacyCompleted)
      }
    }
    Object.defineProperty(window, 'EventSource', { value: TestEventSource })
  }, lostAcknowledgement === 'body')
  if (omnibar) {
    await page.route('**/api/cluster/namespace-scope', async route => {
      const original = await (await route.fetch()).json()
      await route.fulfill({ json: { ...original, context: current, actives: ['default'] } })
    })
  }
  await page.route('**/api/connection?contexts=0', route => {
    if (lostAcknowledgement === 'body' && !operation) return route.fulfill({ status: 503 })
    if (sourceDisconnected && current === 'source-cluster') {
      return route.fulfill({ json: { state: 'disconnected', context: current, errorType: 'config', error: 'The source context could not be loaded.', contextSwitch: operation } })
    }
    if (operation?.phase === 'stopped') {
      return route.fulfill({ json: { state: 'disconnected', context: current, errorType: 'switch-stopped', contextSwitch: operation } })
    }
    const syncing = operation?.phase === 'switching' || operation?.phase === 'stopping'
    return route.fulfill({ json: {
      state: syncing ? 'connecting' : 'connected', context: current, contextSwitch: operation,
      syncStatus: syncing ? {
        phase: 'syncing_critical', criticalTotal: 1, criticalSynced: 0, deferredTotal: 0, deferredSynced: 0, kinds: [],
      } : undefined,
    } })
  })
  await page.route('**/api/contexts', route => route.fulfill({
    json: ['source-cluster', 'target-cluster'].map(name => ({ name, cluster: name, isCurrent: name === current })),
  }))
  await page.route('**/api/contexts/target-cluster?track=1', route => {
    expect(route.request().method()).toBe('POST')
    starts++
    if (synchronous) {
      current = 'target-cluster'
      return route.fulfill({ json: { context: current } })
    }
    operation = {
      id: 'test-switch', startedAt: new Date().toISOString(), target: 'target-cluster', phase: 'preflight',
      preflightTimeoutSeconds: 47, syncTimeoutSeconds: 125, progress: 'Testing cluster connectivity…',
    }
    if (lostAcknowledgement === 'connection') return route.abort('connectionreset')
    if (lostAcknowledgement === 'body') return route.fulfill({ status: 202, contentType: 'application/json', body: '{' })
    return route.fulfill({ status: 202, json: operation })
  })
  await page.route('**/api/contexts/target-cluster?cancelSwitch=test-switch', route => {
    cancels++
    operation = { ...operation!, phase: 'cancelled' }
    return route.fulfill({ status: 204 })
  })
  await page.route('**/api/contexts/target-cluster?stopSwitch=test-switch', route => {
    expect(operation?.phase).toBe('switching')
    operation = { ...operation!, phase: 'stopped', canStop: false }
    return route.fulfill({ status: 204 })
  })
  await page.route('**/api/contexts/source-cluster?track=1', route => {
    current = 'source-cluster'
    starts++
    operation = { ...operation!, id: 'return-switch', target: current, phase: 'succeeded' }
    return route.fulfill({ status: 202, json: operation })
  })
  await page.route('**/api/resources/pods?*', async route => {
    const response = await route.fetch()
    const body = await response.json()
    for (const pod of body.items) pod.metadata.name += '-' + current
    await route.fulfill({ json: body })
  })
  await page.goto(omnibar ? '/resources/pods?namespaces=default' : '/resources/pods', { waitUntil: 'domcontentloaded' })
  if (!sourceDisconnected) await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  if (omnibar) {
    await page.getByRole('textbox', { name: 'Search resources and commands' }).fill('target-cluster')
    await page.getByText('target-cluster', { exact: true }).first().click()
  } else {
    await page.getByRole('button', { name: /source-cluster/ }).first().click()
    await page.getByRole('button', { name: 'target-cluster', exact: true }).click()
  }
  if (synchronous) {
    await expect.poll(() => starts).toBe(1)
    await page.evaluate(() => window.dispatchEvent(new Event('test-legacy-complete')))
  } else {
    await expect(page.getByRole('button', { name: 'Cancel switch' })).toBeVisible()
    await expect(page.getByText(/Connectivity check limit 47s/)).toBeVisible()
  }
  return {
    get starts() { return starts },
    get cancels() { return cancels },
    warming() { current = 'target-cluster'; operation = { ...operation!, phase: 'switching', canStop: true, progress: 'Loading workloads…' } },
    succeed(currentContext = 'target-cluster') { current = currentContext; operation = { ...operation!, phase: 'succeeded' } },
    refuse() { operation = { ...operation!, phase: 'failed', errorType: 'network', error: 'The target cluster did not answer.' } },
  }
}

test.afterEach(async ({ page }) => {
  await page.unrouteAll({ behavior: 'wait' })
})

test('a slow switch survives reload and completes from server status when SSE is lost', async ({ page }) => {
  const fixture = await switchFixture(page)
  fixture.warming()
  await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
  await expect(page.getByText(/Initial workload sync limit 2m 5s/)).toBeVisible()
  await page.clock.setSystemTime(new Date(Date.now() + 50_000))
  await expect(page.getByText(/Elapsed 5\ds/)).toBeVisible()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Hide progress', exact: true }).click()
  await expect(page.getByText('The switch is still running. Resources will appear when the cluster is ready.')).toBeVisible()
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

test('stopping workload sync leaves the previous connection closed and lets the user choose it again', async ({ page }) => {
  const fixture = await switchFixture(page)
  fixture.warming()
  await page.getByRole('button', { name: 'Stop switch', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Cluster switch stopped' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'target-cluster', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Reconnect cluster' })).toBeHidden()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true })).toBeHidden()
  await page.getByRole('button', { name: 'Choose a cluster' }).click()
  await page.getByRole('button', { name: 'source-cluster', exact: true }).last().click()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  expect(fixture.starts).toBe(2)
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
    const fixture = await switchFixture(page, { lostAcknowledgement: lost })
    fixture.warming()
    await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
    await page.evaluate(() => window.dispatchEvent(new Event('test-context-changed')))
    await expect(page.getByText('Loading workloads…', { exact: true })).toBeVisible()
    fixture.succeed()
    await expect(page.getByText('nginx-abc-111-target-cluster', { exact: true }).first()).toBeVisible()
    expect(fixture.starts).toBe(1)
  })
}

test('a completed switch retained by the server does not reset the newer cluster’s deep link', async ({ page }) => {
  const fixture = await switchFixture(page)
  fixture.succeed('source-cluster')
  await page.goto('/resources/pods?resource=default/nginx-abc-111-source-cluster', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('button', { name: 'source-cluster', exact: true })).toBeVisible()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  await expect(page).toHaveURL(/resource=default(?:%2F|\/)nginx-abc-111-source-cluster/)
  expect(fixture.starts).toBe(1)
})

test('a refused target remains actionable when the source is already disconnected', async ({ page }) => {
  const fixture = await switchFixture(page, { sourceDisconnected: true })
  fixture.refuse()
  await expect(page.getByText('The target cluster did not answer.', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Back to source-cluster' }).click()
  await expect(page.getByRole('heading', { name: 'Cannot Load Cluster Configuration' })).toBeVisible()
  expect(fixture.starts).toBe(1)
})

test('a synchronous server can finish a switch requested by the current browser', async ({ page }) => {
  const fixture = await switchFixture(page, { synchronous: true })
  await expect(page.getByText('nginx-abc-111-target-cluster', { exact: true }).first()).toBeVisible()
  expect(fixture.starts).toBe(1)
})

test('cancelling an omnibar switch keeps the source namespace filter', async ({ page }) => {
  const fixture = await switchFixture(page, { omnibar: true })
  await page.getByRole('button', { name: 'Cancel switch' }).click()
  await expect(page.getByText('nginx-abc-111-source-cluster', { exact: true }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Switch active namespaces' })).toContainText('default')
  expect(fixture.starts).toBe(1)
})
