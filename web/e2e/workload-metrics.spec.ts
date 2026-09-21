import { test, expect } from '@playwright/test'

const routes = [
  '/workload/Deployment/default/nginx?tab=metrics',
  '/applications?app=default/Deployment/nginx&workload=Deployment/default/nginx&tab=metrics',
]

test.beforeEach(async ({ page }) => {
  await page.route('**/api/prometheus/status', (route) => route.fulfill({ json: { available: true, connected: true } }))
  await page.route('**/api/prometheus/workload/**', (route) => {
    const panel = { state: 'available', unit: 'cores', series: [{ labels: { pod: 'nginx-abc-111' }, dataPoints: [{ timestamp: 160, value: 0.1 }] }] }
    return route.fulfill({ json: {
      state: 'available', sources: [], pods: 2, podsTotal: 2,
      start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
      history: { cpu: { mode: 'current-pods' } }, comparison: { cpu: panel }, panels: { cpu: panel },
    } })
  })
})

for (const path of routes) {
  test(`time window survives navigation and reload: ${path}`, async ({ page }) => {
    await page.goto(`${path}&metricsRange=invalid&search=keep-me`)
    const range = page.getByRole('combobox', { name: 'Metrics time range' })
    await expect(range).toHaveValue('1h')
    const historyLength = await page.evaluate(() => history.length)
    await range.selectOption('3h')
    await expect(page).toHaveURL(/metricsRange=3h/)
    expect(await page.evaluate(() => history.length)).toBe(historyLength)
    expect(new URL(page.url()).searchParams.get('search')).toBe('keep-me')
    const selectedURL = page.url()
    await page.getByRole('link', { name: 'nginx-abc-111', exact: true }).click()
    await expect(page).toHaveURL(/resource=default%2Fnginx-abc-111/)
    await page.goBack()
    await expect(page).toHaveURL(selectedURL)
    await expect(range).toHaveValue('3h')
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await page.getByRole('tab', { name: 'Metrics', exact: true }).click()
    await expect(range).toHaveValue('3h')
    await page.reload()
    await expect(range).toHaveValue('3h')
    expect(new URL(page.url()).searchParams.has('metricsSource')).toBe(false)
  })
}

test('disconnected setup opens the existing Metrics settings', async ({ page }) => {
  await page.route('**/api/prometheus/status', (route) => route.fulfill({ json: { available: false, connected: false, error: 'Backend query failed' } }))
  await page.route('**/api/prometheus/connect', (route) => route.fulfill({ status: 503, json: { error: 'Backend query failed' } }))
  await page.goto(routes[0])
  await expect(page.getByText('Backend query failed', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Configure metrics', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Metrics', exact: true })).toBeVisible()
})

test('opening the full dashboard preserves the drawer time window', async ({ page }) => {
  await page.goto('/resources/deployments?resource=default%2Fnginx')
  const drawerRange = page.locator('select').filter({ has: page.locator('option[value="6h"]') })
  await drawerRange.selectOption('6h')
  await page.getByRole('button', { name: 'Open request and resource dashboard →', exact: true }).click()
  await expect(page).toHaveURL(/metricsRange=6h/)
  await expect(page.getByRole('combobox', { name: 'Metrics time range' })).toHaveValue('6h')
})

test('omitted evaluations break the workload line and do not borrow hover values', async ({ page }) => {
  const start = 1789374210
  await page.route('**/api/prometheus/workload/**', (route) => route.fulfill({ json: {
    state: 'available', sources: [], pods: 1, podsTotal: 1,
    start, end: start + 120, stepSeconds: 15, rateWindowSeconds: 300,
    history: { cpu: { mode: 'workload-history' } }, comparison: {},
    panels: { cpu: { state: 'available', unit: 'cores', series: [{
      labels: { aggregation: 'Workload' },
      dataPoints: [[0, 0], [15, 0.02], [105, 0.03], [120, 0.04]].map(([offset, value]) => ({ timestamp: start + offset, value })),
    }] } },
  } }))
  await page.goto(routes[0])
  const chart = page.locator('section.metrics-chart').filter({ has: page.getByRole('heading', { name: 'CPU usage · workload', exact: true }) })
  await expect(chart.locator('path[fill="none"]')).toHaveCount(2)
  const plot = chart.locator('svg > rect').last()
  const bounds = await plot.boundingBox()
  expect(bounds).not.toBeNull()
  const tooltip = chart.locator('div.pointer-events-none')
  await page.mouse.move(bounds!.x + bounds!.width / 2, bounds!.y + bounds!.height / 2)
  await expect(tooltip).toBeVisible()
  await expect(tooltip.getByText('Workload', { exact: true })).toHaveCount(0)
  await page.mouse.move(bounds!.x + bounds!.width / 8, bounds!.y + bounds!.height / 2)
  await expect(tooltip.getByText('Workload', { exact: true })).toBeVisible()
  await expect(tooltip.getByText('20m', { exact: true })).toBeVisible()
})

