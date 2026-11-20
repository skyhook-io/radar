import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { getApiBase } from '../../api/config'
import type { Java4ShellResult } from '../../api/vulnerabilities'
import { Java4ShellPanel } from './Java4ShellPanel'

it('shows the Java home count before opening details and counts other failures once', () => {
  const result: Java4ShellResult = {
    findings: [],
    containers: 6,
    matches: 0,
    findingGroups: 0,
    unassessed: { java_home_declared: 3, auth_required: 2, no_java_declaration: 1 },
    unassessedRows: [],
    unassessedGroups: 3,
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
  expect(html).toContain('Java version unassessed in 2 containers.')
  expect(html).toContain('aria-expanded="false"')
  expect(html).not.toContain('aria-label="Unassessed containers by workload"')
})
