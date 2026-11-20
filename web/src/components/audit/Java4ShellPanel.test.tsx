import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { getApiBase } from '../../api/config'
import { streamJava4Shell, type Java4ShellResult } from '../../api/vulnerabilities'
import { Java4ShellPanel } from './Java4ShellPanel'

it('shows Java home and check limit counts before opening details and counts other failures once', () => {
  const result: Java4ShellResult = {
    findings: [],
    containers: 18,
    matches: 0,
    findingGroups: 0,
    unassessed: { java_home_declared: 3, auth_required: 2, platform_unknown: 2, no_java_declaration: 1, not_checked: 4, docker_hub_limit: 1, rate_limited: 2 },
    unassessedRows: [],
    unassessedGroups: 5,
    rateLimitedRegistries: ['public.ecr.aws'],
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
  expect(html).toContain('Scan limit reached for 4 containers.')
  expect(html).toContain('Radar&#x27;s Docker Hub lookup cap reached for 1 container.')
  expect(html).toContain('14 with assessment gaps')
  expect(html).toContain('Check scope</span> · All namespaces')
  expect(html).toContain('Could not check · next steps')
  expect(html).toContain('Retry to reuse cached public image metadata.')
  expect(html).toContain('Registry throttling affected 2 containers · public.ecr.aws')
  expect(html).toContain('Radar pauses calls to throttled registries for five minutes.')
  expect(html).toContain('verify the installed version in workloads that use Java')
  expect(html).toContain('Declared Java 8 outside affected versions · 3 containers')
  expect(html).toContain('Registry authentication required for 2 containers.')
  expect(html).toContain('Image platform unknown · 2 containers')
  expect(html).toContain('<p role="status" class="sr-only">Java4Shell check finished.</p>')
  expect(html).toContain('aria-expanded="false"')
  expect(html).not.toContain('aria-label="Unassessed containers by workload"')
})

it('explains how to follow up when one workload hits the Docker Hub cap', () => {
  const workload = { kind: 'Deployment', group: 'apps', namespace: 'payments', name: 'api' }
  const coverage: Java4ShellResult['coverage'] = { status: 'complete', scope: { type: 'namespaces' }, gaps: [] }
  const client = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  client.setQueryData([
    'java4shell', getApiBase(), 'fixture',
    '/vulnerabilities/java4shell?workloadKind=Deployment&workloadGroup=apps&workloadNamespace=payments&workloadName=api',
  ], {
    findings: [], containers: 1, matches: 0, findingGroups: 0, unassessed: { docker_hub_limit: 1 },
    unassessedRows: [{ workload, container: 'app', image: 'docker.io/library/fixture', pods: 1, reason: 'docker_hub_limit' }],
    unassessedGroups: 1, coverage, workload,
  } satisfies Java4ShellResult)
  const html = renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <Java4ShellPanel context="fixture" connected workload={workload} />
    </QueryClientProvider>,
  )

  expect(html).toContain('Check scope</span> · payments/api')
  expect(html).toContain('Retry to reuse any image metadata Radar cached. If the cap is reached again, verify Java in the skipped containers.')
  expect(html).toContain('Show skipped images')
})

it('shows instance-type inference alongside unknown CPU and non-x86 placement', () => {
  const result: Java4ShellResult = {
    findings: [{
      workload: { kind: 'Deployment', group: 'apps', namespace: 'team', name: 'api' },
      container: 'app',
      image: 'example.com/team/api',
      digest: 'sha256:fixture',
      version: 'jdk-21.0.8+9',
      pods: 4,
      vbmiPods: 0,
      unknownCPUPods: 1,
      noVBMIByInstanceType: { 'm6a.large': 2 },
      mitigationDeclaredPods: 0,
    }],
    containers: 4,
    matches: 4,
    findingGroups: 1,
    unassessed: {},
    unassessedRows: [],
    unassessedGroups: 0,
    coverage: { status: 'complete', scope: { type: 'all_namespaces' }, read: { type: 'all_namespaces' }, gaps: [] },
  }
  const client = new QueryClient({ defaultOptions: { queries: { enabled: false } } })
  client.setQueryData(['java4shell', getApiBase(), 'fixture', '/vulnerabilities/java4shell'], result)
  const html = renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <Java4ShellPanel context="fixture" connected onNavigateToResource={() => {}} />
    </QueryClientProvider>,
  )
  expect(html).toContain('2 Pods on m6a.large · VBMI absent (inferred from instance type)')
  expect(html).toContain('CPU capability unknown for 1 Pod')
  expect(html).toContain('1 Pod on non-x86 nodes at check time')
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
