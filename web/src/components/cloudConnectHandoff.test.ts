import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import { handoffForBlocked, handoffForPrepareError, isHandoffOutcome, signupUrlFor } from './cloudConnectHandoff'

const APP = 'https://app.test.example'

describe('signupUrlFor', () => {
  it('tags the link and omits the outcome when there was no attempt', () => {
    expect(signupUrlFor(APP, 'driver-footer-browser-link')).toBe(
      `${APP}/signup?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal&utm_content=driver-footer-browser-link`,
    )
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

describe('handoffForPrepareError', () => {
  it('separates no cluster, inspection failed, Radar unreachable, and an unusable response', () => {
    expect(handoffForPrepareError(new ApiError('down', 503)).outcome).toBe('radar_not_connected_to_cluster')
    expect(handoffForPrepareError(new ApiError('nope', 500)).outcome).toBe('cluster_inspect_failed')
    expect(handoffForPrepareError(new TypeError('Failed to fetch')).outcome).toBe('radar_server_unreachable')
    expect(handoffForPrepareError(new SyntaxError('Unexpected token <')).outcome).toBe('cluster_inspect_request_error')
  })

  it('treats every prepare error as worth trying again', () => {
    for (const err of [new ApiError('down', 503), new ApiError('nope', 500), new TypeError('x'), new Error('y')]) {
      expect(handoffForPrepareError(err).retryable).toBe(true)
    }
  })
})

describe('handoffForBlocked', () => {
  it('names the refusal and does not offer a retry', () => {
    expect(handoffForBlocked('gitops')).toEqual({ outcome: 'blocked_gitops_managed_install', retryable: false })
    expect(handoffForBlocked('preflight')).toEqual({ outcome: 'blocked_preflight_checks_failed', retryable: false })
    expect(handoffForBlocked('unsupported')).toEqual({ outcome: 'blocked_unsupported_install', retryable: false })
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
