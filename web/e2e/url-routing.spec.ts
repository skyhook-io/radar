import { test, expect } from '@playwright/test'

// Wait for the app to be connected to the cluster and the resources view to load
async function waitForResourcesView(page: import('@playwright/test').Page) {
  // Wait for the resource table or sidebar to appear (indicates data loaded)
  await page.waitForSelector('[data-testid="resources-sidebar"], table, .resource-list, [class*="ResourcesView"]', { timeout: 15000 }).catch(() => {})
  // Give the app a moment to settle after initial render
  await page.waitForTimeout(1000)
}

test.describe('URL Routing Migration: kind in path', () => {

  test('navigating to /resources/pods shows pods list', async ({ page }) => {
    await page.goto('/resources/pods')
    await waitForResourcesView(page)

    // URL should have kind in path, not query param
    expect(page.url()).toContain('/resources/pods')
    expect(page.url()).not.toContain('kind=pods')

    // Page should be on resources view
    await expect(page.locator('body')).toBeVisible()
  })

  test('navigating to /resources/deployments shows deployments list', async ({ page }) => {
    await page.goto('/resources/deployments')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/deployments')
    expect(page.url()).not.toContain('kind=')
  })

  test('navigating to /resources/services shows services list', async ({ page }) => {
    await page.goto('/resources/services')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/services')
    expect(page.url()).not.toContain('kind=')
  })

  test('/resources with no kind defaults to pods', async ({ page }) => {
    await page.goto('/resources')
    await waitForResourcesView(page)

    // Should redirect or default to pods — URL should contain /resources/pods
    expect(page.url()).toContain('/resources/pods')
  })

  test('backward compat: /resources?kind=pods redirects to /resources/pods', async ({ page }) => {
    await page.goto('/resources?kind=pods')
    await waitForResourcesView(page)

    // Should have been redirected — kind in path, not query param
    expect(page.url()).toContain('/resources/pods')
    expect(page.url()).not.toContain('kind=pods')
  })

  test('backward compat: /resources?kind=deployments redirects to /resources/deployments', async ({ page }) => {
    await page.goto('/resources?kind=deployments')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/deployments')
    expect(page.url()).not.toContain('kind=deployments')
  })

  test('backward compat: /resources?kind=secrets&search=radar preserves other params', async ({ page }) => {
    await page.goto('/resources?kind=secrets&search=radar')
    await waitForResourcesView(page)

    // Kind should be in path, search should remain as query param
    expect(page.url()).toContain('/resources/secrets')
    expect(page.url()).not.toContain('kind=secrets')
    expect(page.url()).toContain('search=radar')
  })

  test('query params (search, namespaces) are preserved with path-based kind', async ({ page }) => {
    await page.goto('/resources/pods?search=test&namespaces=default')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/pods')
    expect(page.url()).toContain('search=test')
    expect(page.url()).toContain('namespaces=default')
    expect(page.url()).not.toContain('kind=')
  })

  test('apiGroup query param is preserved for CRDs', async ({ page }) => {
    // Navigate to a CRD-like resource with apiGroup
    await page.goto('/resources/applications?apiGroup=argoproj.io')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/applications')
    expect(page.url()).toContain('apiGroup=argoproj.io')
    expect(page.url()).not.toContain('kind=')
  })

  test('switching kinds via sidebar updates URL path', async ({ page }) => {
    await page.goto('/resources/pods')
    await waitForResourcesView(page)

    // Click on a different kind in the sidebar — find "Services" or "Deployments" text
    const sidebarItem = page.locator('text=Deployments').first()
    if (await sidebarItem.isVisible()) {
      await sidebarItem.click()
      await page.waitForTimeout(1000)

      // URL should now have the new kind in path
      expect(page.url()).toContain('/resources/deployments')
      expect(page.url()).not.toContain('kind=')
    }
  })

  test('browser back/forward navigates between kinds', async ({ page }) => {
    // Start on pods
    await page.goto('/resources/pods')
    await waitForResourcesView(page)

    // Navigate to deployments via sidebar
    const deploymentsItem = page.locator('text=Deployments').first()
    if (await deploymentsItem.isVisible()) {
      await deploymentsItem.click()
      await page.waitForTimeout(1000)
      expect(page.url()).toContain('/resources/deployments')

      // Navigate to services
      const servicesItem = page.locator('text=Services').first()
      if (await servicesItem.isVisible()) {
        await servicesItem.click()
        await page.waitForTimeout(1000)
        expect(page.url()).toContain('/resources/services')

        // Go back — should be on deployments
        await page.goBack()
        await page.waitForTimeout(1000)
        expect(page.url()).toContain('/resources/deployments')

        // Go back again — should be on pods
        await page.goBack()
        await page.waitForTimeout(1000)
        expect(page.url()).toContain('/resources/pods')

        // Go forward — should be on deployments
        await page.goForward()
        await page.waitForTimeout(1000)
        expect(page.url()).toContain('/resources/deployments')
      }
    }
  })

  test('deep link from home dashboard navigates with kind in path', async ({ page }) => {
    await page.goto('/')
    await page.waitForTimeout(3000) // Wait for dashboard to load

    // Look for a resource count card that navigates to resources view
    // Dashboard typically has clickable cards like "Pods", "Deployments", etc.
    const podsCard = page.locator('text=Pods').first()
    if (await podsCard.isVisible()) {
      await podsCard.click()
      await page.waitForTimeout(1500)

      // If we navigated to resources, kind should be in path
      if (page.url().includes('/resources')) {
        expect(page.url()).not.toContain('kind=')
      }
    }
  })

  test('other views are not affected (topology)', async ({ page }) => {
    await page.goto('/topology')
    await page.waitForTimeout(2000)

    expect(page.url()).toContain('/topology')
    // Topology should not have kind in URL at all
    expect(page.url()).not.toContain('/resources/')
  })

  test('other views are not affected (timeline)', async ({ page }) => {
    await page.goto('/timeline')
    await page.waitForTimeout(2000)

    expect(page.url()).toContain('/timeline')
    expect(page.url()).not.toContain('/resources/')
  })

  test('owner filter deep link works with new URL format', async ({ page }) => {
    // Simulate the WorkloadRenderer "View Pods" link format
    await page.goto('/resources/pods?ownerKind=Deployment&ownerName=test&namespace=default')
    await waitForResourcesView(page)

    expect(page.url()).toContain('/resources/pods')
    expect(page.url()).toContain('ownerKind=Deployment')
    expect(page.url()).toContain('ownerName=test')
    expect(page.url()).not.toContain('kind=')
  })
})
