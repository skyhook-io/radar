import { describe, test, expect } from 'vitest'

import { destinationNamespaceLabel, hasSyncPolicy, normalizeArgoApplicationSet, normalizeArgoApplication } from './GitOpsTableView'

// Mirrors scripts/gitops-demo/05-argo-applicationset.yaml: a list generator,
// so nothing about the deployed source is discoverable from the generator.
const listGeneratorSet = {
  metadata: { name: 'radar-demo-set', namespace: 'argocd', creationTimestamp: '2026-01-01T00:00:00Z' },
  spec: {
    generators: [{ list: { elements: [{ name: 'vanilla', path: 'guestbook' }] } }],
    template: {
      spec: {
        project: 'radar-demo',
        source: { repoURL: 'https://github.com/argoproj/argocd-example-apps', targetRevision: 'HEAD', path: '{{path}}' },
        destination: { server: 'https://kubernetes.default.svc', namespace: 'demo-set' },
        syncPolicy: { automated: { prune: true, selfHeal: true } },
      },
    },
  },
  status: { conditions: [{ type: 'ResourcesUpToDate', status: 'True' }] },
}

describe('normalizeArgoApplicationSet source and project', () => {
  // The source the generated Applications deploy from lives on the template.
  // List, cluster and SCM generators carry no repo at all, so the template is
  // the only place it can come from for those.
  test('reads the repo from the template, not the generator', () => {
    const row = normalizeArgoApplicationSet(listGeneratorSet)
    expect(row.repository).toBe('https://github.com/argoproj/argocd-example-apps')
    expect(row.targetRevision).toBe('HEAD')
  })

  // A templated value only resolves per generated Application, so the literal
  // "{{path}}" would be noise in the Source column.
  test('blanks a templated path rather than printing the placeholder', () => {
    expect(normalizeArgoApplicationSet(listGeneratorSet).path).toBe('')
  })

  test('reads a literal project from the template', () => {
    expect(normalizeArgoApplicationSet(listGeneratorSet).project).toBe('radar-demo')
  })

  test('blanks a templated project', () => {
    const templated = structuredClone(listGeneratorSet)
    templated.spec.template.spec.project = '{{cluster.name}}'
    expect(normalizeArgoApplicationSet(templated).project).toBe('')
  })

  // A git generator's repo is scanned for parameters, not necessarily deployed
  // from. It stays available for templates that carry no literal source.
  test('falls back to the git generator when the template has no source', () => {
    const gitGen: any = structuredClone(listGeneratorSet)
    delete gitGen.spec.template.spec.source
    gitGen.spec.generators = [{ git: { repoURL: 'https://github.com/org/infra', revision: 'main', directories: [{ path: 'apps/prod' }] } }]
    const row = normalizeArgoApplicationSet(gitGen)
    expect(row.repository).toBe('https://github.com/org/infra')
    expect(row.targetRevision).toBe('main')
    expect(row.path).toBe('apps/prod')
  })

  // A glob is how git-generator directories are normally written, and it is a
  // real literal - not a per-app placeholder - so it survives.
  test('keeps a directory glob from the git generator', () => {
    const gitGen: any = structuredClone(listGeneratorSet)
    delete gitGen.spec.template.spec.source
    gitGen.spec.generators = [{ git: { repoURL: 'https://github.com/org/infra', directories: [{ path: 'apps/*' }] } }]
    expect(normalizeArgoApplicationSet(gitGen).path).toBe('apps/*')
  })

  test('a set with neither template source nor git generator stays blank', () => {
    const bare: any = structuredClone(listGeneratorSet)
    delete bare.spec.template.spec.source
    const row = normalizeArgoApplicationSet(bare)
    expect(row.repository).toBe('')
    expect(row.path).toBe('')
  })
})

describe('hasSyncPolicy', () => {
  // The Automation facet answers "what is not auto-syncing". An ApplicationSet
  // never syncs, so counting it as Manual puts objects in that answer that
  // were never going to sync.
  test('an ApplicationSet has no sync policy of its own', () => {
    expect(hasSyncPolicy(normalizeArgoApplicationSet(listGeneratorSet))).toBe(false)
  })

  test('an Application does', () => {
    const app = normalizeArgoApplication({
      metadata: { name: 'guestbook', namespace: 'argocd' },
      spec: { project: 'default', source: {}, destination: {}, syncPolicy: { automated: {} } },
      status: {},
    })
    expect(hasSyncPolicy(app)).toBe(true)
  })
})

describe('normalizeArgoApplicationSet destination', () => {
  // A cluster generator templates the destination by definition. Rendering
  // "{{server}}" in the Destination column is the same class of noise as a
  // templated path, and the table shows a dash for an empty destination.
  test('blanks a templated destination server', () => {
    const clusterGen = structuredClone(listGeneratorSet)
    clusterGen.spec.template.spec.destination = { server: '{{server}}', namespace: 'demo-set' } as any
    const row = normalizeArgoApplicationSet(clusterGen)
    expect(row.destination).toBe('')
    expect(row.destinationNamespace).toBe('demo-set')
  })

  test('keeps a literal destination server', () => {
    expect(normalizeArgoApplicationSet(listGeneratorSet).destination).toBe('https://kubernetes.default.svc')
  })

  test('blanks a templated destination namespace', () => {
    const templatedNs = structuredClone(listGeneratorSet)
    templatedNs.spec.template.spec.destination = { server: 'https://kubernetes.default.svc', namespace: '{{ns}}' } as any
    expect(normalizeArgoApplicationSet(templatedNs).destinationNamespace).toBe('')
  })
})

