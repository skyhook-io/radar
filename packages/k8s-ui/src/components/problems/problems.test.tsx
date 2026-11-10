import { describe, expect, expectTypeOf, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProblemMeta, WorkspaceProblemCallout, ProblemList, problemOriginLabel, type WorkspaceProblem } from './problems'

const details = (kind: string) => ({
  id: 'p',
  tone: 'degraded' as const,
  category: 'availability',
  title: 'Something needs a look',
  subject: { kind, group: 'example.io', namespace: 'ns', name: 'child-1' },
})
const problem = (kind: string): Extract<WorkspaceProblem, { source: 'issue' }> => ({ ...details(kind), source: 'issue' })

describe('ProblemMeta', () => {
  it('names the subject only when it is not the workspace root kind', () => {
    expect(renderToStaticMarkup(<ProblemMeta problem={problem('Backup')} rootKind="Cluster" />)).toContain('Backup')
    expect(renderToStaticMarkup(<ProblemMeta problem={problem('Cluster')} rootKind="Cluster" />)).not.toContain('child-1')
  })
})

describe('problemOriginLabel', () => {
  it('says who measured a measurement, and never calls an issue generic', () => {
    expect(problemOriginLabel({ ...details('Cluster'), source: 'measurement', measuredBy: 'Prometheus' }).label).toBe('Measured by Prometheus')
    expect(problemOriginLabel(problem('Cluster')).label).toBe('Detected by Radar')
  })
})

it('keeps original scheduler evidence behind a closed disclosure in callouts and lists', () => {
  const p = { ...problem('Pod'), detail: 'both nodes have reached their Pod limit', rawDetailLabel: 'Scheduler message', rawDetail: '0/2 nodes are available: 2 Too many pods.' }
  for (const node of [<WorkspaceProblemCallout problem={p} rootKind="Cluster" />, <ProblemList problems={[p]} rootKind="Cluster" />]) {
    const html = renderToStaticMarkup(node)
    expect(html).toContain(p.detail)
    expect(html).toContain(p.rawDetail)
    expect(html).toContain('Scheduler message')
    expect(html).toContain('aria-expanded="false"')
  }
  expect(renderToStaticMarkup(<WorkspaceProblemCallout problem={problem('Pod')} rootKind="Cluster" />)).not.toContain('Scheduler message')
})

it('uses a generic label for raw evidence from another controller', () => {
  const p = { ...problem('Deployment'), rawDetail: 'Progress deadline exceeded.' }
  expect(renderToStaticMarkup(<WorkspaceProblemCallout problem={p} rootKind="Application" />)).toContain('Original evidence')
})

it('keeps source-specific provenance fields exclusive in the public type', () => {
  type Details = ReturnType<typeof details>
  expectTypeOf<Details & { source: 'issue'; measuredBy: string }>().not.toMatchTypeOf<WorkspaceProblem>()
  expectTypeOf<Details & { source: 'measurement'; origin: { label: string } }>().not.toMatchTypeOf<WorkspaceProblem>()
  expectTypeOf<Details & { source: 'audit'; origin: { label: string } }>().not.toMatchTypeOf<WorkspaceProblem>()
})

it('preserves alert and unknown tones in callouts and compact lists', () => {
  for (const tone of ['alert', 'unknown'] as const) {
    const p = { ...problem('Pod'), tone }
    const callout = renderToStaticMarkup(<WorkspaceProblemCallout problem={p} rootKind="Cluster" />)
    expect(callout).toContain(`status-${tone}`)
    const list = renderToStaticMarkup(<ProblemList problems={[p]} rootKind="Cluster" />)
    expect(list).toContain(tone === 'alert' ? 'bg-orange-500' : 'bg-slate-400')
    expect(list).toContain(tone === 'alert' ? 'text-orange-600' : 'text-theme-text-tertiary')
  }
  type HealthyProblem = Omit<ReturnType<typeof details>, 'tone'> & { source: 'issue'; tone: 'healthy' }
  expectTypeOf<HealthyProblem>().not.toMatchTypeOf<WorkspaceProblem>()
})
