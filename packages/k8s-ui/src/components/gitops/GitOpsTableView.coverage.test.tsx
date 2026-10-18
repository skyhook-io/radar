import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsTableView } from './GitOpsTableView'

const render = (props: Partial<Parameters<typeof GitOpsTableView>[0]> = {}) =>
  renderToString(<GitOpsTableView rows={[]} counts={{}} onRowClick={() => {}} {...props} />)

describe('GitOpsTableView coverage', () => {
  it('shows the onboarding state to a host that passes no coverage, as before coverage', () => {
    expect(render()).toContain('No GitOps resources detected')
    expect(render({ coverageStatus: 'complete' })).toContain('No GitOps resources detected')
  })
  it('keeps the unqualified labels and empty text for unknown coverage', () => {
    const html = render({ coverageStatus: 'unknown', counts: { 'argoproj.io/Application': 2 } })
    expect(html).toContain('Total Applications')
    expect(html).toContain('No applications found.')
    expect(html).not.toContain('Partial results')
  })
  it('says what a complete empty answer covers', () => {
    expect(render({ coverageStatus: 'complete', counts: { 'argoproj.io/Application': 2 } })).toContain('No applications found in the requested scope.')
  })
  it('does not call a partial answer empty', () => {
    const html = render({ coverageStatus: 'partial' })
    expect(html).not.toContain('No GitOps resources detected')
    expect(html).toContain('Partial results')
  })
})
