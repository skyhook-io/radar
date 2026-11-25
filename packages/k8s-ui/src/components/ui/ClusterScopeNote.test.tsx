import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ClusterScopeNote } from './ClusterScopeNote'
import type { ClusterScope, ClusterScopeState } from '../../types/cluster-scope'

const scope = (state: ClusterScopeState): ClusterScope => ({ state, note: `note for ${state}` })

describe('ClusterScopeNote', () => {
  it('renders nothing when the view was kept to this cluster or is single-cluster', () => {
    for (const state of ['scoped_configured', 'scoped_verified', 'single_cluster_asserted', 'unlabeled'] as const) {
      expect(renderToStaticMarkup(<ClusterScopeNote scope={scope(state)} />)).toBe('')
    }
  })

  it('renders nothing for an older Radar that sends no scope', () => {
    expect(renderToStaticMarkup(<ClusterScopeNote />)).toBe('')
    expect(renderToStaticMarkup(<ClusterScopeNote scope={null} />)).toBe('')
  })

  it('shows the server note when the numbers were withheld', () => {
    for (const state of ['several_clusters', 'label_missing', 'unknown'] as const) {
      const html = renderToStaticMarkup(<ClusterScopeNote scope={scope(state)} title="Recommendations withheld" />)
      expect(html).toContain(`note for ${state}`)
      expect(html).toContain('Recommendations withheld')
      expect(html).toContain('role="note"')
    }
  })
})
