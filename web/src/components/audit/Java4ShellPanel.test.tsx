import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { getApiBase } from '../../api/config'
import { streamJava4Shell, type Java4ShellResult } from '../../api/vulnerabilities'
import { Java4ShellPanel } from './Java4ShellPanel'

it('shows Java home and check limit counts before opening details and counts other failures once', () => {
  const result: Java4ShellResult = {
    findings: [],
    containers: 16,
    matches: 0,
    findingGroups: 0,
    unassessed: { java_home_declared: 3, auth_required: 2, platform_unknown: 2, no_java_declaration: 1, not_checked: 4, docker_hub_limit: 1 },
    unassessedRows: [],
    unassessedGroups: 5,
    unaffectedByMajor: { '8': 3 },
    coverage: { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] },
  }
  const client = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  client.setQueryData(['java4shell', getApiBase(), 'fixture', '/vulnerabilities/java4shell'], result)
  const html = renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <Java4ShellPanel context="fixture" connected onNavigateToResource={() => {}} />
    </QueryClientProvider>,
  )

  expect(html).toContain('Images for 3 containers set <code>JAVA_HOME</code>. Verify the installed Java version.')
  expect(html).toContain('Check limit reached for 4 containers.')
  expect(html).toContain('Docker Hub lookup limit reached for 1 container.')
  expect(html).toContain('12 with assessment gaps')
  expect(html).toContain('Declared Java 8 outside affected versions · 3 containers')
  expect(html).toContain('Registry authentication required for 2 containers.')
  expect(html).toContain('Image platform unknown · 2 containers')
  expect(html).toContain('aria-expanded="false"')
  expect(html).not.toContain('aria-label="Unassessed containers by workload"')
})

it('delivers progress and only returns a result after the terminal record', async () => {
  const update = { done: 1, total: 3 }
  const report = { containers: 3, matches: 0, unaffectedByMajor: { '8': 3 } }
  const progressLine = JSON.stringify({ type: 'progress', progress: update }) + '\n'
  const resultLine = JSON.stringify({ type: 'result', report }) + '\n'
  const encoder = new TextEncoder()
  const fetch = vi.fn()
  const response = (...chunks: string[]) => new Response(new ReadableStream({
    start(stream) {
      for (const chunk of chunks) stream.enqueue(encoder.encode(chunk))
      stream.close()
    },
  }))
  fetch.mockResolvedValueOnce(response(progressLine.slice(0, 6), progressLine.slice(6) + resultLine))
  fetch.mockResolvedValueOnce(response(progressLine))
  vi.stubGlobal('fetch', fetch)
  try {
    const onProgress = vi.fn()
    await expect(streamJava4Shell('/api/vulnerabilities/java4shell/stream', onProgress)).resolves.toEqual(report)
    expect(onProgress).toHaveBeenCalledExactlyOnceWith(update)
    await expect(streamJava4Shell('/api/vulnerabilities/java4shell/stream', vi.fn())).rejects.toThrow('ended before Radar returned a result')
  } finally {
    vi.unstubAllGlobals()
  }
})
