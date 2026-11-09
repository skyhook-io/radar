import { describe, test, expect } from 'vitest'

import {
  formatGitOpsDestination,
  formatGitOpsSourceUrl,
  getGitOpsResourceStatus,
  getGitOpsTool,
  gitOpsDetailGroup,
  isDestinationRef,
  parseArgoRollbackID,
} from './detail-helpers'

describe('parseArgoRollbackID', () => {
  // The function exists specifically to defend against three foot-guns
  // that would each route to a wrong rollback target without it. Pins
  // them so a future "simplification" can't regress.
  test('undefined returns null', () => {
    expect(parseArgoRollbackID(undefined)).toBeNull()
  })
  test('empty string returns null (would be Number("") === 0)', () => {
    expect(parseArgoRollbackID('')).toBeNull()
  })
  // Argo numbers history from 0, so the first deploy is a rollback target.
  test('"0" is the first history entry', () => {
    expect(parseArgoRollbackID('0')).toBe(0)
  })
  test('negative numbers return null', () => {
    expect(parseArgoRollbackID('-5')).toBeNull()
  })
  test('Flux condition type string returns null', () => {
    expect(parseArgoRollbackID('Ready')).toBeNull()
  })
  // Number() reads each of these as a number; with 0 now valid, a blank id
  // would otherwise target the first deploy.
  test.each([' ', '0x1f', '1e3', '1.5', '+1', ' 1'])('%j returns null', (id) => {
    expect(parseArgoRollbackID(id)).toBeNull()
  })
  test('positive integer returns the number', () => {
    expect(parseArgoRollbackID('42')).toBe(42)
  })
})

describe('formatGitOpsDestination', () => {
  // The literal Argo controllers write for the in-cluster destination.
  test('canonical in-cluster URL collapses to "in-cluster"', () => {
    expect(formatGitOpsDestination('https://kubernetes.default.svc', 'prod')).toBe('in-cluster, Namespace: prod')
  })
  // Argo 2.13 writes a trailing slash. Must normalize equivalently.
  test('in-cluster URL with trailing slash collapses to "in-cluster"', () => {
    expect(formatGitOpsDestination('https://kubernetes.default.svc/', 'prod')).toBe('in-cluster, Namespace: prod')
  })
  // Empty/whitespace-only server — Argo's "default when unspecified"
  // semantics target the controller's own cluster.
  test('empty server renders as "in-cluster"', () => {
    expect(formatGitOpsDestination('', 'prod')).toBe('in-cluster, Namespace: prod')
    expect(formatGitOpsDestination('   ', 'prod')).toBe('in-cluster, Namespace: prod')
  })
  // External destination URLs render with protocol stripped — keeps the
  // chip readable (no redundant "https://" prefix when every URL has it).
  test('external server strips protocol prefix', () => {
    expect(formatGitOpsDestination('https://prod.k8s.example.com', 'web')).toBe('prod.k8s.example.com, Namespace: web')
    expect(formatGitOpsDestination('http://insecure.example.com', 'web')).toBe('insecure.example.com, Namespace: web')
  })
  // No namespace → host alone.
  test('namespace omitted when undefined', () => {
    expect(formatGitOpsDestination('https://kubernetes.default.svc', undefined)).toBe('in-cluster')
  })
})

describe('formatGitOpsSourceUrl', () => {
  test('strips https://', () => {
    expect(formatGitOpsSourceUrl('https://github.com/org/repo')).toBe('github.com/org/repo')
  })
  test('strips http://', () => {
    expect(formatGitOpsSourceUrl('http://gitlab.example.com/repo')).toBe('gitlab.example.com/repo')
  })
  // SSH-style + on-prem mirrors don't carry the http(s) prefix; preserve
  // the user's literal so they recognize their own config.
  test('SSH origins pass through unchanged', () => {
    expect(formatGitOpsSourceUrl('git@github.com:org/repo.git')).toBe('git@github.com:org/repo.git')
  })
})

