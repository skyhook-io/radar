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

    // Should have Configuration and Preferences tabs
    await expect(page.locator('button:text("Configuration")')).toBeVisible()
    await expect(page.locator('button:text("Preferences")')).toBeVisible()
  })

  test('Configuration tab shows startup fields', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()

    // Should show the Configuration tab content by default
    await expect(page.locator('text=Changes require a restart')).toBeVisible()
    await expect(page.getByText('Kubeconfig', { exact: true })).toBeVisible()
    await expect(page.locator('label:text("Default Namespace")')).toBeVisible()
    await expect(page.locator('label:text("Storage Backend")')).toBeVisible()
  })

  test('Preferences tab shows log viewer settings', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()

    // Switch to Preferences tab
    await page.locator('button:text("Preferences")').click()

    await expect(page.locator('text=These preferences apply immediately')).toBeVisible()
    await expect(page.locator('text=Word wrap')).toBeVisible()
    await expect(page.locator('text=Show timestamps')).toBeVisible()
  })

  test('ESC closes the settings dialog', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()
    await expect(page.locator('button:text("Configuration")')).toBeVisible()

    await page.keyboard.press('Escape')
    // Dialog should be gone after animation
    await expect(page.locator('button:text("Configuration")')).not.toBeVisible({ timeout: 1000 })
  })

  test('backdrop click closes the dialog', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('header', { timeout: 10000 })

    await page.locator('button[title="Settings"]').click()
    await expect(page.locator('button:text("Configuration")')).toBeVisible()

    // Click the backdrop (outside the dialog)
    await page.locator('.bg-black\\/60').click({ position: { x: 10, y: 10 } })
    await expect(page.locator('button:text("Configuration")')).not.toBeVisible({ timeout: 1000 })
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

  test('PUT /api/settings with logsWrap persists correctly', async ({ request }) => {
    const putRes = await request.put('/api/settings', {
      data: { logsWrap: false },
    })
    expect(putRes.ok()).toBeTruthy()
    const body = await putRes.json()
    expect(body.logsWrap).toBe(false)

    // Read back
    const getRes = await request.get('/api/settings')
    const getBody = await getRes.json()
    expect(getBody.logsWrap).toBe(false)

    // Clean up — set back to default
    await request.put('/api/settings', { data: { logsWrap: true } })
  })
})
