import { expect, test, type Page } from '@playwright/test'
import type { IntegrationKind, IntegrationProfile, IntegrationProfiles } from '../src/components/settings/LocalConnectionSettings'

const integrations = [
  { kind: 'metrics', tab: 'Metrics', field: 'Metrics backend URL', apply: 'Apply now' },
  { kind: 'argocd', tab: 'Argo CD', field: 'Argo CD server URL', apply: 'Test & apply' },
  { kind: 'cost', tab: 'Cost', field: 'Kubecost Aggregator URL', apply: 'Test & apply' },
] as const

function profile(kind: IntegrationKind): IntegrationProfile {
  return {
    target: { binding: 'development', context: 'development', source: '/test/kubeconfig', inFileName: 'development', fingerprint: 'target', clientGeneration: 1, operationGeneration: 1,
      identity: { server: 'https://cluster.example', user: 'developer', trust: 'test', insecureTls: false } },
    revision: '1', state: 'saved', mode: kind === 'cost' ? 'kubecost' : 'auto',
    url: `https://${kind}.example`, headerKeys: kind === 'metrics' ? ['Authorization'] : [], envHeaderKeys: [], headersManaged: false,
    secretSet: kind !== 'metrics', insecureTls: false, clusterId: '',
  }
}

async function fixture(page: Page) {
  const profiles: IntegrationProfiles = { metrics: profile('metrics'), argocd: profile('argocd'), cost: profile('cost') }
  let file: Record<string, unknown> = {}
  const writes: Record<string, unknown>[] = []
  let waitForApply: Promise<void> | undefined
  let releaseApply: (() => void) | undefined
  let statusFailure = false
  let preserveRevision = false
  await page.route('**/api/capabilities', async route => {
    const response = await route.fetch()
    const capabilities = await response.json()
    await route.fulfill({ json: { ...capabilities, deployment: { ...capabilities.deployment, mode: 'local' } } })
  })
  await page.route('**/api/config', async route => {
    if (route.request().method() === 'PUT') {
      file = route.request().postDataJSON()
      return route.fulfill({ json: file })
    }
    return route.fulfill({ json: { management: 'local', file, effective: file, isDesktop: false, integrationProfiles: profiles } })
  })
  await page.route('**/api/prometheus/status', route => route.fulfill({ json: { connected: !statusFailure, available: true, discovering: false, address: profiles.metrics.url, ...(statusFailure ? { error: 'Backend unavailable' } : {}) } }))
  await page.route('**/api/integrations/argocd/status', route => route.fulfill({ json: { connected: true, configured: true, address: profiles.argocd.url } }))
  await page.route('**/api/opencost/summary', route => route.fulfill({ json: { available: true, source: 'kubecost' } }))
  await page.route('**/api/integrations/connections', async route => {
    if (route.request().method() === 'PUT') {
      const update = route.request().postDataJSON()
      writes.push(update)
      await waitForApply
      const current = profiles[update.kind as IntegrationKind]
      if (!preserveRevision) current.revision = String(Number(current.revision) + 1)
      if (update.action === 'reconfirm') {
        for (const kind of update.kinds as IntegrationKind[]) profiles[kind].state = 'saved'
      }
      if (update.url !== undefined) current.url = update.url
      if (update.mode !== undefined) current.mode = update.mode
      if (update.clusterId !== undefined) current.clusterId = update.clusterId
      if (update.secret?.action === 'set') current.secretSet = true
      if (update.secret?.action === 'clear') current.secretSet = false
      if (update.headers) {
        const keys = new Set(current.headerKeys)
        for (const header of update.headers) {
          if (header.action === 'set') keys.add(header.key)
          if (header.action === 'clear') keys.delete(header.key)
        }
        current.headerKeys = [...keys]
      }
      current.state = 'saved'
    }
    return route.fulfill({ json: { profiles, connections: [], unlinkedAssignments: [], checked: true, connected: true } })
  })
  return {
    profiles, writes, file: () => file,
    delayApply() { waitForApply = new Promise<void>(resolve => { releaseApply = resolve }) },
    releaseApply() { releaseApply?.(); waitForApply = undefined },
    failStatus() { statusFailure = true },
    preserveRevision() { preserveRevision = true },
  }
}

async function openSettings(page: Page, tab: string) {
  await page.goto('/')
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await page.getByRole('tab', { name: tab, exact: true }).click()
  return page.getByRole('dialog', { name: 'Settings', exact: true })
}

