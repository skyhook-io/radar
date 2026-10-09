import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProblemMeta, ProblemCallout, ProblemList, problemOriginLabel, type WorkspaceProblem } from './problems'

const problem = (kind: string): WorkspaceProblem => ({
  id: 'p',
  severity: 'warning',
  category: 'availability',
  title: 'Something needs a look',
  subject: { kind, group: 'example.io', namespace: 'ns', name: 'child-1' },
  source: 'issue',
})

describe('ProblemMeta', () => {
  it('names the subject only when it is not the workspace root kind', () => {
    expect(renderToStaticMarkup(<ProblemMeta problem={problem('Backup')} rootKind="Cluster" />)).toContain('Backup')
    expect(renderToStaticMarkup(<ProblemMeta problem={problem('Cluster')} rootKind="Cluster" />)).not.toContain('child-1')
  })
})

describe('problemOriginLabel', () => {
  it('says who measured a measurement, and never calls an issue generic', () => {
    expect(problemOriginLabel({ ...problem('Cluster'), source: 'measurement', measuredBy: 'Prometheus' }).label).toBe('Measured by Prometheus')
    expect(problemOriginLabel(problem('Cluster')).label).toBe('Detected by Radar')
  })
})

it('keeps original scheduler evidence behind a closed disclosure in callouts and lists', () => {
  const p = { ...problem('Pod'), detail: 'both nodes have reached their Pod limit', rawDetail: '0/2 nodes are available: 2 Too many pods.' }
  for (const node of [<ProblemCallout problem={p} rootKind="Cluster" />, <ProblemList problems={[p]} rootKind="Cluster" />]) {
    const html = renderToStaticMarkup(node)
    expect(html).toContain(p.detail)
    expect(html).toContain(p.rawDetail)
    expect(html).toContain('Scheduler message')
    expect(html).toContain('aria-expanded="false"')
  }
  expect(renderToStaticMarkup(<ProblemCallout problem={problem('Pod')} rootKind="Cluster" />)).not.toContain('Scheduler message')
})
