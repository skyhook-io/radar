import { createServer } from 'vite'
import { chromium } from 'playwright'
import assert from 'node:assert/strict'

// Render the real dialog and real React Query mutations against HTTP fixtures.
// No Kubernetes requests or release writes are made by this test.
const server = await createServer({ optimizeDeps: { include: ['react', 'react-dom/client', '@tanstack/react-query'] }, server: { host: '127.0.0.1', port: 0 }, plugins: [{
  name: 'repository-click-fixture',
  configureServer(server) {
    server.middlewares.use('/__repository-click', async (req, res, next) => {
      if (req.url.includes('?')) return next()
      res.setHeader('Content-Type', 'text/html')
      res.end(await server.transformIndexHtml('/__repository-click', `<div id="root"></div><script type="module">
        import React from 'react';
        import ReactDOMClient from 'react-dom/client';
        import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
        import {TrackChartSourceDialog} from '/src/components/helm/TrackChartSourceDialog.tsx';
        const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
        client.getQueryCache().subscribe(event => {
          if(event.action?.type==='invalidate') (window.invalidated ||= []).push(event.query.queryKey[0]);
        });
        client.setQueryData(['helm-upgrade-info'], {});
        client.setQueryData(['helm-batch-upgrade-info'], {});
        function Fixture() {
          const [open,setOpen]=React.useState(true);
          return React.createElement(TrackChartSourceDialog,{open,namespace:'example-namespace',releaseName:'example-release',
            chartName:'example-chart',sourceIssue:'untracked',onClose:()=>{window.closedDialog=true;setOpen(false)}});
        }
        ReactDOMClient.createRoot(document.getElementById('root')).render(React.createElement(QueryClientProvider,{client},React.createElement(Fixture)));
      </script>`))
    })
  },
}] })
await server.listen()
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  page.on('pageerror', error => console.error(error.message))
  page.on('console', message => { if(message.type() === 'error') console.error(message.text()) })
  page.on('requestfailed', request => console.error(request.url(), request.failure()?.errorText))
  page.on('response', response => { if(response.status() >= 400) console.error(response.status(), response.url()) })
  const requests = []
  let reject = true
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (!path.startsWith('/api/')) return route.continue()
    if (route.request().method() === 'POST' && path.endsWith('/repositories')) {
      requests.push(route.request().postDataJSON())
      await new Promise(resolve => setTimeout(resolve, 300))
      return route.fulfill({ status: reject ? 400 : 200, json: reject
        ? {error:'repository name conflicts with a different URL'} : {name:'recorded-repo',associated:true} })
    }
    return route.fulfill({json: path.endsWith('/repositories')
      ? [{name:'recorded-repo',url:'https://recorded.example.test/charts/'}]
      : path.endsWith('/source-candidates') ? [{type:'repository',reference:'recorded-repo'}]
      : path.endsWith('/cluster-info') ? {} : []})
  })
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__repository-click`)
  const form = page.getByTestId('helm-repository-form')
  await form.waitFor({timeout: 120000})
  await page.waitForFunction(() => document.querySelector('[data-testid="helm-repository-form"] input')?.value === 'recorded-repo')
  assert.equal(await form.getByLabel('Repository URL').inputValue(), 'https://recorded.example.test/charts/')
  await page.getByLabel('Prefix', {exact:true}).fill('https://invalid-in-oci.example')
  await form.getByRole('button', {name:'Add repository',exact:true}).click()
  assert.equal(await form.getByRole('button',{name:'Adding repository…'}).isDisabled(),true)
  await form.getByRole('alert').waitFor()
  assert.match(await form.getByRole('alert').innerText(),/conflicts with a different URL/)
  assert.deepEqual(requests,[{name:'recorded-repo',url:'https://recorded.example.test/charts/',namespace:'example-namespace',releaseName:'example-release'}])
  await form.getByLabel('Name',{exact:true}).fill('edited')
  await form.getByLabel('Repository URL').fill('https://manual.example/charts/')
  reject=false
  await form.getByRole('button',{name:'Add repository',exact:true}).click()
  await page.waitForFunction(()=>window.closedDialog)
  assert.equal(requests.length,2)
  assert.equal(requests[1].name,'edited')
  assert.equal(requests[1].url,'https://manual.example/charts/')
  const invalidated=await page.evaluate(()=>window.invalidated)
  for(const key of ['helm-repositories','helm-source-candidates','helm-upgrade-info','helm-batch-upgrade-info']) assert.ok(invalidated.includes(key),key)

  // Prove the prefill is driven by arbitrary API data, not the recorded-source
  // regression fixture above.
  const genericPage = await browser.newPage()
  await genericPage.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (!path.startsWith('/api/')) return route.continue()
    if (route.request().method() !== 'GET') {
      return route.fulfill({status: 500, json: {error: 'unexpected mutation'}})
    }
    return route.fulfill({json: path.endsWith('/repositories')
      ? []
      : path.endsWith('/source-candidates') ? [{type:'repository',reference:'example-repo',url:'https://charts.example.test'}]
      : path.endsWith('/cluster-info') ? {} : []})
  })
  await genericPage.goto(`http://127.0.0.1:${server.httpServer.address().port}/__repository-click`)
  const genericForm = genericPage.getByTestId('helm-repository-form')
  await genericForm.waitFor({timeout: 120000})
  await genericPage.waitForFunction(() => document.querySelector('[data-testid="helm-repository-form"] input')?.value === 'example-repo')
  assert.equal(await genericForm.getByLabel('Name',{exact:true}).inputValue(), 'example-repo')
  assert.equal(await genericForm.getByLabel('Repository URL').inputValue(), 'https://charts.example.test')
  assert.equal(await genericForm.getByRole('button',{name:'Add repository',exact:true}).isEnabled(), true)
  await genericPage.close()

  // ArtifactHub is opt-in: opening the dialog performs no external discovery.
  // Once clicked, a uniquely verified candidate fills the actual controlled
  // form state while manual recovery remains present.
  const discoveryPage = await browser.newPage()
  let discoveryRequests = 0
  await discoveryPage.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (!path.startsWith('/api/')) return route.continue()
    if (path.endsWith('/source-discovery/artifacthub')) {
      discoveryRequests++
      assert.equal(route.request().method(), 'POST')
      assert.equal(route.request().postData(), null)
      return route.fulfill({json:[{type:'repository',reference:'fixture-repo',url:'https://verified.example.test/charts'}]})
    }
    return route.fulfill({json: path.endsWith('/source-candidates') || path.endsWith('/repositories') ? [] : {}})
  })
  await discoveryPage.goto(`http://127.0.0.1:${server.httpServer.address().port}/__repository-click`)
  const discovery = discoveryPage.getByTestId('artifacthub-recovery')
  await discovery.waitFor({timeout: 120000})
  assert.equal(discoveryRequests, 0)
  await discovery.getByRole('button',{name:'Search ArtifactHub for possible source',exact:true}).click()
  await discovery.getByText('Possible source found via ArtifactHub',{exact:true}).waitFor()
  const discoveryForm = discoveryPage.getByTestId('helm-repository-form')
  assert.equal(await discoveryForm.getByLabel('Name',{exact:true}).inputValue(), 'fixture-repo')
  assert.equal(await discoveryForm.getByLabel('Repository URL').inputValue(), 'https://verified.example.test/charts')
  assert.equal(await discoveryForm.getByRole('button',{name:'Add and use repository',exact:true}).isEnabled(), true)
  assert.equal(discoveryRequests, 1)
  await discoveryPage.close()

  // Closing the dialog aborts an in-flight external discovery request instead
  // of leaving a background search or pagination loop running.
  const cancellationPage = await browser.newPage()
  let cancellationStarted = false
  let cancellationObserved = false
  cancellationPage.on('requestfailed', request => {
    if (request.url().includes('/source-discovery/artifacthub')) cancellationObserved = true
  })
  await cancellationPage.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (!path.startsWith('/api/')) return route.continue()
    if (path.endsWith('/source-discovery/artifacthub')) {
      cancellationStarted = true
      await new Promise(resolve => setTimeout(resolve, 1500))
      return route.fulfill({json: []}).catch(() => {})
    }
    return route.fulfill({json: path.endsWith('/source-candidates') || path.endsWith('/repositories') ? [] : {}})
  })
  await cancellationPage.goto(`http://127.0.0.1:${server.httpServer.address().port}/__repository-click`)
  await cancellationPage.getByTestId('artifacthub-recovery').getByRole('button',{name:'Search ArtifactHub for possible source',exact:true}).click()
  await cancellationPage.waitForFunction(() => true, undefined, {timeout: 100})
  assert.equal(cancellationStarted, true)
  await cancellationPage.getByRole('button',{name:'Close',exact:true}).click()
  await cancellationPage.waitForFunction(() => !document.querySelector('[data-testid="artifacthub-recovery"]'))
  await new Promise(resolve => setTimeout(resolve, 100))
  assert.equal(cancellationObserved, true)
  await cancellationPage.close()

  console.log('PASS: generic dynamic prefill, explicit ArtifactHub opt-in, verified candidate state, cancellation, input UX, one POST per click, loading/error, manual edits, independent OCI state')
} finally {
  await browser.close()
  await server.close()
}
