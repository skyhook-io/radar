import { describe, expect, it } from 'vitest'
import { buildArgoResourceSyncVars } from './client'

const options = {
  revision: 'should-not-survive',
  prune: true,
  dryRun: false,
  force: true,
  applyOnly: true,
  syncOptions: ['ServerSideApply=true'],
}

describe('buildArgoResourceSyncVars', () => {
  it('preserves the complete Argo status ref and forces resource-safe options', () => {
    expect(
      buildArgoResourceSyncVars(
        'argocd',
        'guestbook',
        {
          group: 'apps',
          kind: 'Deployment',
          namespace: 'guestbook',
          name: 'guestbook-ui',
        },
        options,
      ),
    ).toEqual({
      namespace: 'argocd',
      name: 'guestbook',
      resources: [
        {
          group: 'apps',
          kind: 'Deployment',
          namespace: 'guestbook',
          name: 'guestbook-ui',
        },
      ],
      revision: undefined,
      prune: false,
      dryRun: false,
      force: true,
      applyOnly: false,
      syncOptions: ['ServerSideApply=true'],
    })
  })

  it('preserves an empty core API group', () => {
    const variables = buildArgoResourceSyncVars(
      'argocd',
      'guestbook',
      {
        group: '',
        kind: 'Service',
        namespace: 'guestbook',
        name: 'guestbook-ui',
      },
      options,
    )

    expect(variables.resources).toEqual([
      {
        group: '',
        kind: 'Service',
        namespace: 'guestbook',
        name: 'guestbook-ui',
      },
    ])
  })
})