describe('getGitOpsTool', () => {
  // The API group decides, the same rule as the backend (pkg/gitops ToolFor).
  test('Argo CD kinds in argoproj.io', () => {
    expect(getGitOpsTool('applications', 'argoproj.io')).toBe('argo')
    expect(getGitOpsTool('applicationsets', 'argoproj.io')).toBe('argo')
    expect(getGitOpsTool('appprojects', 'argoproj.io')).toBe('argo')
  })
  test('Flux groups', () => {
    expect(getGitOpsTool('kustomizations', 'kustomize.toolkit.fluxcd.io')).toBe('flux')
    expect(getGitOpsTool('helmreleases', 'helm.toolkit.fluxcd.io')).toBe('flux')
    expect(getGitOpsTool('gitrepositories', 'source.toolkit.fluxcd.io')).toBe('flux')
  })
  // SIG Apps defines its own Application kind; offering Argo Sync on it
  // would sync a same-named argoproj.io Application instead.
  test('an Application outside argoproj.io is not Argo CD', () => {
    expect(getGitOpsTool('applications', 'app.k8s.io')).toBeNull()
    expect(getGitOpsTool('applications', 'source.toolkit.fluxcd.io')).toBeNull()
    expect(getGitOpsTool('kustomizations', 'source.toolkit.fluxcd.io')).toBeNull()
    expect(getGitOpsTool('helmreleases', 'kustomize.toolkit.fluxcd.io')).toBeNull()
  })
  // argoproj.io is shared with Argo Rollouts and Workflows.
  test('other argoproj.io kinds are not Argo CD', () => {
    expect(getGitOpsTool('rollouts', 'argoproj.io')).toBeNull()
    expect(getGitOpsTool('workflows', 'argoproj.io')).toBeNull()
  })
  // The kind alone is not identity, and nothing defaults to Flux.
  test('no group or an unknown group is not GitOps', () => {
    expect(getGitOpsTool('applications', undefined)).toBeNull()
    expect(getGitOpsTool('applicationsets', '')).toBeNull()
    expect(getGitOpsTool('somecustomresource', 'unknown.io')).toBeNull()
    expect(getGitOpsTool('applications', 'notargoproj.io')).toBeNull()
    expect(getGitOpsTool('kustomizations', 'fake.kustomize.toolkit.fluxcd.io')).toBeNull()
  })
})

describe('gitOpsDetailGroup', () => {
  test('the loaded object\'s apiVersion wins over the route group', () => {
    expect(gitOpsDetailGroup('argoproj.io', { apiVersion: 'app.k8s.io/v1beta1' })).toBe('app.k8s.io')
  })
  test('the route group stands in until the object loads', () => {
    expect(gitOpsDetailGroup('app.k8s.io', undefined)).toBe('app.k8s.io')
    expect(gitOpsDetailGroup('argoproj.io', {})).toBe('argoproj.io')
  })
})

