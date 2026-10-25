import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'

import { ManagedByChip } from './ManagedByChip'

const owner = { tool: 'argocd' as const, kind: 'applications' as const, namespace: 'argocd', name: 'billing' }

describe('ManagedByChip', () => {
  it('says Managed by for a verified owner its status confirms', () => {
    const html = renderToString(<ManagedByChip owner={owner} verified listing={{ state: 'confirmed' }} />)
    expect(html).toContain('Managed by')
  })

  it('says Tracked by, with a warning, for an owner whose status does not list the resource while another does', () => {
    const html = renderToString(
      <ManagedByChip owner={owner} verified listing={{ state: 'conflict', listedBy: [{ tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'hub' }] }} />,
    )
    expect(html).toContain('Tracked by')
    expect(html).not.toContain('Managed by')
    expect(html).toMatch(/rounded-full[^"]*bg-amber/)
  })
})
