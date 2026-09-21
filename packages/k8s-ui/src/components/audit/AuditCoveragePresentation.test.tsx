import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { AuditCard } from './AuditCard'
import { ChecksView } from '../checks/ChecksView'
import { FilterLocationProvider } from '../../filter-state'

describe('audit coverage presentation', () => {
  for (const [passing, missingInputs, expected] of [
    [0, [], 'No resources evaluated'],
    [0, ['secrets'], 'Some checks could not run'],
    [4, ['secrets'], 'Some checks could not run'],
    [4, [], 'All checks passing'],
  ] as const) {
    it(`shows ${expected} with ${passing} passing and ${missingInputs.length} unavailable inputs`, () => {
      const html = renderToString(<AuditCard data={{ passing, missingInputs: [...missingInputs], warning: 0, danger: 0, categories: {} }} onNavigate={() => {}} />)
      expect(html).toContain(expected)
      if (missingInputs.length || passing === 0) expect(html).not.toContain('All checks passing')
    })
  }
  it('keeps a coverage warning alongside real findings', () => {
    const html = renderToString(<AuditCard data={{ passing: 1, missingInputs: ['secrets'], warning: 1, danger: 0, categories: { Security: { passing: 1, warning: 0, danger: 0 } } }} onNavigate={() => {}} />)
    expect(html).toContain('Some checks could not run')
    expect(html).not.toContain('All passing')
  })
  for (const [evaluated, missingInputs, expected] of [
    [0, [], 'No resources evaluated'],
    [0, ['secrets'], 'No findings in the available data'],
    [4, ['secrets'], 'No findings in the available data'],
    [4, [], 'Every audited resource passed'],
  ] as const) {
    it(`Checks: ${expected}`, () => {
      const html = renderToString(
        <FilterLocationProvider value={{ searchParams: new URLSearchParams(), update: () => {} }}>
          <ChecksView checks={[]} catalog={{}} anyData evaluated={evaluated} missingInputs={[...missingInputs]} />
        </FilterLocationProvider>,
      )
      expect(html).toContain(expected)
      if (missingInputs.length) {
        expect(html).toContain('Unavailable inputs (')
        expect(html).toContain('Secret')
        expect(html).not.toContain('Every audited resource passed')
      }
    })
  }
})

describe('replica placement coverage explanation', () => {
  for (const inputs of [['replicasets'], ['replicaset-ownership'], ['replicasets', 'replicaset-ownership', 'secrets']]) {
    it(`names the unevaluated check for ${inputs.join(', ')}`, () => {
      const html = renderToString(
        <FilterLocationProvider value={{ searchParams: new URLSearchParams(), update: () => {} }}>
          <ChecksView checks={[]} catalog={{}} anyData evaluated={0} missingInputs={inputs} />
        </FilterLocationProvider>,
      )
      expect(html).toContain('Running replicas on same node')
      expect(html).toContain('could not be fully evaluated')
      expect(html).toContain('affected Deployments were skipped, not passed')
      expect(html).toContain('No findings in the available data')
      expect(html).not.toContain('replicaset-ownership')
      if (inputs.includes('replicasets')) expect(html).toContain('ReplicaSet')
      if (inputs.includes('replicaset-ownership')) expect(html).toContain('ReplicaSet ownership')
    })
  }
  it('does not imply missing replica evidence for unrelated unavailable inputs', () => {
    const html = renderToString(
      <FilterLocationProvider value={{ searchParams: new URLSearchParams(), update: () => {} }}>
        <ChecksView checks={[]} catalog={{}} anyData evaluated={4} missingInputs={['secrets']} />
      </FilterLocationProvider>,
    )
    expect(html).toContain('Unavailable inputs (')
    expect(html).toContain('Secret')
    expect(html).not.toContain('Running replicas on same node')
    expect(html).not.toContain('skipped, not passed')
  })
})


describe('unavailable input details', () => {
  it('keeps a long input list collapsed with readable kinds and preserves unknown reasons', () => {
    const html = renderToString(
      <FilterLocationProvider value={{ searchParams: new URLSearchParams(), update: () => {} }}>
        <ChecksView checks={[]} catalog={{}} anyData evaluated={0}
          missingInputs={['serviceaccounts', 'horizontalpodautoscalers', 'limitranges', 'poddisruptionbudgets', 'configmap-references', 'secret-references', 'unrecognized-input']} />
      </FilterLocationProvider>,
    )
    expect(html).toContain('Unavailable inputs (7)')
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('ServiceAccount, HorizontalPodAutoscaler, LimitRange, PodDisruptionBudget, ConfigMap references, Secret references, unrecognized-input')
    expect(html).not.toContain('Every audited resource passed')
  })
})
