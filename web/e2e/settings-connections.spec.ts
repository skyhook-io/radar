import { expect, test, type Page } from '@playwright/test'
import type { ConnectionResponse, IntegrationKind, IntegrationProfile, IntegrationProfiles } from '../src/components/settings/LocalConnectionSettings'

const integrations = [
  { kind: 'metrics', tab: 'Metrics', field: 'Metrics backend URL', apply: 'Save changes' },
  { kind: 'argocd', tab: 'Argo CD', field: 'Argo CD server URL', apply: 'Save changes' },
  { kind: 'cost', tab: 'Cost', field: 'Kubecost Aggregator URL', apply: 'Save changes' },
] as const

for (const [width, height] of [[1280, 800], [1366, 768], [1440, 900], [1920, 1080]]) {
  test(`Overview fits ${width}x${height} with one version and an update link in the header`, async ({ page }) => {
    const state = await fixture(page)
    state.failStatus()
    await page.route('**/api/version-check', route => route.fulfill({ json: { currentVersion: 'v1.14.1-test', latestVersion: '1.14.2', updateAvailable: true, releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.14.2' } }))
    await page.setViewportSize({ width, height })
    const dialog = await openSettings(page, 'Overview')
    await expect(page.getByRole('button', { name: 'Configuration files', exact: true })).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Update available', exact: true })).toBeVisible()
    await expect(dialog.getByText(/Radar v1\.14\.1-test/)).toHaveCount(1)
    await expect(dialog.getByText(/Radar vv/)).toHaveCount(0)
    await expect(dialog.getByText(/you're on/)).toHaveCount(0)
    await dialog.evaluate(async el => { await Promise.all(el.getAnimations({ subtree: true }).filter(animation => animation.effect?.getTiming().iterations !== Infinity).map(animation => animation.finished.catch(() => {}))) })
    const layout = await dialog.evaluate(el => {
      const rect = el.getBoundingClientRect()
      const content = el.querySelector<HTMLElement>('div.overflow-y-auto.p-4')!
      return { width: rect.width, height: rect.height, top: rect.top, bottom: rect.bottom, overflow: content.scrollHeight - content.clientHeight }
    })
    expect(layout.width).toBe(1024)
    expect(layout.height).toBe(Math.min(800, height - 64))
    expect(layout.top).toBeGreaterThanOrEqual(32)
    expect(layout.bottom).toBeLessThanOrEqual(height - 32)
    expect(layout.overflow).toBeLessThanOrEqual(1)
    const label = dialog.getByRole('tabpanel').getByText('AI investigations', { exact: true })
    expect(await label.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  })
}

for (const mode of ['in-cluster', 'cloud']) {
  test(`${mode} header preserves the deployment-specific update behavior`, async ({ page }) => {
    await fixture(page)
    await page.route('**/api/capabilities', async route => {
      const capabilities = await (await route.fetch()).json()
      await route.fulfill({ json: { ...capabilities, deployment: { ...capabilities.deployment, mode } } })
    })
    await page.route('**/api/version-check', route => route.fulfill({ json: { currentVersion: '1.14.1', latestVersion: '1.14.2', updateAvailable: true, releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.14.2' } }))
    const dialog = await openSettings(page, 'Overview')
    if (mode === 'cloud') {
      await expect(dialog.getByRole('link', { name: 'Update available', exact: true })).toHaveCount(0)
      await expect(dialog.getByRole('button', { name: 'Update available', exact: true })).toHaveCount(0)
    }
    else await expect(dialog.getByRole('link', { name: 'Update available', exact: true })).toHaveAttribute('href', 'https://radarhq.io/docs/configuration/in-cluster')
  })
}

for (const installMethod of ['homebrew', 'krew', 'direct', 'desktop']) {
  test(`Settings offers ${installMethod} update instructions without leaving the dialog`, async ({ page }) => {
    await fixture(page)
    const command = installMethod === 'homebrew' ? 'brew upgrade radar' : installMethod === 'krew' ? 'kubectl krew upgrade radar' : 'curl -fsSL https://radarhq.io/install.sh | sh'
    await page.route('**/api/version-check', route => route.fulfill({ json: {
      currentVersion: '1.14.1', latestVersion: '1.14.2', updateAvailable: true, installMethod,
      updateCommand: installMethod === 'desktop' ? undefined : command,
      releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.14.2',
    } }))
    await page.addInitScript(() => localStorage.setItem('radar-update-dismissed', '1.14.2'))
    const dialog = await openSettings(page, 'Metrics')
    const trigger = dialog.getByRole('button', { name: 'Update available', exact: true })
    await trigger.click()
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    if (installMethod === 'desktop') await expect(dialog.getByRole('button', { name: 'Update Now', exact: true })).toBeVisible()
    else await expect(dialog.getByRole('button', { name: command, exact: true })).toBeVisible()
    await expect(dialog.getByText(/You're on/)).toHaveCount(0)
    if (installMethod === 'direct') await expect(dialog.getByRole('link', { name: 'or download from GitHub' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await expect(trigger).toBeFocused()
    await expect(dialog).toBeVisible()
    await trigger.click()
    await dialog.getByRole('tab', { name: 'Overview', exact: true }).click()
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  })
}

function profile(kind: IntegrationKind): IntegrationProfile {
  return {
    target: { binding: 'development', context: 'development', source: '/test/kubeconfig', inFileName: 'development', fingerprint: 'target', clientGeneration: 1, operationGeneration: 1,
      identity: { server: 'https://cluster.example', user: 'developer', trust: 'test', insecureTls: false } },
    revision: '1', state: 'saved', mode: kind === 'cost' ? 'kubecost' : 'auto',
    url: `https://${kind}.example`, headerKeys: kind === 'metrics' ? ['Authorization'] : [], envHeaderKeys: [], headersManaged: false,
    secretSet: kind !== 'metrics', insecureTls: false, clusterId: '',
  }
}

const discoverySettings = { url: '', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false }

async function fixture(page: Page) {
  const profiles: IntegrationProfiles = { metrics: profile('metrics'), argocd: profile('argocd'), cost: profile('cost') }
  let file: Record<string, unknown> = {}
  const writes: Record<string, unknown>[] = []
  let waitForApply: Promise<void> | undefined
  let releaseApply: (() => void) | undefined
  let statusFailure = false
  let preserveRevision = false
  let failNextCopy = false
  let applyError = ''
  let catalogFailure = false
  const connections: ConnectionResponse['connections'] = []
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
    if (route.request().method() === 'GET' && catalogFailure)
      return route.fulfill({ status: 503, json: { error: 'Catalog unavailable' } })
    if (route.request().method() === 'PUT') {
      const update = route.request().postDataJSON()
      writes.push(update)
      if (update.action === 'copy' && failNextCopy) {
        failNextCopy = false
        connections[0].revision = 'refreshed-source'
        return route.fulfill({ status: 409, json: { error: 'Source settings changed' } })
      }
      await waitForApply
      if (update.action === 'forget') {
        for (let index = connections.length - 1; index >= 0; index--) {
          const entry = connections[index]
          if (entry.binding === update.binding && entry.integration === update.kind) connections.splice(index, 1)
        }
        return route.fulfill({ json: { profiles, connections, checked: false, connected: false } })
      }
      const current = profiles[update.kind as IntegrationKind]
      if (update.action === 'dismiss_legacy') {
        delete current.legacy
        current.revision = String(Number(current.revision) + 1)
        return route.fulfill({ json: { profiles, connections, checked: false, connected: false } })
      }
      if (update.action === 'adopt') {
        Object.assign(current, current.legacy)
        delete current.legacy
      }
      if (update.action === 'copy') {
        const source = connections.find(connection => connection.binding === update.binding && connection.integration === update.kind)!
        Object.assign(current, { url: source.url, headerKeys: [...source.headerKeys], envHeaderKeys: [...source.envHeaderKeys], secretSet: source.secretSet, insecureTls: source.insecureTls })
      }
      if (update.action === 'replace') Object.assign(current, { headerKeys: [], secretSet: false, insecureTls: false, clusterId: '' })
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
      if (update.action === 'auto') Object.assign(current, { state: 'auto', mode: update.mode || 'auto', url: '', headerKeys: [], secretSet: false, clusterId: '' })
    }
    return route.fulfill({ json: { profiles, connections, checked: true, connected: !applyError, ...(route.request().method() === 'PUT' && applyError ? { error: applyError } : {}) } })
  })
  return {
    profiles, writes, connections, file: () => file,
    delayApply() { waitForApply = new Promise<void>(resolve => { releaseApply = resolve }) },
    releaseApply() { releaseApply?.(); waitForApply = undefined },
    failStatus() { statusFailure = true },
    recoverStatus() { statusFailure = false },
    preserveRevision() { preserveRevision = true },
    failNextCopy() { failNextCopy = true },
    failCatalog() { catalogFailure = true },
    recoverCatalog() { catalogFailure = false },
    failConnectionCheck() { applyError = 'Saved, but the metrics backend could not be reached. Check its URL, authentication, network access and TLS configuration.' },
  }
}

for (const integration of integrations) {
  test(`${integration.tab}: previous settings are discoverable, editable drafts until Save`, async ({ page }, testInfo) => {
    const state = await fixture(page)
    const current = state.profiles[integration.kind]
    Object.assign(current, discoverySettings, { state: 'auto', mode: 'auto', legacy: {
      url: 'https://previous.example', headerKeys: integration.kind === 'metrics' ? ['Authorization'] : [],
      envHeaderKeys: [], secretSet: integration.kind !== 'metrics', insecureTls: integration.kind === 'argocd',
      clusterId: integration.kind === 'cost' ? 'development-cost' : '', mode: integration.kind === 'cost' ? 'kubecost' : 'connection', revision: 'previous-revision',
    } })
    const dialog = await openSettings(page, 'Overview')
    const notice = dialog.getByRole('region', { name: 'Previous integration settings', exact: true })
    await expect(notice).toBeVisible()
    await expect(notice.getByRole('button')).toHaveCount(1)
    if (integration.kind === 'metrics') await page.screenshot({ path: testInfo.outputPath('previous-settings-overview.png') })
    await notice.getByRole('button', { name: `Review ${integration.tab}`, exact: true }).click()
    await expect(dialog.getByRole('tab', { name: integration.tab, exact: true })).toHaveAttribute('aria-selected', 'true')
    await dialog.getByRole('button', { name: 'Use previous settings', exact: true }).click()
    const field = dialog.getByRole('textbox', { name: integration.field, exact: true })
    await expect(field).toHaveValue('https://previous.example')
    await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    expect(state.writes).toHaveLength(0)
    if (integration.kind === 'metrics') {
      await expect(dialog.getByLabel('Authorization value', { exact: true })).toBeVisible()
      await page.screenshot({ path: testInfo.outputPath('previous-settings-draft.png') })
    }
    await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
    await expect(dialog.getByRole('button', { name: 'Use previous settings', exact: true })).toBeVisible()
    expect(state.writes).toHaveLength(0)
    await dialog.getByRole('button', { name: 'Use previous settings', exact: true }).click()
    await field.fill('https://previous.example/edited')
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    expect(state.writes[0]).toMatchObject({ action: 'adopt', kind: integration.kind, url: 'https://previous.example/edited', legacyRevision: 'previous-revision' })
    await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
    await dialog.getByRole('tab', { name: 'Overview', exact: true }).click()
    await expect(notice).toHaveCount(0)
  })

  test(`${integration.tab}: dismissing previous settings removes the Overview offer without applying`, async ({ page }) => {
    const state = await fixture(page)
    Object.assign(state.profiles[integration.kind], discoverySettings, { state: 'auto', mode: 'auto', legacy: {
      url: 'https://previous.example', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
      clusterId: '', mode: 'auto', revision: 'previous-revision',
    } })
    const dialog = await openSettings(page, 'Overview')
    await dialog.getByRole('button', { name: `Review ${integration.tab}`, exact: true }).click()
    await dialog.getByRole('button', { name: 'Dismiss for this cluster', exact: true }).click()
    await expect(dialog.getByRole('button', { name: 'Use previous settings', exact: true })).toHaveCount(0)
    expect(state.writes).toHaveLength(1)
    expect(state.writes[0].action).toBe('dismiss_legacy')
    expect(state.profiles[integration.kind].url).toBe('')
    await dialog.getByRole('tab', { name: 'Overview', exact: true }).click()
    await expect(dialog.getByRole('region', { name: 'Previous integration settings', exact: true })).toHaveCount(0)
  })

  test(`${integration.tab}: clearing a copied credentialed URL is guarded before confirmation`, async ({ page }) => {
    const state = await fixture(page)
    Object.assign(state.profiles[integration.kind], { headerKeys: [], secretSet: false })
    state.connections.push({ url: 'https://source.example', headerKeys: integration.kind === 'metrics' ? ['Authorization'] : [], envHeaderKeys: [], secretSet: integration.kind !== 'metrics', insecureTls: false,
      binding: 'source', integration: integration.kind, context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: '1' })
    const dialog = await openSettings(page, integration.tab)
    await dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
    await dialog.getByRole('option', { name: /staging/ }).click()
    await dialog.getByRole('textbox', { name: integration.field, exact: true }).fill('')
    await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(dialog.getByRole('alert')).toContainText('choose Use auto-discovery')
    await expect(page.getByRole('heading', { name: 'Replace saved connection?', exact: true })).toHaveCount(0)
    expect(state.writes).toHaveLength(0)
  })

  test(`${integration.tab}: catalog recovery preserves the copied draft`, async ({ page }) => {
    const state = await fixture(page)
    state.connections.push({ url: 'https://source.example', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
      binding: 'source', integration: integration.kind, context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: '1' })
    const dialog = await openSettings(page, integration.tab)
    await dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
    state.failCatalog()
    await dialog.getByRole('option', { name: /staging/ }).click()
    const field = dialog.getByRole('textbox', { name: integration.field, exact: true })
    await field.fill('https://source.example/draft')
    await expect(dialog.getByText('Could not load other clusters.', { exact: false })).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Discard draft & reload', exact: true })).toHaveCount(0)
    await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    state.recoverCatalog()
    await dialog.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(dialog.getByText('Could not load other clusters.', { exact: false })).toHaveCount(0)
    await expect(field).toHaveValue('https://source.example/draft')
    await expect(dialog.getByText(/Copied from staging/)).toBeVisible()
    expect(state.writes).toHaveLength(0)
  })

  test(`${integration.tab}: clearing a credentialed URL offers discovery before confirming`, async ({ page }) => {
    const state = await fixture(page)
    await openSettings(page, integration.tab)
    await page.getByRole('textbox', { name: integration.field, exact: true }).fill('')
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('choose Use auto-discovery')
    expect(state.writes).toHaveLength(0)
    await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
    await expect(page.getByRole('alert')).toHaveCount(0)
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Use auto-discovery?', exact: true })).toBeVisible()
    expect(state.writes).toHaveLength(0)
  })

  test(`${integration.tab}: editing a staged discovery draft does not reuse removed credentials`, async ({ page }) => {
    const state = await fixture(page)
    await openSettings(page, integration.tab)
    await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await page.getByRole('tab', { name: integration.tab, exact: true }).click()
    if (integration.kind === 'cost') await page.getByRole('combobox', { name: 'Cost source', exact: true }).selectOption('kubecost')
    await page.getByRole('textbox', { name: integration.field, exact: true }).fill('https://replacement.example')
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Replace saved connection?', exact: true }) })
    await confirmation.getByRole('button', { name: 'Replace connection', exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    expect(state.writes[0]).toMatchObject({ action: 'replace', confirmRemoval: true, url: 'https://replacement.example' })
    if (integration.kind === 'metrics') expect(state.writes[0].headers).toEqual([])
    else expect(state.writes[0].secret).toEqual({ action: 'keep' })
  })

  for (const stateName of ['auto', 'saved'] as const) {
    test(`${integration.tab}: disables discovery for ${stateName} automatic settings`, async ({ page }) => {
      const state = await fixture(page)
      Object.assign(state.profiles[integration.kind], {
        state: stateName, mode: 'auto', url: '', secretSet: false, headerKeys: [], clusterId: ''
      })
      await openSettings(page, integration.tab)
      await expect(page.getByRole('button', { name: 'Use auto-discovery', exact: true })).toBeDisabled()
      expect(state.writes).toHaveLength(0)
    })
  }

  test(`${integration.tab}: saving keeps the sidebar and form anchored`, async ({ page }) => {
    const state = await fixture(page)
    await page.setViewportSize({ width: 1920, height: 1080 })
    await openSettings(page, integration.tab)
    const nav = page.getByRole('tab', { name: 'Metrics', exact: true })
    const field = page.getByRole('textbox', { name: integration.field, exact: true })
    await field.fill(`https://${integration.kind}.example/new`)
    await page.getByRole('button', { name: 'Save changes', exact: true }).scrollIntoViewIfNeeded()
    await page.getByRole('dialog').evaluate(async el => {
      await Promise.all(el.getAnimations({ subtree: true }).filter(animation => animation.effect?.getTiming().iterations !== Infinity).map(animation => animation.finished.catch(() => {})))
    })
    const navBefore = await nav.boundingBox()
    // Ignore native click-to-focus scrolling; measure movement within the content.
    const fieldLayout = () => field.evaluate(el => {
      const { x, y, width, height } = el.getBoundingClientRect()
      let scroller = el.parentElement
      while (scroller && getComputedStyle(scroller).overflowY !== 'auto') scroller = scroller.parentElement
      return { x, y: y + (scroller?.scrollTop ?? 0), width, height }
    })
    const fieldBefore = await fieldLayout()
    state.delayApply()
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    expect(await nav.boundingBox()).toEqual(navBefore)
    expect(await fieldLayout()).toEqual(fieldBefore)
    await expect(page.getByText('Updating connection settings…', { exact: true })).toHaveCount(0)
    state.releaseApply()
    await expect(page.getByText('Connection checked', { exact: true })).toBeVisible()
    expect(await nav.boundingBox()).toEqual(navBefore)
    expect(await fieldLayout()).toEqual(fieldBefore)
    const save = await page.getByRole('button', { name: 'Save changes', exact: true }).boundingBox()
    const feedback = await page.getByText('Connection checked', { exact: true }).boundingBox()
    expect(feedback!.x).toBeGreaterThan(save!.x + save!.width)
    expect(Math.abs(feedback!.y + feedback!.height / 2 - save!.y - save!.height / 2)).toBeLessThan(2)
  })

  test(`${integration.tab}: hides copy when no other cluster has this integration`, async ({ page }) => {
    const state = await fixture(page)
    const otherKind = integration.kind === 'metrics' ? 'cost' : 'metrics'
    state.connections.push({ url: 'https://other.example', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
      binding: 'other', integration: otherKind, context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: '1' })
    await openSettings(page, integration.tab)
    await expect(page.getByRole('button', { name: 'Copy from another cluster…', exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  })

  test(`${integration.tab}: copy stages an editable draft and saves an independent source snapshot`, async ({ page }) => {
    const state = await fixture(page)
    state.profiles[integration.kind].secretSet = true
    if (integration.kind === 'cost') state.profiles.cost.clusterId = 'destination-cluster'
    state.connections.push({
      url: 'https://source.example', headerKeys: ['Authorization', 'X-Scope-OrgID'], envHeaderKeys: [], secretSet: true, insecureTls: false,
      binding: 'source-binding', integration: integration.kind, context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: 'source-snapshot'
    })
    await openSettings(page, integration.tab)
    const field = page.getByRole('textbox', { name: integration.field, exact: true })
    const fieldNode = await field.elementHandle()
    await page.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
    await expect(field).toBeVisible()
    expect(await fieldNode!.evaluate(el => el.isConnected)).toBe(true)
    await page.getByRole('option', { name: /staging https:\/\/source.example/ }).click()
    await expect(field).toHaveValue('https://source.example')
    await expect(page.getByText(/Copied from staging · Unsaved/)).toBeVisible()
    await expect(page.getByRole('listbox')).toHaveCount(0)
    expect(state.writes).toHaveLength(0)
    await field.fill('https://source.example/destination')
    if (integration.kind === 'cost') {
      await expect(page.getByRole('textbox', { name: 'Kubecost cluster ID' })).toHaveValue('destination-cluster')
    }
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    expect(state.writes).toHaveLength(0)
    await page.getByRole('button', { name: 'Replace connection', exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    expect(state.writes[0]).toMatchObject({ action: 'copy', binding: 'source-binding', sourceRevision: 'source-snapshot', revision: '1', confirmRemoval: true })
    expect(state.writes[0].url).toBe('https://source.example/destination')
    if (integration.kind === 'metrics') expect(state.writes[0].headers).toEqual([])
    else expect(state.writes[0].secret).toEqual({ action: 'keep' })
    if (integration.kind === 'cost') expect(state.writes[0].clusterId).toBe('destination-cluster')
    await expect(page.getByRole('textbox', { name: integration.field, exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Edit shared connection' })).toHaveCount(0)
  })
}

test('connection warning sits directly below Save, not below a blank feedback row', async ({ page }) => {
  const state = await fixture(page)
  state.failConnectionCheck()
  await page.setViewportSize({ width: 1440, height: 1080 })
  await openSettings(page, 'Metrics')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example/unreachable')
  const save = page.getByRole('button', { name: 'Save changes', exact: true })
  await save.click()
  const warning = page.getByRole('status').filter({ hasText: 'Saved, but the metrics backend could not be reached.' })
  await expect(warning).toBeVisible()
  const textTop = await warning.evaluate(el => {
    const range = document.createRange()
    range.selectNodeContents(el)
    return range.getBoundingClientRect().top
  })
  const button = await save.boundingBox()
  expect(textTop - button!.y - button!.height).toBeGreaterThanOrEqual(0)
  expect(textTop - button!.y - button!.height).toBeLessThanOrEqual(12)
})

for (const integration of integrations) {
  test(`${integration.tab}: copied draft is discarded without saving and picker keeps form visible`, async ({ page }) => {
    const state = await fixture(page)
    state.connections.push({ url: 'https://other.example', headerKeys: ['X-Scope-OrgID'], envHeaderKeys: [], secretSet: true, insecureTls: true,
      binding: 'other', integration: integration.kind, context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: '1' })
    const dialog = await openSettings(page, integration.tab)
    const field = dialog.getByRole('textbox', { name: integration.field, exact: true })
    const copy = dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true })
    const discovery = dialog.getByRole('button', { name: 'Use auto-discovery', exact: true })
    const fieldBox = (await field.boundingBox())!
    const copyBox = (await copy.boundingBox())!
    const discoveryBox = (await discovery.boundingBox())!
    expect(copyBox.y).toBeGreaterThanOrEqual(fieldBox.y + fieldBox.height)
    expect(Math.abs(copyBox.y - discoveryBox.y)).toBeLessThan(3)
    expect(copyBox.width).toBeLessThan(240)
    await expect(copy).toHaveCSS('border-top-width', '0px')
    await expect(copy).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    await copy.click()
    await expect(field).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(copy).toBeFocused()
    await expect(dialog).toBeVisible()
    await copy.click()
    await dialog.getByRole('combobox', { name: 'Search clusters or URLs' }).fill('other.example')
    await dialog.getByRole('option', { name: /staging/ }).click()
    await expect(field).toHaveValue('https://other.example')
    await expect(field).toBeFocused()
    await expect(copy).toHaveText('Copy from another cluster…')
    await expect(copy).toBeEnabled()
    await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    if (integration.kind === 'metrics') {
      await expect(dialog.getByRole('textbox', { name: 'X-Scope-OrgID value', exact: true })).toHaveValue('')
      await expect(dialog.getByRole('textbox', { name: 'Authorization value', exact: true })).toHaveCount(0)
      await dialog.getByRole('textbox', { name: 'X-Scope-OrgID value', exact: true }).fill('edited-copy')
    }
    await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example`)
    await expect(dialog.getByText(/Copied from/)).toHaveCount(0)
    await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
    if (integration.kind === 'metrics') {
      await expect(dialog.getByRole('textbox', { name: 'Authorization value', exact: true })).toHaveValue('')
      await expect(dialog.getByRole('textbox', { name: 'X-Scope-OrgID value', exact: true })).toHaveCount(0)
    }
    expect(state.writes).toHaveLength(0)
  })
}

test('copy picker distinguishes duplicate context names and supports empty search', async ({ page }) => {
  const state = await fixture(page)
  for (const source of ['one', 'two']) state.connections.push({ url: `https://${source}.example`, headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
    binding: source, integration: 'metrics', context: 'staging', source: `/test/${source}`, inFileName: 'staging', availability: 'available', revision: '1' })
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
  const search = dialog.getByRole('combobox', { name: 'Search clusters or URLs' })
  await search.fill('no such cluster')
  await expect(dialog.getByText('No matches.', { exact: true }).last()).toBeVisible()
  await search.fill('/test/two')
  await expect(dialog.getByRole('option')).toHaveCount(1)
  await search.press('Enter')
  await expect(dialog.getByRole('textbox', { name: 'Metrics backend URL', exact: true })).toHaveValue('https://two.example')
  expect(state.writes).toHaveLength(0)
})

test('copy into an unconfigured cluster saves edited headers without an intermediate write', async ({ page }) => {
  const state = await fixture(page)
  Object.assign(state.profiles.metrics, { url: '', headerKeys: [], state: 'auto' })
  state.connections.push({ url: 'https://source.example', headerKeys: ['Authorization', 'X-Scope-OrgID'], envHeaderKeys: [], secretSet: false, insecureTls: false,
    binding: 'source', integration: 'metrics', context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: 'source-revision' })
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
  await dialog.getByRole('option', { name: /staging/ }).click()
  await dialog.getByRole('textbox', { name: 'X-Scope-OrgID value', exact: true }).fill('new-tenant')
  await dialog.getByRole('textbox', { name: 'Metrics backend URL', exact: true }).fill('https://source.example/query')
  expect(state.writes).toHaveLength(0)
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ action: 'copy', url: 'https://source.example/query', sourceRevision: 'source-revision', headers: [{ key: 'Authorization', action: 'keep' }, { key: 'X-Scope-OrgID', action: 'set', value: 'new-tenant' }] })
  await expect(dialog.getByText(/Copied from/)).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
})

test('new header names stay editable and duplicates fail before sending', async ({ page }) => {
  const state = await fixture(page)
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Add header', exact: true }).click()
  const name = dialog.getByRole('textbox', { name: 'Header 2 name', exact: true })
  await name.fill('authorization')
  await expect(name).toBeEnabled()
  await dialog.getByRole('textbox', { name: 'authorization value', exact: true }).fill('replacement')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Header names must be unique')
  expect(state.writes).toHaveLength(0)
  await name.fill('X-Tenant')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0].headers).toEqual([{ key: 'Authorization', action: 'keep' }, { key: 'X-Tenant', action: 'set', value: 'replacement' }])
})

test('blank new header rows do not block a URL-only save or clear saved headers', async ({ page }) => {
  const state = await fixture(page)
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Add header', exact: true }).click()
  await dialog.getByRole('textbox', { name: 'Metrics backend URL', exact: true }).fill('https://metrics.example/query')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0].headers).toEqual([{ key: 'Authorization', action: 'keep' }])
})

test('a named new header needs a value before saving', async ({ page }) => {
  const state = await fixture(page)
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Add header', exact: true }).click()
  await dialog.getByRole('textbox', { name: 'Header 2 name', exact: true }).fill('X-Tenant')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Enter a value for X-Tenant.')
  expect(state.writes).toHaveLength(0)
})

test('Cost explains query failures beside the connection status', async ({ page }) => {
  await fixture(page)
  await page.route('**/api/opencost/summary', route => route.fulfill({ json: { available: false, source: 'kubecost', reason: 'query_error' } }))
  const dialog = await openSettings(page, 'Cost')
  await expect(dialog.getByRole('group', { name: 'Current connection', exact: true })).toContainText('The active cost source query failed.')
})

for (const integration of integrations) {
  test(`${integration.tab}: keyboard focus returns to settings after a plain save`, async ({ page }) => {
    const state = await fixture(page)
    state.delayApply()
    const dialog = await openSettings(page, integration.tab)
    await dialog.getByRole('textbox', { name: integration.field, exact: true }).fill(`https://${integration.kind}.example/query`)
    const save = dialog.getByRole('button', { name: 'Save changes', exact: true })
    await save.focus()
    await page.keyboard.press('Enter')
    await expect.poll(() => state.writes.length).toBe(1)
    state.releaseApply()
    await expect(save).toBeDisabled()
    await expect.poll(() => dialog.evaluate(el => el.contains(document.activeElement))).toBe(true)
  })
}

test('auto-discovery replaces a copied draft and Discard restores the saved connection', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push({ url: 'https://source.example', headerKeys: ['X-Scope-OrgID'], envHeaderKeys: [], secretSet: false, insecureTls: false,
    binding: 'source', integration: 'metrics', context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: '1' })
  const dialog = await openSettings(page, 'Metrics')
  await dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
  await dialog.getByRole('option', { name: /staging/ }).click()
  await dialog.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
  await expect(dialog.getByText(/Copied from/)).toHaveCount(0)
  await expect(dialog.getByRole('textbox', { name: 'Metrics backend URL', exact: true })).toHaveValue('')
  await expect(dialog.getByRole('textbox', { name: 'X-Scope-OrgID value', exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
  await expect(dialog.getByRole('textbox', { name: 'Metrics backend URL', exact: true })).toHaveValue('https://metrics.example')
  expect(state.writes).toHaveLength(0)
})

for (const integration of integrations) {
  test(`${integration.tab}: copy replaces edited drafts and allows choosing another source`, async ({ page }) => {
    const state = await fixture(page)
    for (const context of ['staging', 'testing']) state.connections.push({
      url: `https://${context}.example`,
      headerKeys: integration.kind === 'metrics' ? ['Authorization'] : [], envHeaderKeys: [], secretSet: integration.kind !== 'metrics', insecureTls: false,
      binding: context, integration: integration.kind, context, source: `/test/${context}`, inFileName: context, availability: 'available', revision: '1',
    })
    const dialog = await openSettings(page, integration.tab)
    const field = dialog.getByRole('textbox', { name: integration.field, exact: true })
    const credential = dialog.getByRole('textbox', { name: integration.kind === 'metrics' ? 'Authorization value' : integration.kind === 'argocd' ? 'API token' : 'API key', exact: true })
    const copy = dialog.getByRole('button', { name: 'Copy from another cluster…', exact: true })
    for (const context of ['staging', 'testing']) {
      await field.fill('https://unsaved.example')
      await credential.fill('unsaved-secret')
      await copy.click()
      await dialog.getByRole('option', { name: new RegExp(context) }).click()
      await expect(field).toHaveValue(`https://${context}.example`)
      await expect(credential).toHaveValue('')
      await expect(copy).toBeEnabled()
      await expect(dialog.getByText(`Copied from ${context}`, { exact: false })).toBeVisible()
      expect(state.writes).toHaveLength(0)
    }
    await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example`)
    await expect(credential).toHaveValue('')
    expect(state.writes).toHaveLength(0)
  })
}

test('copy after a target change warns before replacement and reload clears stale selection', async ({ page }) => {
  const state = await fixture(page)
  state.profiles.metrics.state = 'target_changed'
  state.profiles.metrics.secretSet = true
  state.profiles.metrics.error = 'Cluster connection changed'
  state.connections.push({
    url: 'https://source.example', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
    binding: 'source', integration: 'metrics', context: 'staging', source: '/test/staging', inFileName: 'staging', availability: 'available', revision: 'old-source'
  })
  state.failNextCopy()
  await openSettings(page, 'Metrics')
  await page.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
  await page.getByRole('option', { name: /staging https:\/\/source.example/ }).click()
  await expect(page.getByRole('textbox', { name: 'Metrics backend URL' })).toHaveValue('https://source.example')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await page.getByRole('button', { name: 'Replace connection', exact: true }).click()
  await expect(page.getByText(/Source settings changed/)).toBeVisible()
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.getByRole('button', { name: 'Discard draft & reload', exact: true }).click()
  await page.getByRole('button', { name: 'Copy from another cluster…', exact: true }).click()
  await expect(page.getByText(/Copied from staging/)).toHaveCount(0)
  await page.getByRole('option', { name: /staging https:\/\/source.example/ }).click()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await page.getByRole('button', { name: 'Replace connection', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1].sourceRevision).toBe('refreshed-source')
  expect(state.writes[1].confirmRemoval).toBe(true)
})

async function openSettings(page: Page, tab: string) {
  await page.goto('/')
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await page.getByRole('tab', { name: tab, exact: true }).click()
  return page.getByRole('dialog', { name: 'Settings', exact: true })
}

test('headers edit directly, preserve untouched values, and support undo and discard', async ({ page }) => {
  const state = await fixture(page)
  state.profiles.metrics.headerKeys = ['Authorization', 'X-Scope-OrgID']
  await openSettings(page, 'Metrics')
  const secret = page.getByRole('textbox', { name: 'Authorization value', exact: true })
  const save = page.getByRole('button', { name: 'Save changes', exact: true })
  await expect(secret).toHaveAttribute('placeholder', 'Saved value')
  await expect(save).toBeDisabled()
  await secret.fill('replacement')
  await expect(save).toBeEnabled()
  await secret.fill('')
  await expect(save).toBeDisabled()
  await page.getByRole('button', { name: 'Remove Authorization', exact: true }).click()
  await expect(secret).toHaveAttribute('placeholder', 'Will be removed')
  await page.getByRole('button', { name: 'Undo removal of Authorization', exact: true }).click()
  await expect(save).toBeDisabled()
  await secret.fill('discard-this')
  await page.getByRole('button', { name: 'Discard', exact: true }).click()
  await expect(secret).toHaveValue('')
  await expect(save).toBeDisabled()
  await secret.fill('replacement')
  await save.click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0].headers).toEqual([{ key: 'Authorization', action: 'set', value: 'replacement' }, { key: 'X-Scope-OrgID', action: 'keep' }])
  await expect(secret).toHaveValue('')
  await expect(save).toBeDisabled()
})

test('old context cleanup lives in Connection and clearly scopes credential removal', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push({ url: 'https://old.example', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
    binding: 'old', integration: 'metrics', context: 'old-cluster', source: '/test/old', inFileName: 'old-cluster', availability: 'removed', revision: 'old-revision' })
  await openSettings(page, 'Metrics')
  await expect(page.getByRole('button', { name: 'Manage stored cluster settings' })).toHaveCount(0)
  await expect(page.getByText('Local storage details', { exact: true })).toHaveCount(0)
  await page.getByRole('tab', { name: 'Connection', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Saved connections', exact: true })).toBeVisible()
  await expect(page.getByText('Not in kubeconfig', { exact: true })).toBeVisible()
  const remove = page.getByRole('button', { name: 'Remove saved Metrics connection for old-cluster', exact: true })
  await remove.click()
  await expect(page.getByText('Remove the saved Metrics connection and credentials for old-cluster?', { exact: false })).toBeVisible()
  await expect(page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Remove saved connection?' }) })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(remove).toBeFocused()
  await remove.click()
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(remove).toBeFocused()
  await expect(page.getByRole('dialog', { name: 'Settings', exact: true })).toBeVisible()
  expect(state.writes).toHaveLength(0)
})

test('Connection groups stale integrations by context and removes only the confirmed entry without losing drafts', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push(...(['metrics', 'argocd', 'cost'] as const).map(integration => ({ ...discoverySettings, binding: 'old', integration, context: 'old-cluster', source: '/test/old', inFileName: 'old-cluster', availability: 'removed' as const, revision: `old-${integration}` })))
  await openSettings(page, 'Metrics')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example/draft')
  await page.getByRole('tab', { name: 'Connection', exact: true }).click()
  await expect(page.getByText('old-cluster', { exact: true })).toHaveCount(1)
  await page.getByRole('button', { name: 'Remove saved Cost connection for old-cluster', exact: true }).click()
  await page.getByRole('button', { name: 'Remove connection', exact: true }).click()
  await expect(page.getByText('Removed the saved Cost connection for old-cluster.', { exact: true })).toBeVisible()
  expect(state.writes).toEqual([{ action: 'forget', kind: 'cost', binding: 'old', sourceRevision: 'old-cost', confirmRemoval: true }])
  await expect(page.getByRole('button', { name: 'Remove saved Cost connection for old-cluster', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Remove saved Argo CD connection for old-cluster', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Metrics backend URL' })).toHaveValue('https://metrics.example/draft')
})

test('Connection preserves the removed-versus-unavailable distinction and cannot remove the active entry', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push(
    { ...discoverySettings, binding: 'development', integration: 'metrics', context: 'development', source: '/test/kubeconfig', inFileName: 'development', availability: 'unavailable', revision: 'active' },
    { ...discoverySettings, binding: 'missing-file', integration: 'cost', context: 'staging', source: '/test/offline', inFileName: 'staging', availability: 'unavailable', revision: 'offline' },
  )
  await openSettings(page, 'Overview')
  await expect(page.getByRole('button', { name: 'Configuration files', exact: true })).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('heading', { name: 'Saved connections', exact: true })).not.toBeVisible()
  await page.getByRole('button', { name: 'Configuration files', exact: true }).click()
  await expect(page.getByText('config.json', { exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'Connection', exact: true }).click()
  await expect(page.getByText('Not loaded in this session', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Remove saved Metrics connection for development', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Remove saved Cost connection for staging', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Remove saved Cost connection for staging', exact: true }).click()
  await expect(page.getByText(/This entry may still exist in another kubeconfig/)).toBeVisible()
})

test('saved settings load failure offers recovery rather than an empty-state claim', async ({ page }) => {
  await fixture(page)
  let fail = true
  await page.route('**/api/integrations/connections', route => fail ? route.fulfill({ status: 500, json: { error: 'Could not read saved settings.' } }) : route.fallback())
  await openSettings(page, 'Connection')
  await expect(page.getByRole('alert')).toContainText('Could not read saved settings.')
  await expect(page.getByText('No saved connections yet.', { exact: true })).toHaveCount(0)
  fail = false
  await page.getByRole('button', { name: 'Reload saved connections', exact: true }).click()
  await expect(page.getByText('No saved connections yet.', { exact: true })).toBeVisible()
})

test('cleanup blocks close while committing and keeps remaining cards mounted without a refetch', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push(...(['metrics', 'cost'] as const).map(integration => ({ ...discoverySettings, binding: 'old', integration, context: 'old-cluster', source: '/test/old', inFileName: 'old-cluster', availability: 'removed' as const, revision: `old-${integration}` })))
  await openSettings(page, 'Connection')
  const remaining = page.getByRole('button', { name: 'Remove saved Cost connection for old-cluster', exact: true })
  const node = await remaining.elementHandle()
  await page.getByRole('button', { name: 'Remove saved Metrics connection for old-cluster', exact: true }).click()
  const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Remove saved connection?', exact: true }) })
  state.delayApply()
  await confirmation.getByRole('button', { name: 'Remove connection', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  await expect(page.getByRole('button', { name: 'Close settings', exact: true })).toBeDisabled()
  await expect(confirmation.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(confirmation).toBeVisible()
  // Hold catalog refreshes so a redundant reload cannot hide the row unnoticed.
  await page.route('**/api/integrations/connections', async route => { if (route.request().method() === 'GET') return; await route.fallback() })
  state.releaseApply()
  await expect(page.getByText('Removed the saved Metrics connection for old-cluster.', { exact: true })).toBeVisible()
  await expect(remaining).toBeVisible()
  expect(await node!.evaluate(el => el.isConnected)).toBe(true)
  await expect(page.getByRole('heading', { name: 'Saved connections', exact: true })).toBeFocused()
  await expect(page.getByRole('button', { name: 'Close settings', exact: true })).toBeEnabled()
})

test('stale cleanup revisions require reload and keep keyboard focus in the confirmation', async ({ page }) => {
  const state = await fixture(page)
  state.connections.push({ ...discoverySettings, binding: 'old', integration: 'metrics', context: 'old-cluster', source: '/test/old', inFileName: 'old-cluster', availability: 'removed', revision: 'old-revision' })
  await page.route('**/api/integrations/connections', route => route.request().method() === 'PUT'
    ? route.fulfill({ status: 409, json: { error: 'Settings changed; reload latest settings.' } })
    : route.fallback())
  await openSettings(page, 'Connection')
  await page.getByRole('button', { name: 'Remove saved Metrics connection for old-cluster', exact: true }).click()
  const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Remove saved connection?', exact: true }) })
  await confirmation.getByRole('button', { name: 'Remove connection', exact: true }).click()
  await expect(confirmation.getByRole('alert')).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect.poll(() => confirmation.evaluate(el => el.contains(document.activeElement))).toBe(true)
  await expect(confirmation.getByRole('button', { name: 'Remove connection', exact: true })).toBeDisabled()
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.getByRole('button', { name: 'Reload saved connections', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Remove saved Metrics connection for old-cluster', exact: true })).toBeVisible()
})

for (const management of ['operator', 'cloud']) {
  test(`${management} settings do not show local files or saved-cluster cleanup`, async ({ page }) => {
    await fixture(page)
    await page.route('**/api/config', route => route.fulfill({ json: { management, file: {}, effective: {}, isDesktop: false } }))
    await openSettings(page, 'Overview')
    await expect(page.getByRole('button', { name: 'Configuration files', exact: true })).toHaveCount(0)
    await page.getByRole('tab', { name: 'Connection', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Saved connections', exact: true })).toHaveCount(0)
    if (management === 'operator') await expect(page.getByText(/Helm/).first()).toBeVisible()
  })
}

for (const integration of integrations) {
  test(`${integration.tab}: discovery is a discardable draft and only commits through Save changes`, async ({ page }) => {
    const state = await fixture(page)
    const settings = await openSettings(page, integration.tab)
    const trigger = settings.getByRole('button', { name: 'Use auto-discovery', exact: true })
    const field = settings.getByRole('textbox', { name: integration.field, exact: true })
    const fieldBox = await field.boundingBox()
    const triggerBox = await trigger.boundingBox()
    expect(Math.abs(triggerBox!.y - fieldBox!.y)).toBeLessThan(70)
    await trigger.click()
    const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Use auto-discovery?', exact: true }) })
    const save = settings.getByRole('button', { name: 'Save changes', exact: true })
    await expect(confirmation).toBeHidden()
    await expect(field).toHaveValue('')
    await expect(trigger).toBeDisabled()
    await expect(save).toBeEnabled()
    expect(state.writes).toHaveLength(0)
    await settings.getByRole('button', { name: 'Discard', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example`)
    if (integration.kind === 'metrics') await expect(page.getByRole('textbox', { name: 'Authorization value', exact: true })).toHaveAttribute('placeholder', 'Saved value')
    else await expect(page.getByRole('textbox', { name: integration.kind === 'argocd' ? 'API token' : 'API key', exact: true })).toHaveAttribute('placeholder', 'Saved value')
    await expect(save).toBeDisabled()
    await trigger.click()
    await save.click()
    await expect(confirmation).toBeVisible()
    await expect(field).toHaveValue('')
    await expect(page.getByRole('button', { name: /^Back to/ })).toHaveCount(0)
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(save).toBeFocused()
    expect(state.writes).toHaveLength(0)
    await save.click()
    await confirmation.getByRole('button', { name: 'Save & use auto-discovery', exact: true }).focus()
    await page.keyboard.press('Tab')
    await expect.poll(() => confirmation.evaluate(el => el.contains(document.activeElement))).toBe(true)
    await page.keyboard.press('Escape')
    await expect(save).toBeFocused()
    await expect(settings).toBeVisible()
    await save.click()
    state.delayApply()
    await confirmation.getByRole('button', { name: 'Save & use auto-discovery', exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    await expect(confirmation.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
    await page.keyboard.press('Escape')
    await expect(confirmation).toBeVisible()
    state.releaseApply()
    await expect(confirmation).toBeHidden()
    await expect(field).toHaveValue('')
    await expect(trigger).toBeDisabled()
    await expect(save).toBeDisabled()
    expect(state.writes[0]).toMatchObject({ action: 'auto', confirmRemoval: true, kind: integration.kind })
    await expect(settings).toBeVisible()
  })
}

test('cancelling replacement preserves URL and credential drafts, with no fake error', async ({ page }) => {
  const state = await fixture(page)

  await openSettings(page, 'Metrics')
  await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
  await page.getByRole('button', { name: 'Add header', exact: true }).click()
  await page.getByRole('textbox', { name: 'Header 1 name', exact: true }).fill('Authorization')
  await page.getByRole('textbox', { name: 'Authorization value', exact: true }).fill('draft-secret')
  await page.getByRole('textbox', { name: 'Metrics backend URL', exact: true }).fill('https://new.example')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Replace saved connection?', exact: true }) })
  await expect(confirmation).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Authorization value', exact: true })).toHaveValue('draft-secret')
  await expect(page.getByRole('textbox', { name: 'Metrics backend URL', exact: true })).toHaveValue('https://new.example')
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
  expect(state.writes).toHaveLength(0)
})

test('failed confirmation keeps keyboard focus and exposes stale-settings recovery after cancel', async ({ page }) => {
  const state = await fixture(page)
  await openSettings(page, 'Metrics')
  await page.route('**/api/integrations/connections', async route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    await route.fulfill({ status: 409, json: { error: 'Settings changed; reload latest settings.' } })
  })
  await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Use auto-discovery?', exact: true }) })
  await confirmation.getByRole('button', { name: 'Save & use auto-discovery', exact: true }).click()
  await expect(confirmation.getByRole('alert')).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect.poll(() => confirmation.evaluate(el => el.contains(document.activeElement))).toBe(true)
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Discard draft & reload', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Discard draft & reload', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  expect(state.writes).toHaveLength(0)
})

for (const integration of integrations.filter(item => item.kind !== 'metrics')) {
  test(`${integration.tab}: replacement cancellation preserves the typed credential`, async ({ page }) => {
    const state = await fixture(page)

    await openSettings(page, integration.tab)
    const credential = page.getByRole('textbox', { name: integration.kind === 'argocd' ? 'API token' : 'API key', exact: true })
    await credential.fill('draft-secret')
    await page.getByRole('textbox', { name: integration.field, exact: true }).fill('')
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Replace saved connection?', exact: true }) })
    await expect(confirmation).toBeVisible()
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(credential).toHaveValue('draft-secret')
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    expect(state.writes).toHaveLength(0)
  })
}

for (const [tab, field] of [['Argo CD', 'API token'], ['Cost', 'API key']]) {
  test(`${tab}: erasing a new credential restores a clean draft`, async ({ page }) => {
    const state = await fixture(page)
    state.profiles.argocd.secretSet = false
    state.profiles.cost.secretSet = false
    await openSettings(page, tab)
    const input = page.getByRole('textbox', { name: field, exact: true })
    await input.fill('temporary')
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    await input.fill('')
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
    expect(state.writes).toHaveLength(0)
  })
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
    await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
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
    await expect(dialog.getByRole('button', { name: 'Discard changes', exact: true })).toBeHidden()
    await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
    await expect(field).toHaveValue(`https://${integration.kind}.example/applied`)
    await expect(page.getByText('Connection checked', { exact: true })).toBeHidden()
    await expect(page.getByText('Saved', { exact: true })).toBeHidden()
    expect(state.writes).toHaveLength(1)
  })
}

test('discard clears drafts in all integration editors, including credentials and mapping', async ({ page }) => {
  const state = await fixture(page)
  const dialog = await openSettings(page, 'Metrics')
  await page.getByRole('textbox', { name: 'Authorization value' }).fill('draft-secret')
  await page.getByRole('tab', { name: 'Argo CD', exact: true }).click()
  await page.getByRole('textbox', { name: 'API token', exact: true }).fill('draft-token')
  await page.getByRole('tab', { name: 'Cost', exact: true }).click()
  await page.getByRole('textbox', { name: 'API key', exact: true }).fill('draft-key')
  await page.getByRole('button', { name: 'Cluster mapping', exact: true }).click()
  await page.getByRole('textbox', { name: 'Kubecost cluster ID', exact: true }).fill('draft-cluster')
  await dialog.getByRole('button', { name: 'Discard all changes', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'API key', exact: true })).toHaveValue('')
  await page.getByRole('button', { name: 'Cluster mapping', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Kubecost cluster ID', exact: true })).toHaveValue('')
  await page.getByRole('tab', { name: 'Argo CD', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'API token', exact: true })).toHaveValue('')
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Authorization value' })).toHaveValue('')
  await expect(page.getByRole('textbox', { name: 'Authorization value' })).toHaveAttribute('placeholder', 'Saved value')
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
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Metrics backend URL' })).toHaveValue('https://metrics.example/draft')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByRole('tabpanel').getByText('Connection checked', { exact: true })).toBeVisible()
  expect(state.writes.map(write => write.kind)).toEqual(['argocd', 'metrics'])
})

test('an in-flight apply cannot be discarded or closed as though it were cancelled', async ({ page }) => {
  const state = await fixture(page)
  state.delayApply()
  const dialog = await openSettings(page, 'Argo CD')
  await page.getByRole('textbox', { name: 'Argo CD server URL' }).fill('https://argocd.example/applied')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  await expect(dialog.getByRole('button', { name: 'Discard', exact: true })).toBeDisabled()
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

test('unchanged settings cannot be saved, and a credential edit shows one success result', async ({ page }) => {
  const state = await fixture(page)
  state.preserveRevision()
  await openSettings(page, 'Metrics')
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  await page.getByRole('textbox', { name: 'Authorization value' }).fill('replacement')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
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

test('Cost source changes confirm removal only when a saved connection exists', async ({ page }) => {
  const state = await fixture(page)
  await openSettings(page, 'Cost')
  await page.getByRole('textbox', { name: 'Kubecost Aggregator URL', exact: true }).fill('')
  await page.getByRole('combobox', { name: 'Cost source', exact: true }).selectOption('prometheus')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const confirmation = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Use metrics for cost data?', exact: true }) })
  await confirmation.getByRole('button', { name: 'Use metrics connection', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ action: 'auto', mode: 'prometheus', confirmRemoval: true })
  await expect(confirmation).toBeHidden()
  await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
  await expect(page.getByText('Saving clears the saved credentials for this cluster.', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1]).toMatchObject({ action: 'auto' })
  expect(state.writes[1].confirmRemoval).toBeUndefined()
  await expect(page.getByRole('heading', { name: 'Use auto-discovery?', exact: true })).toHaveCount(0)
})

for (const integration of integrations) {
  for (const credentials of [false, true]) {
    test(`${integration.tab}: discovery feedback only warns about saved credentials (${credentials})`, async ({ page }) => {
      const state = await fixture(page)
      state.profiles[integration.kind].secretSet = credentials && integration.kind !== 'metrics'
      state.profiles[integration.kind].headerKeys = credentials && integration.kind === 'metrics' ? ['Authorization'] : []
      const dialog = await openSettings(page, integration.tab)
      await dialog.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
      const warning = dialog.getByText('Saving clears the saved credentials for this cluster.', { exact: true })
      if (credentials) await expect(warning).toBeVisible()
      else await expect(warning).toHaveCount(0)
      await expect(dialog.getByText(/Auto-discovery selected/)).toHaveCount(0)
      await dialog.getByRole('button', { name: 'Discard', exact: true }).click()
      await expect(warning).toBeHidden()
      expect(state.writes).toHaveLength(0)
    })
  }
}

test('resolved status detail does not reopen an empty row on the next save', async ({ page }) => {
  const state = await fixture(page)
  state.failStatus()
  await openSettings(page, 'Metrics')
  const current = page.getByLabel('Current connection', { exact: true })
  await expect(current).toContainText('Backend unavailable')
  state.recoverStatus()
  const field = page.getByRole('textbox', { name: 'Metrics backend URL', exact: true })
  await field.fill('https://metrics.example/recovered')
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(current).toContainText('Connected')
  const detailHeight = () => current.getByText('Backend unavailable', { exact: true }).evaluate(el => el.closest('.grid')!.getBoundingClientRect().height)
  await expect.poll(detailHeight).toBe(0)
  await field.fill('https://metrics.example/again')
  state.delayApply()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(current).toContainText('Checking…')
  await expect.poll(detailHeight).toBe(0)
  state.releaseApply()
})

test('a saved-to-discovery refresh never labels the old configured URL as discovered', async ({ page }) => {
  await fixture(page)
  await openSettings(page, 'Metrics')
  const current = page.getByLabel('Current connection', { exact: true })
  await expect(current).toContainText('Connected')
  let release: (() => void) | undefined
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/prometheus/status', async route => {
    await held
    await route.fulfill({ json: { connected: true, discovering: false, address: 'http://discovered.example' } })
  })
  await page.getByRole('button', { name: 'Use auto-discovery', exact: true }).click()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await page.getByRole('button', { name: 'Save & use auto-discovery', exact: true }).click()
  await expect(current).toContainText('Checking…')
  await expect(current).not.toContainText('Discovered: https://metrics.example')
  await expect(current.getByText('Connected', { exact: true })).toHaveCount(0)
  release?.()
  await expect(current).toContainText('Discovered: http://discovered.example')
})

test('current connection does not describe a draft URL, and refreshes after apply', async ({ page }) => {
  const state = await fixture(page)
  await openSettings(page, 'Metrics')
  const current = page.getByLabel('Current connection', { exact: true })
  await expect(current).toContainText('Connected')
  await expect(current).not.toContainText('https://metrics.example')
  await page.getByRole('textbox', { name: 'Metrics backend URL' }).fill('https://metrics.example/new')
  await expect(current).not.toContainText('https://metrics.example/new')
  state.failStatus()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(current).toContainText('Not connected')
  await expect(current).toContainText('Backend unavailable')
  await expect(current).not.toContainText('https://metrics.example')
})
