import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { AnalysisRunRenderer, getAnalysisRunKargoStage } from './AnalysisRunRenderer'
import { getAnalysisRunStatus, summarizeAnalysisMetrics } from '../resource-utils'

// SSR splits interpolated text with <!-- --> markers; strip so assertions read as prose.
function render(element: Parameters<typeof renderToString>[0]): string {
  return renderToString(element).replace(/<!-- -->/g, '')
}

function analysisRun(overrides: {
  phase?: string
  message?: string
  labels?: Record<string, string>
  metricResults?: any[]
  runSummary?: Record<string, number>
  metrics?: any[]
}) {
  return {
    metadata: {
      name: 'web-6c4f-2',
      namespace: 'prod',
      labels: overrides.labels,
      ownerReferences: [{ kind: 'Rollout', name: 'web' }],
    },
    spec: { metrics: overrides.metrics ?? [] },
    status: {
      phase: overrides.phase,
      message: overrides.message,
      metricResults: overrides.metricResults,
      runSummary: overrides.runSummary,
    },
  }
}

describe('AnalysisRunRenderer verdict banner', () => {
  it('escalates Failed to an error banner naming the abort consequence', () => {
    const html = render(
      <AnalysisRunRenderer
        data={analysisRun({
          phase: 'Failed',
          metricResults: [{ name: 'success-rate', phase: 'Failed', message: 'result 0 breached fail if result == 0' }],
        })}
      />,
    )
    expect(html).toContain('the rollout will be aborted')
    expect(html).toContain('result 0 breached fail if result == 0')
  })

  it('distinguishes Error (could not run) from Failed (metric breached)', () => {
    const html = render(<AnalysisRunRenderer data={analysisRun({ phase: 'Error' })} />)
    expect(html).toContain('Analysis could not run')
    expect(html).not.toContain('the rollout will be aborted')
  })

  it('frames Inconclusive as awaiting a human decision, not a failure', () => {
    const html = render(
      <AnalysisRunRenderer
        data={analysisRun({
          phase: 'Inconclusive',
          metricResults: [{ name: 'success-rate', phase: 'Inconclusive', message: 'result 2 met neither condition' }],
        })}
      />,
    )
    expect(html).toContain('paused for a human decision')
    expect(html).toContain('result 2 met neither condition')
  })

  it('falls back to a generic reason when no metric carried a message', () => {
    const html = render(
      <AnalysisRunRenderer data={analysisRun({ phase: 'Inconclusive', metricResults: [{ name: 'm', phase: 'Inconclusive' }] })} />,
    )
    expect(html).toContain('none met its success condition either')
  })

  it('renders no banner for a run still in flight', () => {
    for (const phase of ['Pending', 'Running', 'Successful']) {
      const html = render(<AnalysisRunRenderer data={analysisRun({ phase })} />)
      expect(html).not.toContain('paused for a human decision')
      expect(html).not.toContain('the rollout will be aborted')
      expect(html).not.toContain('Analysis could not run')
    }
  })
})

describe('AnalysisRunRenderer for a Kargo verification', () => {
  const kargoRun = (phase: string, metadata: Record<string, unknown>) => ({
    metadata: { name: 'uat.01m8fe7sen59dej60m59knkjfw.7c6082a', namespace: 'kargo-demo', ownerReferences: [{ kind: 'Freight', name: 'b56f6e3' }], ...metadata },
    status: { phase, message: 'Metric "error-rate" assessed Failed due to failed (1) > failureLimit (0)' },
  })

  it('names the Kargo stage instead of a rollout, and links to it', () => {
    const opened: unknown[] = []
    const run = kargoRun('Failed', { labels: { 'kargo.akuity.io/stage': 'uat' } })
    const html = render(<AnalysisRunRenderer data={run} onNavigate={(ref) => opened.push(ref)} />)
    expect(html).toContain('Verification failed for Kargo stage uat')
    expect(html).toContain('Open stage')
    expect(html).not.toContain('rollout')
    expect(getAnalysisRunKargoStage(run)).toBe('uat')
  })

  it('reads the full stage name from the annotation when the label was shortened', () => {
    const run = kargoRun('Error', {
      labels: { 'kargo.akuity.io/stage': 'shortened-abc123' },
      annotations: { 'kargo.akuity.io/stage': 'a-very-long-stage-name' },
    })
    expect(render(<AnalysisRunRenderer data={run} />)).toContain('run for Kargo stage a-very-long-stage-name')
  })

  it('does not promise a rollout abort for a run no Rollout owns', () => {
    const html = render(<AnalysisRunRenderer data={{ metadata: { name: 'ar', namespace: 'ns' }, status: { phase: 'Failed' } }} />)
    expect(html).toContain('Analysis failed')
    expect(html).not.toContain('rollout')
  })
})

