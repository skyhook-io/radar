import { describe, expect, it } from 'vitest'
import { ApiError, type CloudInstallAttempted } from '../api/client'
import { composeAdminNote, exitFor, handoffForBlocked, handoffForPrepareError, isHandoffOutcome, signupUrlFor } from './cloudConnectHandoff'

const APP = 'https://app.test.example'
const UTM = 'utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal'
const CARD = 'driver-blocked-card-browser-link'

const attempted = (over: Partial<CloudInstallAttempted>): CloudInstallAttempted => ({
  mode: 'fresh',
  namespace: 'radar',
  release: 'radar',
  stage: 'preflight',
  ...over,
})

describe('signupUrlFor', () => {
  it('tags the link and omits the outcome when there was no attempt', () => {
    expect(signupUrlFor(APP, 'driver-footer-browser-link')).toBe(`${APP}/signup?${UTM}&utm_content=driver-footer-browser-link`)
    expect(signupUrlFor(APP, 'driver-footer-browser-link', null)).not.toContain('radar_outcome')
  })

  it('appends a well-formed outcome after an attempt', () => {
    expect(signupUrlFor(APP, 'driver-footer-browser-link', { outcome: 'hub_connect_request_failed', retryable: true })).toMatch(
      /utm_content=driver-footer-browser-link&radar_outcome=hub_connect_request_failed$/,
    )
  })

  it('never forwards an outcome outside the vocabulary shape', () => {
    // A server message or a cluster name must not ride along in the link.
    for (const bad of ['connect failed: dial tcp 10.0.0.1', 'Prod-East', '', 'a'.repeat(41), 'x&y=1']) {
      expect(signupUrlFor(APP, 'driver-footer-browser-link', { outcome: bad, retryable: true })).not.toContain('radar_outcome')
    }
  })
})

describe('exitFor — every blocked card has one exit, chosen by what Radar established', () => {
  it('adopts a release Radar found, pinned to the Helm tab', () => {
    const h = handoffForBlocked('preflight', attempted({ mode: 'adopt', namespace: 'monitoring', release: 'radar-prod', stage: 'inspect' }))
    const exit = exitFor(APP, CARD, h)
    expect(exit).toMatchObject({ install: true, label: 'Get the install command' })
    expect(exit.href).toBe(
      `${APP}/install?existing=1&ns=monitoring&release=radar-prod&method=helm&${UTM}&utm_content=${CARD}&radar_outcome=blocked_preflight_checks_failed`,
    )
  })

  it('offers a fresh install only from a complete plan whose discovery saw the whole cluster', () => {
    const fresh = exitFor(APP, CARD, handoffForBlocked('preflight', attempted({})))
    expect(fresh).toMatchObject({ install: true, label: 'Get the install command' })
    expect(fresh.href).toContain('/install?method=helm&')
    expect(fresh.href).not.toContain('existing')

    // A partial scan may have missed a Radar elsewhere; guessing fresh would
    // reset its values, so the exit is Radar Cloud itself.
    const partial = exitFor(APP, CARD, handoffForBlocked('preflight', attempted({ partialScan: true })))
    expect(partial).toMatchObject({ install: false, label: 'Open Radar Cloud' })
    expect(partial.href).toContain('/signup?')
    expect(partial.href).toContain('radar_outcome=blocked_preflight_checks_failed')
  })

  it('offers a fresh install when nothing is running and only the release records were unread', () => {
    const exit = exitFor(APP, CARD, handoffForBlocked('preflight', attempted({ stage: 'inspect', releaseUnread: true })))
    expect(exit).toMatchObject({ install: true, label: 'Get the install command' })
    expect(exit.href).toContain('/install?method=helm&')
  })

  it('sends an unknown target to Radar Cloud, never to a guessed install', () => {
    const exit = exitFor(APP, CARD, handoffForBlocked('preflight'))
    expect(exit).toMatchObject({ install: false, label: 'Open Radar Cloud' })
    expect(exit.href).toContain('/signup?')
  })

  it('hands a GitOps-managed release to its verified controller’s tab', () => {
    for (const [method, label] of [
      ['argocd', 'Get the Argo CD values patch'],
      ['flux', 'Get the Flux values patch'],
    ] as const) {
      const exit = exitFor(APP, CARD, handoffForBlocked('gitops', attempted({ mode: 'gitops', namespace: 'radar', release: 'radar', stage: 'inspect', method })))
      expect(exit).toMatchObject({ install: true, label })
      expect(exit.href).toContain(`/install?existing=1&ns=radar&release=radar&method=${method}&`)
    }
    // An unverified or unrecognized owner gets no patch for the wrong tool.
    const unknownOwner = exitFor(APP, CARD, handoffForBlocked('gitops', attempted({ mode: 'gitops', stage: 'inspect', method: '' })))
    expect(unknownOwner).toMatchObject({ install: false, label: 'Open Radar Cloud' })
  })

  it('never turns an unsupported refusal into an install, even with a known release', () => {
    // Preparation can refuse after inspection found a release (incompatible
    // settings, a token Secret already present); the card still keeps that
    // as evidence, but the exit is Radar Cloud, not an install over it.
    const exit = exitFor(APP, CARD, handoffForBlocked('unsupported', attempted({ mode: 'adopt', stage: 'prepare' })))
    expect(exit).toMatchObject({ install: false, label: 'Open Radar Cloud' })
    expect(exit.href).toContain('radar_outcome=blocked_unsupported_install')
    expect(exitFor(APP, CARD, handoffForBlocked('unsupported'))).toMatchObject({ install: false, label: 'Open Radar Cloud' })
  })
})

