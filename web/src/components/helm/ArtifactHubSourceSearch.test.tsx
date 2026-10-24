// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { ArtifactHubChart } from '../../types'
import { ArtifactHubSourceSearch, artifactHubChartSource } from './ArtifactHubSourceSearch'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const chart: ArtifactHubChart = { packageId: 'chart', name: 'podinfo', version: '6.15.0', stars: 0, repository: { name: 'podinfo', url: 'https://charts.example.test' } }
const fetchMock = vi.fn<typeof fetch>()
const add = vi.fn()
const configured = vi.fn()
let root: Root
let queries: QueryClient

beforeEach(() => {
  fetchMock.mockReset()
  add.mockReset()
  configured.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  queries = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => root.unmount())
  queries.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

async function render() {
  await act(async () => root.render(<QueryClientProvider client={queries}><ArtifactHubSourceSearch chartName="podinfo" disabled={false} isConfigured={() => false} onAdd={add} onConfigured={configured} manualAction={<button>Add manually</button>} /></QueryClientProvider>))
}

async function click(text: string) {
  const button = Array.from(document.querySelectorAll('button')).find(button => button.textContent === text)
  expect(button).toBeDefined()
  await act(async () => button?.click())
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
}

it('sends only the chart name after a click and never downloads or selects a result automatically', async () => {
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ charts: [chart, { ...chart, name: 'another-chart' }], total: 2 })))
  await render()
  expect(fetchMock).not.toHaveBeenCalled()
  expect(document.body.textContent).not.toContain('Add repository')
  await click('Search ArtifactHub')
  expect(fetchMock).toHaveBeenCalledTimes(1)
  const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost')
  expect(url.pathname).toBe('/api/helm/artifacthub/search')
  expect(Object.fromEntries(url.searchParams)).toEqual({ query: 'podinfo', limit: '60' })
  expect(document.querySelectorAll('button[aria-label^="Choose source:"]')).toHaveLength(1)
  expect(add).not.toHaveBeenCalled()
  await click('podinfohttps://charts.example.test')
  await click('Add repository')
  expect(add).toHaveBeenCalledWith(chart, { type: 'repository', url: 'https://charts.example.test' })
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

it('shows failures and allows retry while preserving manual-source guidance', async () => {
  fetchMock.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(new Response(JSON.stringify({ charts: [], total: 0 })))
  await render()
  await click('Search ArtifactHub')
  expect(document.body.textContent).toContain('You can still add a source manually.')
  await click('Retry search')
  expect(document.body.textContent).toContain('No exact chart-name matches on this page.')
})

it('paginates unfiltered search results even when a page has no exact matches', async () => {
  fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ charts: Array.from({ length: 60 }, () => ({ ...chart, name: 'different' })), total: 60 })))
    .mockResolvedValueOnce(new Response(JSON.stringify({ charts: [chart], total: 1 })))
  await render()
  await click('Search ArtifactHub')
  expect(document.body.textContent).toContain('No exact chart-name matches on this page.')
  await click('Next results')
  expect(String(fetchMock.mock.calls[1][0])).toContain('offset=60')
  expect(document.body.textContent).toContain('Search page 2')
  expect(document.querySelectorAll('button[aria-label^="Choose source:"]')).toHaveLength(1)
  await click('Previous results')
  expect(document.body.textContent).toContain('Search page 1')
})

it('filters unsafe addresses and maps OCI chart references to their parent prefix', () => {
  const source = (url: string) => artifactHubChartSource({ ...chart, repository: { name: 'example', url } }, 'podinfo')
  expect(source('oci://registry.example.test/team/podinfo/')).toEqual({ type: 'oci', url: 'oci://registry.example.test/team/podinfo', prefix: 'oci://registry.example.test/team' })
  expect(source('oci://REGISTRY.example.test/Team/podinfo')).toEqual({ type: 'oci', url: 'oci://registry.example.test/Team/podinfo', prefix: 'oci://registry.example.test/Team' })
  expect(source('https://CHARTS.example.test/Team/')).toEqual({ type: 'repository', url: 'https://charts.example.test/Team' })
  expect(source('https://charts.example.test/team')).toEqual({ type: 'repository', url: 'https://charts.example.test/team' })
  for (const url of ['file:///tmp/chart', 'javascript:alert(1)', 'https://user:password@example.test/charts', 'https://example.test/charts?token=secret', 'https://example.test/#secret', 'oci://registry.example.test/team/other']) {
    expect(source(url)).toBeUndefined()
  }
})

it.each([false, true])('keeps one representative publisher, badge and repository name together regardless of response order (%s)', async reverse => {
  const ordinary = { ...chart, packageId: 'ordinary', repository: { ...chart.repository, name: 'ordinary', organizationDisplayName: 'Other Publisher' } }
  const official = { ...chart, packageId: 'official', official: true, repository: { ...chart.repository, name: 'official-charts', displayName: 'Official Charts', organizationDisplayName: 'Publisher Team' } }
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ charts: reverse ? [official, ordinary] : [ordinary, official] })))
  await render()
  await click('Search ArtifactHub')
  expect(document.querySelectorAll('button[aria-label^="Choose source:"]')).toHaveLength(1)
  expect(document.body.textContent).toContain('Publisher Team')
  expect(document.body.textContent).not.toContain('Other Publisher')
  expect(document.body.textContent).toContain('ArtifactHub official package')
  expect(document.body.textContent).not.toContain('ArtifactHub official repository')
  expect(add).not.toHaveBeenCalled()
  await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label^="Choose source:"]')?.click())
  await click('Add repository')
  expect(add).toHaveBeenCalledWith(official, { type: 'repository', url: chart.repository.url })
})

it.each([false, true])('uses a stable listing identity when duplicate badges tie (%s)', async reverse => {
  const first = { ...chart, packageId: 'a', repository: { ...chart.repository, name: 'first', userAlias: 'first-account', verifiedPublisher: true } }
  const second = { ...chart, packageId: 'b', repository: { ...chart.repository, name: 'second', userAlias: 'second-account', verifiedPublisher: true } }
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ charts: reverse ? [second, first] : [first, second] })))
  await render()
  await click('Search ArtifactHub')
  expect(document.body.textContent).toContain('first-account')
  expect(document.body.textContent).not.toContain('second-account')
})