test('non-HTTP metrics keep explanations collapsed and keyboard accessible', async ({ page }) => {
  await page.goto(routes[0])
  await expect(page.getByText('No usable HTTP metrics in this window', { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'CPU usage · per Pod', exact: true })).toBeVisible()
  await expect(page.getByText('Resource charts below remain available.', { exact: true })).toHaveCount(0)
  const help = page.getByRole('button', { name: 'About these metrics' })
  const docs = page.getByRole('link', { name: 'What each chart needs', exact: true })
  await expect(docs).toBeHidden()
  await help.focus()
  await page.keyboard.press('Enter')
  await expect(docs).toBeVisible()
  const dialog = page.getByRole('dialog', { name: 'Metrics sources & coverage' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Close metrics help' })).toBeFocused()
  for (let index = 0; index < 6; index++) {
    await page.keyboard.press('Tab')
    expect(await page.evaluate(() => document.activeElement === document.body || !!document.activeElement?.closest('dialog'))).toBe(true)
  }
  await dialog.getByRole('button', { name: 'Close metrics help' }).focus()
  await page.keyboard.press('Escape')
  await expect(docs).toBeHidden()
  await expect(help).toBeFocused()
  const definition = page.getByText('Resource totals include reporting containers and sidecars.', { exact: false })
  await expect(definition).toBeVisible()
  await expect(page.getByText('Current Pods only', { exact: true })).toBeVisible()
})

test('metrics dialog groups evidence, keeps overrides optional and closes without moving charts', async ({ page }) => {
  await page.route('**/api/prometheus/workload/**', (route) => route.fulfill({ json: {
    state: 'available', sources: [], pods: 2, podsTotal: 2,
    start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
    attribution: { cpu: 'Matched to current Pod UIDs · 2 of 2 current Pods', memory: 'Matched to current Pod UIDs · 2 of 2 current Pods', throttling: 'Matched by cluster label · 1 of 2 current Pods', istio: 'No observations for current Pod names.' },
    history: { cpu: { mode: 'current-pods' } }, comparison: {},
    panels: { cpu: { state: 'available', unit: 'cores', series: [{ labels: {}, dataPoints: [{ timestamp: 160, value: 0.1 }] }] } },
  } }))
  await page.goto(routes[0])
  const chart = page.getByRole('heading', { name: 'CPU usage · per Pod', exact: true })
  const before = await chart.boundingBox()
  await page.getByRole('button', { name: 'About these metrics' }).click()
  const dialog = page.getByRole('dialog', { name: 'Metrics sources & coverage' })
  await expect(dialog.getByRole('rowheader', { name: 'CPU / Memory', exact: true })).toBeVisible()
  await expect(dialog.getByRole('rowheader', { name: 'Throttling', exact: true })).toBeVisible()
  const override = dialog.getByText('--prometheus-single-cluster', { exact: true })
  await expect(override).toBeHidden()
  await dialog.getByText('Troubleshooting identity matching', { exact: true }).click()
  await expect(override).toBeVisible()
  const codeBounds = await override.boundingBox()
  await page.mouse.move(codeBounds!.x + 5, codeBounds!.y + 5)
  await page.mouse.down()
  await page.mouse.move(5, 5, { steps: 5 })
  await page.mouse.up()
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Close metrics help' }).click()
  await expect(dialog).toHaveCount(0)
  expect(await chart.boundingBox()).toEqual(before)
  await page.getByRole('button', { name: 'About these metrics' }).click()
  await page.mouse.click(5, 5)
  await expect(dialog).toHaveCount(0)
})

test('failed history stays visible once while optional attribution stays collapsed', async ({ page }) => {
  await page.route('**/api/prometheus/workload/**', (route) => route.fulfill({ json: {
    state: 'partial', sources: [], pods: 2, podsTotal: 2,
    start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
    attribution: { cpu: 'Metrics identity proof' },
    history: { cpu: { mode: 'unavailable', reason: 'Historical ownership query failed' } }, comparison: {},
    panels: {
      cpu: { state: 'error', unit: 'cores', series: [], reason: 'Historical ownership query failed' },
      requests: { state: 'partial', unit: 'requests/s', series: [], reason: 'Ambiguous observation population' },
    },
  } }))
  await page.goto(routes[0])
  await expect(page.getByText('Historical ownership query failed', { exact: true })).toHaveCount(1)
  await expect(page.getByText('Historical ownership query failed', { exact: true })).toBeVisible()
  await expect(page.getByText('Request metrics withheld', { exact: true })).toBeVisible()
  await expect(page.getByText('No usable HTTP metrics in this window', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Metrics identity proof', { exact: false })).toBeHidden()
  await page.getByText('Request metrics withheld', { exact: true }).click()
  await expect(page.getByText('Ambiguous observation population', { exact: true })).toBeVisible()
})

test('unattributed HTTP evidence is contextual and does not claim absent observations', async ({ page }) => {
  await page.route('**/api/prometheus/workload/**', (route) => route.fulfill({ json: {
    state: 'available', sources: [{ id: 'beyla', label: 'Beyla', state: 'unavailable' }, { id: 'istio', label: 'Istio', state: 'unavailable' }],
    pods: 2, podsTotal: 2, start: 100, end: 160, stepSeconds: 60, rateWindowSeconds: 300,
    attribution: { beyla: 'Metrics exist, but their cluster identity could not be established automatically.', istio: 'No observations for current Pod names.' },
    history: {}, comparison: {}, panels: {
      requests: { state: 'unavailable', unit: 'requests/s', series: [], reason: 'Generic prerequisites' },
      ...Object.fromEntries(['cpu', 'memory', 'throttling'].map((key) => [key, {
        state: 'unavailable', unit: key === 'cpu' ? 'cores' : key === 'memory' ? 'bytes' : 'percent', series: [], reason: 'Metrics exist, but their cluster identity could not be established automatically.',
      }])),
    },
  } }))
  await page.goto(routes[0])
  await expect(page.getByRole('heading', { name: 'Resources', exact: true })).toBeVisible()
  await page.getByText('No usable HTTP metrics in this window', { exact: true }).click()
  await expect(page.getByText('Metrics exist, but their cluster identity', { exact: false }).first()).toBeVisible()
  await expect(page.getByText('Generic prerequisites', { exact: true })).toHaveCount(0)
  await expect(page.getByText('No HTTP observations in this window', { exact: true })).toHaveCount(0)
})
