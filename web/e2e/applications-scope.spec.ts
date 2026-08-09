import { test, expect } from '@playwright/test'

// The seeded cluster has exactly one application (nginx) with exactly one
// workload, which is the case the invariant governs: a single-workload app has
// no application scope, so the detail page must land on the workload's runtime
// and must not offer the application view tabs.
const SINGLE_WORKLOAD_APP = 'default/Deployment/nginx'
const SINGLE_WORKLOAD_KEY = 'Deployment/default/nginx'

test.describe('Applications: single-workload scope', () => {
  test('a single-workload app opens on its workload, not application scope', async ({ page }) => {
    await page.goto(`/applications?app=${SINGLE_WORKLOAD_APP}`)

    // The host canonicalizes the sole workload into the URL (replace navigation).
    await expect(page).toHaveURL(/workload=/, { timeout: 15000 })
    await expect(page.getByRole('tablist', { name: 'Application views' })).toHaveCount(0)
  })

  test('a workload key naming a workload the app no longer has still resolves to the sole workload', async ({
    page,
  }) => {
    await page.goto(
      `/applications?app=${SINGLE_WORKLOAD_APP}&workload=Deployment/default/removed`,
    )

    await expect(page).toHaveURL(/workload=Deployment(%2F|\/)default(%2F|\/)nginx/, {
      timeout: 15000,
    })
    await expect(page.getByRole('tablist', { name: 'Application views' })).toHaveCount(0)
  })

  test('a valid workload deep link is preserved', async ({ page }) => {
    await page.goto(
      `/applications?app=${SINGLE_WORKLOAD_APP}&workload=${SINGLE_WORKLOAD_KEY}`,
    )

    await expect(page).toHaveURL(/workload=Deployment(%2F|\/)default(%2F|\/)nginx/, {
      timeout: 15000,
    })
    await expect(page.getByRole('tablist', { name: 'Application views' })).toHaveCount(0)
  })
})
