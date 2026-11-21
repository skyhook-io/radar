// Browser check for docs/connection-lifecycle.md (scenario U1): a tab whose
// view of the connection is frozen (SSE and status blocked) restarts a
// workload from its stale drawer after another tab switched clusters.
//
//   RADAR_URL=http://localhost:9362 node scripts/connection-lifecycle-ui-check.mjs <screenshot-dir>
//
// Needs a Radar started on radar-test-nonprod with the fixture kubeconfig from
// scripts/connection-lifecycle-check.sh setup (so radar-live-probe exists on
// both clusters) and an installed Chrome. Exits 1 if any expectation fails.
import { chromium } from 'playwright'
import { execSync } from 'node:child_process'
const base = process.env.RADAR_URL || 'http://localhost:9362'
const t = () => new Date().toISOString().slice(11, 19)
const log = (...a) => console.log(t(), ...a)
const ann = (ctx) => execSync(`kubectl --context ${ctx} -n default get deploy radar-live-probe -o jsonpath='{.spec.template.metadata.annotations.kubectl\\.kubernetes\\.io/restartedAt}'`).toString()
const status = async () => (await fetch(base + '/api/connection?contexts=0')).json()

const browser = await chromium.launch({ channel: 'chrome', headless: true })
const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } })
const p1 = await ctx.newPage(), p2 = await ctx.newPage()
const shots = process.argv[2] || '.'
let failed = false
const check = (what, ok) => { if (!ok) { failed = true; log('FAIL:', what) } }
await p2.route('**/api/events/stream**', r => r.abort())
const errors = []
for (const [n, p] of [['tab1', p1], ['tab2', p2]]) p.on('console', m => { if (m.type() === 'error') errors.push(`${n}: ${m.text()}`) })

for (const p of [p1, p2]) {
  await p.goto(base + '/resources/deployments?namespace=default')
  await p.getByText('radar-live-probe').first().waitFor({ timeout: 60000 })
}
log('both tabs show nonprod deployments; connection', (await status()).connectionId)
await p2.getByText('radar-live-probe').first().click()
const restartBtn = p2.getByRole('button', { name: /^Restart/ }).first()
await restartBtn.waitFor({ timeout: 20000 })
await p2.screenshot({ path: `${shots}/ui-1-tab2-drawer-on-nonprod.png` })

// Freeze tab2's view of the connection (no SSE, no status poll) until it acts.
const blockStatus = r => r.abort()
await p2.route('**/api/connection?**', blockStatus)
const before = { nonprod: ann('radar-test-nonprod'), prod: ann('radar-test-prod') }
log('tab1 switches to prod')
await p1.evaluate(() => fetch('/api/contexts/radar-test-prod?track=1', { method: 'POST' }))
for (let i = 0; i < 120; i++) { const s = await status(); if (s.state === 'connected' && s.context === 'radar-test-prod') break; await new Promise(r => setTimeout(r, 500)) }
log('server now serves prod; connection', (await status()).connectionId)

// The stale tab still shows nonprod's drawer: click Restart there.
const tab2StillStale = await restartBtn.isVisible()
log('tab2 drawer still visible (stale):', tab2StillStale)
await p2.unroute('**/api/connection?**', blockStatus)
if (tab2StillStale) await restartBtn.click()
await p2.waitForTimeout(2500)
await p2.screenshot({ path: `${shots}/ui-2-tab2-after-stale-restart.png` })
const toast = await p2.locator('text=/cluster changed|switching clusters/i').first().textContent().catch(() => null)
const after = { nonprod: ann('radar-test-nonprod'), prod: ann('radar-test-prod') }
log('toast in tab2:', toast)
log('writes from the stale tab:', `nonprod=${after.nonprod !== before.nonprod ? 'WROTE' : 'no'} prod=${after.prod !== before.prod ? 'WROTE' : 'no'}`)

// The mismatch should make tab2 re-read its status and reset to prod.
let reset = false
for (let i = 0; i < 40 && !reset; i++) {
  reset = !(await restartBtn.isVisible().catch(() => false))
  if (!reset) await p2.waitForTimeout(500)
}
log('tab2 reset (drawer closed) after the refusal:', reset)
check('stale tab wrote nothing', after.nonprod === before.nonprod && after.prod === before.prod)
check('refusal says nothing was changed', /Nothing was changed/.test(toast || ''))
check('stale tab reset', reset)
await p2.screenshot({ path: `${shots}/ui-3-tab2-after-reset.png` })

// tab1 followed the switch live; its restart from the prod view must work.
await p1.screenshot({ path: `${shots}/ui-3b-tab1-after-switch.png` })
log('tab1 url after switch:', p1.url())
await p1.getByText('radar-live-probe').first().click()
const r1 = p1.getByRole('button', { name: /^Restart/ }).first()
await r1.waitFor({ timeout: 30000 })
const b2 = { nonprod: ann('radar-test-nonprod'), prod: ann('radar-test-prod') }
await r1.click(); await p1.waitForTimeout(2500)
const a2 = { nonprod: ann('radar-test-nonprod'), prod: ann('radar-test-prod') }
log('tab1 (current) restart:', `nonprod=${a2.nonprod !== b2.nonprod ? 'WROTE' : 'no'} prod=${a2.prod !== b2.prod ? 'WROTE' : 'no'}`)
check('current tab wrote prod only', a2.nonprod === b2.nonprod && a2.prod !== b2.prod)
await p1.screenshot({ path: `${shots}/ui-4-tab1-on-prod.png` })
log('console errors:', errors.length ? errors.slice(0, 8) : 'none')
await browser.close()
process.exit(failed ? 1 : 0)
