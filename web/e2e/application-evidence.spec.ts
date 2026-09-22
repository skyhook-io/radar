import { test, expect } from '@playwright/test'

test('a stale Pod refreshes candidates and requires a new explicit collection', async ({ page }) => {
  let candidateReads = 0
  const collectedUIDs: string[] = []
  let releaseRefresh!: () => void
  const refreshReleased = new Promise<void>(resolve => { releaseRefresh = resolve })

  await page.route('**/api/cluster-info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), context: 'evidence-test' } })
  })
  await page.route('**/api/capabilities', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), deployment: { mode: 'local' } } })
  })
  await page.route('**/api/application-evidence/candidates?*', async route => {
    candidateReads++
    const replacement = collectedUIDs.length > 0
    if (replacement) await refreshReleased
    await route.fulfill({ json: {
      enabled: true, context: 'evidence-test', subjectUID: 'deploy-uid-1234',
      candidates: [{ application: 'vault',
        target: { namespace: 'default', pod: 'vault-0', uid: replacement ? 'replacement-uid' : 'original-uid', container: 'vault' },
        expectedEvidence: ['initialized and sealed state'], coverage: 'Selected Pod only',
      }],
    } })
  })
  await page.route('**/api/application-evidence/collect', async route => {
    const request = route.request().postDataJSON()
    collectedUIDs.push(request.uid)
    await route.fulfill({ json: {
      adapter: 'vault', target: { namespace: request.namespace, pod: request.pod, uid: request.uid },
      observedAt: '2026-09-22T10:00:00Z', source: 'selected_pod_endpoint', limitations: ['Selected Pod only'],
      ...(collectedUIDs.length === 1
        ? { outcome: 'unavailable', reason: 'target_changed' }
        : { outcome: 'observed', facts: { vault: { initialized: true, sealed: true, standby: false } } }),
    } })
  })

  await page.goto('/resources/deployments?resource=default%2Fnginx')
  const openAction = page.getByRole('button', { name: 'Collect Vault evidence', exact: true })
  await expect(openAction).toBeEnabled()
  expect(collectedUIDs).toEqual([])
  const initialReads = candidateReads
  await openAction.click()
  expect(collectedUIDs).toEqual([])
  await page.getByRole('button', { name: 'Collect evidence', exact: true }).click()
  await expect(page.getByText('The selected Pod changed.', { exact: false })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Collect again', exact: true })).toBeDisabled()
  await expect.poll(() => candidateReads).toBeGreaterThan(initialReads)
  expect(collectedUIDs).toEqual(['original-uid'])
  await page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Collect application evidence', exact: true }) }).getByRole('button', { name: 'Close', exact: true }).click()
  await expect(openAction).toBeDisabled()
  releaseRefresh()
  await expect(openAction).toBeEnabled()
  await openAction.click()
  expect(collectedUIDs).toEqual(['original-uid'])
  await page.getByRole('button', { name: 'Collect evidence', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Endpoint observations', exact: true })).toBeVisible()
  expect(collectedUIDs).toEqual(['original-uid', 'replacement-uid'])
})
