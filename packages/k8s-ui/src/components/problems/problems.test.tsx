import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProblemMeta, problemOriginLabel, type WorkspaceProblem } from './problems'

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
