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
