import { describe, test, expect } from 'vitest'

import {
  formatGitOpsDestination,
  formatGitOpsSourceUrl,
  getGitOpsTool,
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
  test('"0" returns null (non-positive, Argo history starts at 1)', () => {
    expect(parseArgoRollbackID('0')).toBeNull()
  })
  test('negative numbers return null', () => {
    expect(parseArgoRollbackID('-5')).toBeNull()
  })
  test('Flux condition type string returns null', () => {
    expect(parseArgoRollbackID('Ready')).toBeNull()
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
  // Group is the canonical discriminator.
  test('Argo via group', () => {
    expect(getGitOpsTool('applications', 'argoproj.io')).toBe('argo')
    expect(getGitOpsTool('applicationsets', 'argoproj.io')).toBe('argo')
    expect(getGitOpsTool('appprojects', 'argoproj.io')).toBe('argo')
  })
  // Argo kinds without an explicit group still route to 'argo'.
  // Defends against a normalizer that drops `group` from the frontend payload.
  test('Argo kinds without group still route to argo', () => {
    expect(getGitOpsTool('applications', undefined)).toBe('argo')
    expect(getGitOpsTool('applicationsets', '')).toBe('argo')
  })
  // Flux kinds with their canonical groups.
  test('Flux via group', () => {
    expect(getGitOpsTool('kustomizations', 'kustomize.toolkit.fluxcd.io')).toBe('flux')
    expect(getGitOpsTool('helmreleases', 'helm.toolkit.fluxcd.io')).toBe('flux')
  })
  // Unknown kinds default to flux (matches the OSS fall-through).
  test('unknown kind defaults to flux', () => {
    expect(getGitOpsTool('somecustomresource', 'unknown.io')).toBe('flux')
  })
})