describe('normalizeArgoApplicationSet last activity', () => {
  // The Last Sync column falls back to the creation timestamp when this is
  // empty, which would date the object rather than report activity on it.
  test('carries the condition transition time, not the creation time', () => {
    const withTime = structuredClone(listGeneratorSet)
    withTime.metadata.creationTimestamp = '2026-01-01T00:00:00Z'
    withTime.status.conditions = [
      { type: 'ResourcesUpToDate', status: 'True', lastTransitionTime: '2026-06-15T09:30:00Z' },
    ] as any
    expect(normalizeArgoApplicationSet(withTime).lastSync).toBe('2026-06-15T09:30:00Z')
  })

  test('a failing generator reports when it failed', () => {
    const failed = structuredClone(listGeneratorSet)
    failed.status.conditions = [
      { type: 'ErrorOccurred', status: 'True', message: 'boom', lastTransitionTime: '2026-06-15T10:00:00Z' },
    ] as any
    expect(normalizeArgoApplicationSet(failed).lastSync).toBe('2026-06-15T10:00:00Z')
  })
})

describe('source is taken from one place, never mixed', () => {
  // A template repo paired with a git generator's path would name a tree that
  // exists in neither the generated Application nor the generator.
  test('a literal template repo does not borrow the generator path', () => {
    const mixed: any = structuredClone(listGeneratorSet)
    mixed.spec.template.spec.source = { repoURL: 'https://github.com/org/deploy', path: '{{path}}' }
    mixed.spec.generators = [{ git: { repoURL: 'https://github.com/org/config', revision: 'main', directories: [{ path: 'apps/*' }] } }]
    const row = normalizeArgoApplicationSet(mixed)
    expect(row.repository).toBe('https://github.com/org/deploy')
    expect(row.path).toBe('')
    expect(row.targetRevision).toBe('')
  })

  test('with no literal template repo the generator supplies all three', () => {
    const gen: any = structuredClone(listGeneratorSet)
    delete gen.spec.template.spec.source
    gen.spec.generators = [{ git: { repoURL: 'https://github.com/org/config', revision: 'main', directories: [{ path: 'apps/*' }] } }]
    const row = normalizeArgoApplicationSet(gen)
    expect(row.repository).toBe('https://github.com/org/config')
    expect(row.targetRevision).toBe('main')
    expect(row.path).toBe('apps/*')
  })
})

describe('destinationNamespaceLabel', () => {
  // An ApplicationSet lives in the controller namespace and deploys elsewhere,
  // so naming its own namespace as the destination would be a real-looking
  // value that is simply wrong.
  test('an ApplicationSet with a templated namespace resolves to nothing', () => {
    const templatedNs: any = structuredClone(listGeneratorSet)
    templatedNs.spec.template.spec.destination = { server: 'https://kubernetes.default.svc', namespace: '{{ns}}' }
    expect(destinationNamespaceLabel(normalizeArgoApplicationSet(templatedNs))).toBe('')
  })

  test('a literal namespace is used as-is', () => {
    expect(destinationNamespaceLabel(normalizeArgoApplicationSet(listGeneratorSet))).toBe('demo-set')
  })

  test('an Application still falls back to its own namespace', () => {
    const app = normalizeArgoApplication({
      metadata: { name: 'guestbook', namespace: 'argocd' },
      spec: { project: 'default', source: {}, destination: {} },
      status: {},
    })
    expect(destinationNamespaceLabel(app)).toBe('argocd')
  })
})

describe('a declared template source claims the row even when unresolved', () => {
  // A templated repoURL still means "the generated Applications deploy from
  // here". Falling back would present the generator's own tree as the
  // deployed one.
  test('a fully templated template source does not fall back to the generator', () => {
    const templatedRepo: any = structuredClone(listGeneratorSet)
    templatedRepo.spec.template.spec.source = { repoURL: '{{url}}', targetRevision: '{{rev}}', path: '{{path}}' }
    templatedRepo.spec.generators = [{ git: { repoURL: 'https://github.com/org/config', revision: 'main', directories: [{ path: 'apps/*' }] } }]
    const row = normalizeArgoApplicationSet(templatedRepo)
    expect(row.repository).toBe('')
    expect(row.targetRevision).toBe('')
    expect(row.path).toBe('')
  })

  // Multi-source is unrendered across this view, so a sources-only template
  // reads blank rather than borrowing the generator.
  test('a multi-source template does not fall back to the generator', () => {
    const multi: any = structuredClone(listGeneratorSet)
    delete multi.spec.template.spec.source
    multi.spec.template.spec.sources = [{ repoURL: 'https://github.com/org/a', path: 'x' }]
    multi.spec.generators = [{ git: { repoURL: 'https://github.com/org/config', directories: [{ path: 'apps/*' }] } }]
    const row = normalizeArgoApplicationSet(multi)
    expect(row.repository).toBe('')
    expect(row.path).toBe('')
  })
})