describe('handoffForPrepareError', () => {
  it('separates no cluster, inspection failed, Radar unreachable, and an unusable response', () => {
    expect(handoffForPrepareError(new ApiError('down', 503)).outcome).toBe('radar_not_connected_to_cluster')
    expect(handoffForPrepareError(new ApiError('nope', 500)).outcome).toBe('cluster_inspect_failed')
    expect(handoffForPrepareError(new TypeError('Failed to fetch')).outcome).toBe('radar_server_unreachable')
    expect(handoffForPrepareError(new SyntaxError('Unexpected token <')).outcome).toBe('cluster_inspect_request_error')
  })

  it('is worth trying again and keeps the message for the pitch', () => {
    const h = handoffForPrepareError(new ApiError('Not connected to cluster', 503))
    expect(h.retryable).toBe(true)
    expect(h.detail).toBe('Not connected to cluster')
  })
})

describe('handoffForBlocked', () => {
  it('names the refusal, does not offer a retry, and keeps what Radar attempted', () => {
    expect(handoffForBlocked('gitops')).toEqual({ outcome: 'blocked_gitops_managed_install', retryable: false, target: null })
    expect(handoffForBlocked('preflight')).toEqual({ outcome: 'blocked_preflight_checks_failed', retryable: false, target: null })
    expect(handoffForBlocked('unsupported')).toEqual({ outcome: 'blocked_unsupported_install', retryable: false, target: null })
    const a = attempted({ mode: 'adopt', partialScan: false })
    expect(handoffForBlocked('preflight', a).target).toBe(a)
  })
})

describe('isHandoffOutcome', () => {
  it('accepts the server failure kinds verbatim', () => {
    for (const kind of [
      'hub_connect_request_failed',
      'hub_approval_poll_failed',
      'approval_rejected_in_browser',
      'approval_window_expired',
      'approved_but_credential_pickup_expired',
      'approval_outcome_unknown',
      'canceled_before_approval_page',
      'canceled_after_approval',
      'helm_provision_failed',
      'installed_but_tunnel_not_confirmed',
    ]) {
      expect(isHandoffOutcome(kind)).toBe(true)
    }
  })
})

