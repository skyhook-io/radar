import { test, expect } from '@playwright/test'

test.describe('Settings dialog', () => {

  test('gear icon opens settings dialog', async ({ page }) => {
    await page.goto('/')
    // Wait for the app to load
    await page.waitForSelector('header', { timeout: 10000 })

    // Click the settings gear icon
    const settingsBtn = page.locator('button[title="Settings"]')
    await expect(settingsBtn).toBeVisible()
    await settingsBtn.click()

    // Settings dialog should appear
    const dialog = page.locator('text=Settings')
    await expect(dialog.first()).toBeVisible()

    // Should show configuration fields
    await expect(page.getByText('Kubeconfig', { exact: true })).toBeVisible()
  })

  test('Configuration fields are visible', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()

    // Should show the Configuration content
    await expect(page.locator('text=Changes require a restart')).toBeVisible()
    await expect(page.getByText('Kubeconfig', { exact: true })).toBeVisible()
    await expect(page.locator('label:text("Default Namespace")')).toBeVisible()
    await expect(page.locator('label:text("Storage Backend")')).toBeVisible()
  })

  test('ESC closes the settings dialog', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()
    await expect(page.getByText('Kubeconfig', { exact: true })).toBeVisible()

    await page.keyboard.press('Escape')
    // Dialog should be gone after animation
    await expect(page.getByText('Kubeconfig', { exact: true })).not.toBeVisible({ timeout: 1000 })
  })

  test('backdrop click closes the dialog', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()
    await expect(page.getByText('Kubeconfig', { exact: true })).toBeVisible()

    // Click the backdrop (outside the dialog)
    await page.locator('.bg-black\\/60').click({ position: { x: 10, y: 10 } })
    await expect(page.getByText('Kubeconfig', { exact: true })).not.toBeVisible({ timeout: 1000 })
  })
})

test.describe('Settings API', () => {

  test('GET /api/config returns file and effective config', async ({ request }) => {
    const res = await request.get('/api/config')
    expect(res.ok()).toBeTruthy()

    const body = await res.json()
    expect(body).toHaveProperty('file')
    expect(body).toHaveProperty('effective')
    expect(body).toHaveProperty('isDesktop')
    expect(body.isDesktop).toBe(false) // CLI mode
  })

  test('GET /api/settings returns current settings', async ({ request }) => {
    const res = await request.get('/api/settings')
    expect(res.ok()).toBeTruthy()

    const body = await res.json()
    // Should be a valid object (may or may not have fields)
    expect(typeof body).toBe('object')
  })

  test('PUT /api/config persists and GET reads back', async ({ request }) => {
    // Write a test config value
    const putRes = await request.put('/api/config', {
      data: { namespace: 'e2e-test-ns', historyLimit: 5000 },
    })
    expect(putRes.ok()).toBeTruthy()
    const putBody = await putRes.json()
    expect(putBody.namespace).toBe('e2e-test-ns')
    expect(putBody.historyLimit).toBe(5000)

    // Read it back
    const getRes = await request.get('/api/config')
    const getBody = await getRes.json()
    expect(getBody.file.namespace).toBe('e2e-test-ns')
    expect(getBody.file.historyLimit).toBe(5000)

    // Clean up — reset to empty
    await request.put('/api/config', { data: {} })
  })

  test('PUT /api/settings with theme persists correctly', async ({ request }) => {
    const putRes = await request.put('/api/settings', {
      data: { theme: 'dark' },
    })
    expect(putRes.ok()).toBeTruthy()
    const body = await putRes.json()
    expect(body.theme).toBe('dark')

    // Read back
    const getRes = await request.get('/api/settings')
    const getBody = await getRes.json()
    expect(getBody.theme).toBe('dark')

    // Clean up
    await request.put('/api/settings', { data: { theme: 'system' } })
  })
})