describe('getGitOpsResourceStatus - ApplicationSet', () => {
  // An ApplicationSet reports through ErrorOccurred / ParametersGenerated /
  // ResourcesUpToDate and never sets Ready. Routed to the Flux reader - which
  // looks for Ready - every one of these cases collapses to Unknown, and a
  // generator that is failing outright reads exactly like a healthy one.
  const appSet = (conditions: Array<Record<string, string>>) => ({
    status: { conditions },
  })

  test('a failed generator is Degraded, not Unknown', () => {
    const status = getGitOpsResourceStatus('applicationsets', appSet([
      { type: 'ErrorOccurred', status: 'True', message: 'error generating params from git' },
      { type: 'ResourcesUpToDate', status: 'False', reason: 'ErrorOccurred' },
    ]))
    expect(status?.health).toBe('Degraded')
    expect(status?.message).toBe('error generating params from git')
  })

  test('a generator that produced its Applications is Healthy', () => {
    const status = getGitOpsResourceStatus('applicationsets', appSet([
      { type: 'ResourcesUpToDate', status: 'True', reason: 'ApplicationSetUpToDate' },
      { type: 'ParametersGenerated', status: 'True', reason: 'ParametersGenerated' },
    ]))
    expect(status?.health).toBe('Healthy')
  })

  test('no conditions yet is Unknown', () => {
    expect(getGitOpsResourceStatus('applicationsets', appSet([]))?.health).toBe('Unknown')
  })

  // Sync answers "is the declared state applied". An ApplicationSet applies
  // nothing - it generates Applications, each of which carries its own sync.
  // Unknown is the honest answer in every case, including the healthy one.
  test('sync stays Unknown regardless of health', () => {
    expect(getGitOpsResourceStatus('applicationsets', appSet([
      { type: 'ResourcesUpToDate', status: 'True' },
    ]))?.sync).toBe('Unknown')
    expect(getGitOpsResourceStatus('applicationsets', appSet([
      { type: 'ErrorOccurred', status: 'True', message: 'boom' },
    ]))?.sync).toBe('Unknown')
  })

  // ErrorOccurred=False is the steady state of a working generator; only
  // status 'True' means a fault.
  test('ErrorOccurred=False does not count as a fault', () => {
    const status = getGitOpsResourceStatus('applicationsets', appSet([
      { type: 'ErrorOccurred', status: 'False' },
      { type: 'ResourcesUpToDate', status: 'True' },
    ]))
    expect(status?.health).toBe('Healthy')
  })
})

describe('isDestinationRef', () => {
  const node = (ref: { group?: string; kind: string; namespace: string; name: string }, remote?: boolean) =>
    ({ id: `${ref.kind}/${ref.name}`, ref, role: 'declared' as const, tool: 'fluxcd' as const, remote })
  const root = node({ group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'fleet-prod' })
  const tree = {
    root,
    nodes: [
      root,
      node({ group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'billing' }, true),
      node({ group: 'source.toolkit.fluxcd.io', kind: 'GitRepository', namespace: 'flux-system', name: 'fleet' }),
    ],
    edges: [],
    remoteDestination: true,
  }

  test('a local tree has no destination refs', () => {
    expect(isDestinationRef(tree, false, { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'billing' })).toBe(false)
  })

  test('declared resources are remote; the root and sources are local', () => {
    expect(isDestinationRef(tree, true, { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'billing' })).toBe(true)
    expect(isDestinationRef(tree, true, root.ref)).toBe(false)
    expect(isDestinationRef(tree, true, { group: 'source.toolkit.fluxcd.io', kind: 'GitRepository', namespace: 'flux-system', name: 'fleet' })).toBe(false)
  })

  test('a ref the tree does not list is remote, even before the tree loads', () => {
    expect(isDestinationRef(tree, true, { kind: 'Deployment', name: 'billing' })).toBe(true)
    expect(isDestinationRef(null, true, { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'billing' })).toBe(true)
  })
})

describe('GitOps status identity', () => {
  test.each([
    ['applications', 'app.k8s.io/v1beta1'],
    ['applicationsets', 'vendor.io/v1'],
    ['kustomizations', 'source.toolkit.fluxcd.io/v1'],
    ['helmreleases', 'vendor.helm.toolkit.fluxcd.io/v1'],
  ])('does not interpret foreign %s status from %s', (kind, apiVersion) => {
    expect(getGitOpsResourceStatus(kind, {
      apiVersion,
      status: { sync: { status: 'Synced' }, health: { status: 'Healthy' }, conditions: [{ type: 'Ready', status: 'True' }] },
    })).toBeNull()
  })

  test('preserves existing controller-specific status-only callers', () => {
    expect(getGitOpsResourceStatus('applications', { status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } } })?.sync).toBe('Synced')
    expect(getGitOpsResourceStatus('kustomizations', { status: { conditions: [{ type: 'Ready', status: 'True' }] } })?.health).toBe('Healthy')
  })

  test('does not interpret an AppProject as a Flux resource', () => {
    expect(getGitOpsResourceStatus('appprojects', { apiVersion: 'argoproj.io/v1alpha1', status: { conditions: [{ type: 'Ready', status: 'True' }] } })).toBeNull()
  })
})