describe('composeAdminNote — an ask, then the link, then the evidence', () => {
  const where = { context: 'prod-east', cluster: 'arn:aws:eks:us-east-1:1:cluster/prod-east' }

  it('asks for a permissions block, naming the identity and trimming the refusal to what and why', () => {
    const blocked = {
      reason: 'preflight' as const,
      cause: 'permissions' as const,
      identity: 'system:serviceaccount:default:limited',
      message: '',
      attempted: attempted({ stage: 'inspect', releaseUnread: true }),
      blocking: [
        'inspect Helm release "radar" in namespace "radar": failed to inspect existing release: query: failed to query with labels: secrets is forbidden: User "system:serviceaccount:default:limited" cannot list resource "secrets" in API group "" in the namespace "radar"',
      ],
    }
    const exit = exitFor(APP, CARD, handoffForBlocked('preflight', blocked.attempted))
    const note = composeAdminNote(blocked, exit, where)
    expect(note).toMatch(/^Request to connect cluster prod-east to Radar Cloud\n/)
    expect(note).toContain("the identity in use (system:serviceaccount:default:limited) doesn't have the permissions to install it")
    expect(note).toContain('Nothing in the cluster was changed. Could someone with cluster access connect it?')
    // The link a person reads carries only what the page needs — no attribution.
    // The link a person reads keeps what the page needs to land prefilled,
    // plus one marker that the arrival came by handoff — no utm noise.
    expect(note).toContain('Open Radar Cloud to get the install command (sign in, name the cluster, pick Helm / Argo CD / Flux):\n' + `${APP}/install?method=helm&via=admin_handoff`)
    expect(note).not.toContain('utm_')
    expect(note).not.toContain('radar_outcome')
    expect(note).toContain("Details: No Radar install was found in the cluster; the check stopped while reading Helm's release records.")
    expect(note).toContain('inspect Helm release "radar" in namespace "radar" — User "system:serviceaccount:default:limited" cannot list resource "secrets" in API group "" in the namespace "radar".')
    expect(note).toContain('Please confirm nothing is already installed before a fresh install.')
    // Never the card's second person.
    expect(note).not.toMatch(/\byour\b/i)
  })

  it('asks for a GitOps values patch from the verified tool', () => {
    const blocked = { reason: 'gitops' as const, message: 'managed by Flux', attempted: attempted({ mode: 'gitops', stage: 'inspect', method: 'flux' }) }
    const exit = exitFor(APP, CARD, handoffForBlocked('gitops', blocked.attempted))
    const note = composeAdminNote(blocked, exit, where)
    expect(note).toContain('the install is managed by Flux, so connecting it is a values change in the repository')
    expect(note).toContain('Open Radar Cloud to get the values patch for Flux (sign in, name the cluster')
    expect(note).toContain(`${APP}/install?existing=1&ns=radar&release=radar&method=flux&via=admin_handoff`)
    expect(note).toContain('Details: The release radar in namespace radar is managed by Flux.')
  })

  it('quotes the refusal and sends the admin to Radar Cloud when Radar had no target', () => {
    const blocked = { reason: 'unsupported' as const, message: 'Multiple Radar installations were found in this cluster.  Use `radar cloud install` to pick one.' }
    const exit = exitFor(APP, CARD, handoffForBlocked('unsupported'))
    const note = composeAdminNote(blocked, exit, { cluster: 'kind-dev' })
    expect(note).toContain('Request to connect cluster kind-dev to Radar Cloud')
    expect(note).toContain('Radar reported: Multiple Radar installations were found in this cluster. Use `radar cloud install` to pick one.')
    expect(note).toContain('Could someone with cluster access connect it from Radar Cloud?')
    expect(note).toContain('Open Radar Cloud:\n' + `${APP}/signup?via=admin_handoff`)
  })

  it('keeps the whole line when a refusal has no error chain to trim', () => {
    const blocked = {
      reason: 'preflight' as const,
      cause: 'cluster' as const,
      message: '',
      attempted: attempted({}),
      blocking: ['create Deployment "radar" in namespace "radar": an object already exists but is not owned by the current Helm release'],
    }
    const note = composeAdminNote(blocked, exitFor(APP, CARD, handoffForBlocked('preflight', blocked.attempted)))
    expect(note).toContain('the cluster refused part of the install — a policy, or something already there (details below)')
    expect(note).toContain('Details: No Radar install was found in the cluster; the dry run of the install stopped. create Deployment "radar" in namespace "radar": an object already exists but is not owned by the current Helm release.')
  })
})