for (const integration of integrations) {
  test(`${integration.tab}: save startup changes without dropping the integration draft`, async ({ page }) => {
    const state = await fixture(page)
    const dialog = await openSettings(page, integration.tab)
    const field = page.getByRole('textbox', { name: integration.field, exact: true })
    await field.fill('https://draft.example')
    await page.getByRole('tab', { name: 'Connection', exact: true }).click()
    await page.getByPlaceholder('System default', { exact: true }).fill('Safari')
    await page.keyboard.press('Escape')
    await dialog.getByRole('button', { name: 'Save other changes', exact: true }).click()
    await expect.poll(() => state.file().browser).toBe('Safari')
    await expect(dialog.getByText('Saved. Restart Radar to apply.', { exact: true })).toBeVisible()
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: `Review ${integration.tab}`, exact: true }).click()
    await expect(field).toHaveValue('https://draft.example')
    expect(state.writes).toHaveLength(0)
    await dialog.getByRole('button', { name: 'Discard changes', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example`)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
  })

  test(`${integration.tab}: discard only the newer draft after applying`, async ({ page }) => {
    const state = await fixture(page)
    const dialog = await openSettings(page, integration.tab)
    const field = page.getByRole('textbox', { name: integration.field, exact: true })
    await field.fill(`https://${integration.kind}.example/applied`)
    await page.getByRole('button', { name: integration.apply, exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    await expect(page.getByText('Connection checked', { exact: true })).toBeVisible()
    await field.fill(`https://${integration.kind}.example/draft`)
    await dialog.getByRole('button', { name: 'Discard changes', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example/applied`)
    expect(state.writes).toHaveLength(1)
  })
}

test('discard clears drafts in all integration editors, including credentials and mapping', async ({ page }) => {
  const state = await fixture(page)
  const dialog = await openSettings(page, 'Metrics')
  await page.getByRole('button', { name: 'Edit headers', exact: true }).click()
  await page.getByRole('combobox', { name: 'Authorization action' }).selectOption('set')
  await page.getByRole('textbox', { name: 'Authorization value' }).fill('draft-secret')
  await page.getByRole('tab', { name: 'Argo CD', exact: true }).click()
  await page.getByRole('textbox', { name: 'API token', exact: true }).fill('draft-token')
  await page.getByRole('tab', { name: 'Cost', exact: true }).click()
  await page.getByRole('textbox', { name: 'API key', exact: true }).fill('draft-key')
  await page.getByRole('button', { name: 'Cluster mapping', exact: true }).click()
  await page.getByRole('textbox', { name: 'Kubecost cluster ID', exact: true }).fill('draft-cluster')
  await dialog.getByRole('button', { name: 'Discard changes', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'API key', exact: true })).toHaveValue('')
  await page.getByRole('button', { name: 'Cluster mapping', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Kubecost cluster ID', exact: true })).toHaveValue('')
  await page.getByRole('tab', { name: 'Argo CD', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'API token', exact: true })).toHaveValue('')
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await page.getByRole('button', { name: 'Edit headers', exact: true }).click()
  await expect(page.getByRole('combobox', { name: 'Authorization action' })).toHaveValue('keep')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  expect(state.writes).toHaveLength(0)
})

test('applying one integration preserves another integration draft for its own apply', async ({ page }) => {
  const state = await fixture(page)
  await openSettings(page, 'Metrics')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example/draft')
  await page.getByRole('tab', { name: 'Argo CD', exact: true }).click()
  await page.getByRole('textbox', { name: 'Argo CD server URL' }).fill('https://argocd.example/applied')
  await page.getByRole('button', { name: 'Test & apply', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Metrics backend URL' })).toHaveValue('https://metrics.example/draft')
  await page.getByRole('button', { name: 'Apply now', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  expect(state.writes.map(write => write.kind)).toEqual(['argocd', 'metrics'])
})

test('an in-flight apply cannot be discarded or closed as though it were cancelled', async ({ page }) => {
  const state = await fixture(page)
  state.delayApply()
  const dialog = await openSettings(page, 'Argo CD')
  await page.getByRole('textbox', { name: 'Argo CD server URL' }).fill('https://argocd.example/applied')
  await page.getByRole('button', { name: 'Test & apply', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  await expect(dialog.getByRole('button', { name: 'Discard changes', exact: true })).toBeDisabled()
  await expect(dialog.getByRole('button', { name: 'Close settings', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeVisible()
  state.releaseApply()
  await expect(page.getByText('Connection checked', { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
})

test('Cost Auto hides optional overrides, but keeps entered credentials when collapsed', async ({ page }) => {
  const state = await fixture(page)
  Object.assign(state.profiles.cost, { state: 'auto', mode: 'auto', url: '', secretSet: false })
  await openSettings(page, 'Cost')
  const disclosure = page.getByRole('button', { name: /^Kubecost connection overrides/ })
  const url = page.getByRole('textbox', { name: 'Kubecost Aggregator URL' })
  await expect(disclosure).toHaveAttribute('aria-expanded', 'false')
  await expect.poll(() => url.evaluate(el => !!el.closest('[inert]'))).toBe(true)
  await expect(page.getByText('Display preference · all local clusters. Saved automatically.', { exact: true })).toBeVisible()
  await disclosure.click()
  await url.fill('https://cost.example')
  await page.getByRole('textbox', { name: 'API key', exact: true }).fill('new-key')
  await disclosure.click()
  await disclosure.click()
  await expect(url).toHaveValue('https://cost.example')
  await expect(page.getByRole('textbox', { name: 'API key', exact: true })).toHaveValue('new-key')
  await page.getByRole('combobox', { name: 'Cost source', exact: true }).selectOption('kubecost')
  await expect(disclosure).toBeHidden()
  await expect(url).toBeVisible()
  await page.getByRole('combobox', { name: 'Cost source', exact: true }).selectOption('auto')
  await expect(disclosure).toHaveAttribute('aria-expanded', 'true')
  await expect(url).toHaveValue('https://cost.example')
  await disclosure.click()
  await expect(disclosure).toContainText('configured')
  await expect(disclosure).toHaveAttribute('aria-expanded', 'false')
})

test('checking an unchanged metrics connection shows one success result', async ({ page }) => {
  const state = await fixture(page)
  state.preserveRevision()
  await openSettings(page, 'Metrics')
  await page.getByRole('button', { name: 'Apply now', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  await expect(page.getByRole('tabpanel').getByText(/Connected to .*applied, no restart needed/)).toHaveCount(0)
})

test('confirming one changed integration removes it from the sibling review picker', async ({ page }) => {
  const state = await fixture(page)
  state.profiles.metrics.state = 'target_changed'
  state.profiles.cost.state = 'target_changed'
  await openSettings(page, 'Cost')
  await page.getByRole('button', { name: 'Review changes', exact: true }).click()
  await page.getByRole('checkbox', { name: /Cost/ }).check()
  await page.getByRole('button', { name: 'Use selected connections for this target', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await page.getByRole('button', { name: 'Review changes', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: /Cost/ })).toHaveCount(0)
  await page.getByRole('checkbox', { name: /Metrics/ }).check()
  await page.getByRole('button', { name: 'Use selected connections for this target', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1].kinds).toEqual(['metrics'])
  expect(state.writes[1].revisions).toEqual({ metrics: '1' })
})

test('legacy metrics apply also blocks closing or discarding while committing', async ({ page }) => {
  await fixture(page)
  await page.route('**/api/config', route => route.fulfill({ json: { management: 'local', file: {}, effective: {}, isDesktop: false } }))
  let release: (() => void) | undefined
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/integrations/prometheus', async route => {
    await held
    await route.fulfill({ json: { connected: true, address: 'https://metrics.example' } })
  })
  const dialog = await openSettings(page, 'Metrics')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example')
  await page.getByRole('button', { name: 'Apply now', exact: true }).click()
  await expect(dialog.getByRole('button', { name: 'Close settings', exact: true })).toBeDisabled()
  await expect(dialog.getByRole('button', { name: 'Discard changes', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeVisible()
  release?.()
  await expect(page.getByText('Connected to https://metrics.example — applied, no restart needed', { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
})

test('current connection does not describe a draft URL, and refreshes after apply', async ({ page }) => {
  const state = await fixture(page)
  await openSettings(page, 'Metrics')
  const current = page.getByLabel('Current connection', { exact: true })
  await expect(current).toContainText('https://metrics.example')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example/new')
  await expect(current).not.toContainText('https://metrics.example/new')
  state.failStatus()
  await page.getByRole('button', { name: 'Apply now', exact: true }).click()
  await expect(current).toContainText('Not connected')
  await expect(current).toContainText('Backend unavailable')
  await expect(current).not.toContainText('https://metrics.example')
})
