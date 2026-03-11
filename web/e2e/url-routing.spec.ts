import { test, expect } from '@playwright/test'

test.describe('URL routing: kind in path', () => {

  test('/resources defaults to /resources/pods', async ({ page }) => {
    await page.goto('/resources')
    // The redirect uses history.replaceState (not navigate), so use polling assertion
    await expect(page).toHaveURL(/\/resources\/pods/, { timeout: 10000 })
    expect(page.url()).not.toContain('kind=')
  })

  test('query params are preserved alongside path-based kind', async ({ page }) => {
    await page.goto('/resources/pods?search=test&namespaces=default')
    await page.waitForURL('**/resources/pods**')

    const url = new URL(page.url())
    expect(url.pathname).toBe('/resources/pods')
    expect(url.searchParams.get('search')).toBe('test')
    expect(url.searchParams.get('namespaces')).toBe('default')
    expect(url.searchParams.has('kind')).toBe(false)
  })

  test('apiGroup query param is preserved for CRDs', async ({ page }) => {
    await page.goto('/resources/applications?apiGroup=argoproj.io')
    await page.waitForURL('**/resources/applications**')

    const url = new URL(page.url())
    expect(url.pathname).toBe('/resources/applications')
    expect(url.searchParams.get('apiGroup')).toBe('argoproj.io')
    expect(url.searchParams.has('kind')).toBe(false)
  })

  test('owner filter deep link works', async ({ page }) => {
    await page.goto('/resources/pods?ownerKind=Deployment&ownerName=nginx&namespace=default')
    await page.waitForURL('**/resources/pods**')

    const url = new URL(page.url())
    expect(url.pathname).toBe('/resources/pods')
    expect(url.searchParams.get('ownerKind')).toBe('Deployment')
    expect(url.searchParams.get('ownerName')).toBe('nginx')
    expect(url.searchParams.has('kind')).toBe(false)
  })
})
