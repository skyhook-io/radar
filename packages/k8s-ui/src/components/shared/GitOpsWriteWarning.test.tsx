import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { evaluateGitOpsWriteGuard, type GitOpsWriteGuardInput } from '../../utils/gitops-write-guard'
import { GitOpsWriteWarning } from './GitOpsWriteWarning'

const base: GitOpsWriteGuardInput = {
  target: { kind: 'Deployment', group: 'apps', namespace: 'prod', name: 'api' },
  owner: { tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'api' },
  writes: [{ scope: 'spec', paths: ['spec.replicas'] }],
  evidence: {
    uid: 'u',
    resourceVersion: '1',
    owner: null,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null },
    paths: [{ path: 'spec.replicas', lastApplied: 'present', ownedBy: [], ownedByGitOps: false }],
  },
}

function render(input: GitOpsWriteGuardInput, onOpenOwner?: () => void) {
  return renderToStaticMarkup(
    <GitOpsWriteWarning
      guard={evaluateGitOpsWriteGuard(input)}
      acknowledged={false}
      onAcknowledgedChange={() => {}}
      onOpenOwner={onOpenOwner}
    />,
  )
}

describe('GitOpsWriteWarning', () => {
  it('renders the revert warning, owner link and required acknowledgment', () => {
    const html = render(base, () => {})
    expect(html).toContain('Argo CD Application argocd/api will revert this change.')
    expect(html).toContain('overwritten at the next sync')
    expect(html).toContain('change it in the GitOps source')
    expect(html).toContain('View Argo CD Application')
    expect(html).toContain('type="checkbox"')
    expect(html).toContain('I understand Argo CD may revert this.')
  })

  it('shows a checking state while ownership resolves', () => {
    const html = render({ ...base, ownerPending: true })
    expect(html).toContain('Checking GitOps ownership…')
    expect(html).not.toContain('type="checkbox"')
  })

  it('renders a quiet note without acknowledgment at info level', () => {
    const html = render({ ...base, writes: [{ scope: 'create-child' }] })
    expect(html).toContain('Managed by Argo CD Application argocd/api.')
    expect(html).not.toContain('type="checkbox"')
  })

  it('leaves vertical spacing to the parent', () => {
    for (const input of [base, { ...base, ownerPending: true }, { ...base, writes: [{ scope: 'create-child' as const }] }]) {
      const root = render(input).match(/^<div class="([^"]*)"/)?.[1] ?? ''
      expect(root).not.toMatch(/(^|\s)m[bt]-/)
    }
  })

  it('renders nothing when unmanaged', () => {
    expect(render({ ...base, owner: null })).toBe('')
  })

  it('lists per-write reasons when they differ', () => {
    const html = render({
      ...base,
      writes: [
        { scope: 'spec', paths: ['spec.replicas'], description: 'Replicas' },
        { scope: 'delete', description: 'Delete' },
      ],
      evidence: { ...base.evidence!, policy: { tool: 'argocd', auto: false, selfHeal: false, prune: false, suspended: null } },
    })
    expect(html).toContain('Replicas:')
    expect(html).toContain('Delete:')
  })
})
