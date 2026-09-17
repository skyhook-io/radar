import { describe, expect, it } from 'vitest'
import { ApiError, type CloudInstallAttempted } from '../api/client'
import { exitFor, handoffForBlocked, handoffForPrepareError, isHandoffOutcome, signupUrlFor } from './cloudConnectHandoff'

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
