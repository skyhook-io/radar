import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  getCertificateSigningRequestReason,
  getCertificateSigningRequestState,
  getCertificateSigningRequestStatus,
  isCertificateSigningRequest,
} from './resource-utils-csr'

const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../../pkg/csr/testdata/states.json', import.meta.url)), 'utf8'))

describe('CertificateSigningRequest state shared Go/TS vectors', () => {
  it('loads the vectors', () => {
    expect(fixture.vectors.length).toBeGreaterThan(0)
  })
  for (const vector of fixture.vectors) {
    it(vector.name, () => {
      const state = getCertificateSigningRequestState(vector.csr)
      const badge = getCertificateSigningRequestStatus(vector.csr)
      expect({
        state: state.state,
        label: badge.text,
        level: badge.level,
        conditionType: state.condition?.type ?? '',
        certificatePresent: state.certificatePresent,
      }).toEqual({
        state: vector.state,
        label: vector.label,
        level: vector.level,
        conditionType: vector.conditionType,
        certificatePresent: vector.certificatePresent,
      })
    })
  }
})

describe('getCertificateSigningRequestState', () => {
  it('keeps the approval decision and the signer failure apart', () => {
    const state = getCertificateSigningRequestState({
      apiVersion: 'certificates.k8s.io/v1',
      kind: 'CertificateSigningRequest',
      status: {
        conditions: [
          { type: 'Approved', status: 'True', reason: 'KubectlApprove', lastTransitionTime: '2026-10-20T10:00:00Z' },
          { type: 'Failed', status: 'True', reason: 'SignerValidationFailure', message: 'invalid usage: server auth' },
        ],
      },
    })
    expect(state.state).toBe('failed')
    expect(state.decision).toMatchObject({ type: 'Approved', reason: 'KubectlApprove', lastTransitionTime: '2026-10-20T10:00:00Z' })
    expect(state.failure).toMatchObject({ type: 'Failed', reason: 'SignerValidationFailure' })
  })

  it('reads a missing or malformed status as pending, not issued', () => {
    for (const csr of [undefined, {}, { status: null }, { status: 'x' }, { status: { conditions: 'x', certificate: null } }]) {
      expect(getCertificateSigningRequestState(csr)).toMatchObject({ state: 'pending', certificatePresent: false })
    }
  })
})

describe('getCertificateSigningRequestReason', () => {
  it('joins the deciding condition reason and message', () => {
    expect(getCertificateSigningRequestReason({
      status: { conditions: [{ type: 'Denied', status: 'True', reason: 'KubectlDeny', message: 'denied by an admin' }] },
    })).toBe('KubectlDeny: denied by an admin')
    expect(getCertificateSigningRequestReason({ status: {} })).toBeUndefined()
  })
})

describe('isCertificateSigningRequest', () => {
  it('claims only certificates.k8s.io', () => {
    expect(isCertificateSigningRequest({ apiVersion: 'certificates.k8s.io/v1', kind: 'CertificateSigningRequest' })).toBe(true)
    expect(isCertificateSigningRequest({ apiVersion: 'example.com/v1', kind: 'CertificateSigningRequest' })).toBe(false)
    expect(isCertificateSigningRequest({ apiVersion: 'certificates.k8s.io/v1', kind: 'PodCertificateRequest' })).toBe(false)
  })
})