describe('AnalysisRunRenderer detail', () => {
  it('surfaces the trigger and canary step the controller labelled the run with', () => {
    const html = render(
      <AnalysisRunRenderer
        data={analysisRun({ phase: 'Running', labels: { 'rollout-type': 'Step', 'step-index': '3' } })}
      />,
    )
    expect(html).toContain('Triggered as')
    expect(html).toContain('Step')
    expect(html).toContain('Canary step')
  })

  it('pairs each metric result with its condition from the spec', () => {
    const html = render(
      <AnalysisRunRenderer
        data={analysisRun({
          phase: 'Inconclusive',
          metrics: [
            { name: 'success-rate', successCondition: 'result == 1', failureCondition: 'result == 0', failureLimit: 2 },
          ],
          metricResults: [
            {
              name: 'success-rate',
              phase: 'Inconclusive',
              count: 3,
              inconclusive: 3,
              measurements: [{ value: '2', phase: 'Inconclusive' }],
            },
          ],
        })}
      />,
    )
    expect(html).toContain('success if: result == 1')
    expect(html).toContain('fail if: result == 0')
    expect(html).toContain('failure limit 2')
    expect(html).toContain('latest: 2')
    expect(html).toContain('3 measured')
    expect(html).toContain('3 inconclusive')
  })

  it('flags a dry-run metric so its phase is not read as load-bearing', () => {
    const html = render(
      <AnalysisRunRenderer
        data={analysisRun({ phase: 'Successful', metricResults: [{ name: 'canary-error-rate', phase: 'Failed', dryRun: true }] })}
      />,
    )
    expect(html).toContain('(dry-run)')
  })

  it('renders the tally and metric sections only when populated', () => {
    const bare = render(<AnalysisRunRenderer data={analysisRun({ phase: 'Pending' })} />)
    expect(bare).not.toContain('Measurement Tally')
    expect(bare).not.toContain('Metrics (')

    const full = render(
      <AnalysisRunRenderer
        data={analysisRun({ phase: 'Successful', runSummary: { count: 4, successful: 4 }, metricResults: [{ name: 'm', phase: 'Successful' }] })}
      />,
    )
    expect(full).toContain('Measurement Tally')
    expect(full).toContain('Metrics (1)')
  })

  it('renders without a status, spec, or owner', () => {
    expect(() => render(<AnalysisRunRenderer data={{ metadata: { name: 'ar' } }} />)).not.toThrow()
  })
})

describe('getAnalysisRunStatus', () => {
  // Inconclusive is the alert tier: worse than Running, not as bad as Failed.
  it('maps each phase to the right health tier', () => {
    const cases: Array<[string | undefined, string]> = [
      ['Successful', 'healthy'],
      ['Running', 'degraded'],
      ['Pending', 'degraded'],
      ['Inconclusive', 'alert'],
      ['Failed', 'unhealthy'],
      ['Error', 'unhealthy'],
      [undefined, 'unknown'],
      ['Bogus', 'unknown'],
    ]
    for (const [phase, level] of cases) {
      expect(getAnalysisRunStatus({ status: { phase } }).level).toBe(level)
    }
  })

  it('labels an absent phase Unknown rather than blank', () => {
    expect(getAnalysisRunStatus({}).text).toBe('Unknown')
  })
})

describe('summarizeAnalysisMetrics', () => {
  const run = (...phases: string[]) => ({
    status: { metricResults: phases.map((phase, i) => ({ name: `m${i}`, phase })) },
  })

  it('does not count in-flight metrics as passing', () => {
    expect(summarizeAnalysisMetrics(run('Successful', 'Running', 'Pending'))).toEqual({
      total: 3,
      passing: 1,
      notPassing: 0,
      dryRun: 0,
    })
  })

  it('counts every failure phase as not passing', () => {
    expect(summarizeAnalysisMetrics(run('Failed', 'Error', 'Inconclusive', 'Successful'))).toEqual({
      total: 4,
      passing: 1,
      notPassing: 3,
      dryRun: 0,
    })
  })

  it('reports all passing only once every metric is Successful', () => {
    const { total, passing } = summarizeAnalysisMetrics(run('Successful', 'Successful'))
    expect(passing).toBe(total)
  })

  it('handles a run with no metric results', () => {
    expect(summarizeAnalysisMetrics({})).toEqual({ total: 0, passing: 0, notPassing: 0, dryRun: 0 })
  })

  // Argo excludes dryRun metrics from worstStatus, so a failed dry-run must not
  // paint the row amber while the run itself reports Successful.
  it('excludes dry-run metrics from the verdict counts', () => {
    const run = {
      status: {
        phase: 'Successful',
        metricResults: [
          { name: 'real', phase: 'Successful' },
          { name: 'canary-only', phase: 'Failed', dryRun: true },
        ],
      },
    }
    expect(summarizeAnalysisMetrics(run)).toEqual({
      total: 1,
      passing: 1,
      notPassing: 0,
      dryRun: 1,
    })
  })

  it('still counts a non-dry-run failure alongside a dry-run one', () => {
    const run = {
      status: {
        metricResults: [
          { name: 'real', phase: 'Failed' },
          { name: 'shadow', phase: 'Failed', dryRun: true },
        ],
      },
    }
    const { total, notPassing, dryRun } = summarizeAnalysisMetrics(run)
    expect({ total, notPassing, dryRun }).toEqual({ total: 1, notPassing: 1, dryRun: 1 })
  })
})
