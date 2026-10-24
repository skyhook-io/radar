import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ArgoApplicationRenderer } from './ArgoApplicationRenderer'

describe('Argo Helm value files', () => {
  it.each([true, false, undefined])('shows missing-file behavior for single and multiple sources: %s', optional => {
    const source = { chart: 'cluster', repoURL: 'https://cloudnative-pg.github.io/charts', helm: { valueFiles: ['values.yaml', 'overrides.yaml'], ignoreMissingValueFiles: optional } }
    for (const spec of [{ source }, { sources: [source] }]) {
      const html = renderToStaticMarkup(<ArgoApplicationRenderer data={{ metadata: { name: 'app' }, spec }} />)
      expect(html).toContain('values.yaml, overrides.yaml')
      expect(html).toContain('If a Value File Is Missing')
      expect(html).toContain(optional ? 'Skipped' : 'Render fails')
    }
  })
})
