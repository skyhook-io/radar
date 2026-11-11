import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceRendererDispatch, getResourceStatus } from '../../shared/ResourceRendererDispatch'
import { getCellFilterValue } from '../resource-utils'
import type { CertificateSigningRequestDetails } from '../../../types'

const approved = { type: 'Approved', status: 'True', reason: 'AutoApproved', message: 'Auto approving kubelet client certificate after SubjectAccessReview.', lastTransitionTime: '2026-10-20T10:00:00Z' }

function csr(status: Record<string, unknown>, apiVersion = 'certificates.k8s.io/v1') {
  return {
    apiVersion,
    kind: 'CertificateSigningRequest',
    metadata: { name: 'node-csr-worker-1', creationTimestamp: '2026-10-20T09:59:58Z' },
    spec: {
      request: 'LS0tLS1CRUdJTiBDRVJUSUZJQ0FURSBSRVFVRVNULS0tLS0K',
      signerName: 'kubernetes.io/kube-apiserver-client-kubelet',
      usages: ['digital signature', 'client auth'],
      username: 'system:bootstrap:abcdef',
      uid: 'f1d2c3b4',
      groups: ['system:bootstrappers', 'system:bootstrappers:kubeadm:default-node-token', 'system:authenticated'],
      extra: { 'authentication.kubernetes.io/credential-id': ['JTI=abc'] },
      collisionProbe: 'collision-probe-value',
    },
    status,
  }
}

const decodedRequest: CertificateSigningRequestDetails['request'] = {
  status: 'decoded',
  subject: 'CN=system:node:worker-1,O=system:nodes',
  commonName: 'system:node:worker-1',
  organizations: ['system:nodes'],
  publicKey: 'EC P-256',
  signatureAlgorithm: 'ECDSA-SHA256',
}

const issuedCertificate: CertificateSigningRequestDetails['certificate'] = {
  status: 'decoded',
  certificates: [{
    subject: 'system:node:worker-1',
    subjectDN: 'CN=system:node:worker-1,O=system:nodes',
    issuer: 'kubernetes',
    keyType: 'EC P-256',
    serialNumber: '1A2B',
    notBefore: '2026-10-20T09:55:00Z',
    notAfter: '2027-10-20T09:55:00Z',
    daysLeft: 361,
  }],
}

function render(data: any, details?: CertificateSigningRequestDetails): string {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind: 'certificatesigningrequests', namespace: '', name: data.metadata.name }}
      data={data}
      certificateSigningRequest={details}
      onCopy={() => {}}
      copied={null}
      showCommonSections={false}
    />,
  )
}

describe('CertificateSigningRequestRenderer', () => {
  it('separates the requester from the requested identity', () => {
    const html = render(csr({}), { request: decodedRequest, certificate: { status: 'absent' } })
    expect(html).toContain('Pending approval')
    expect(html).toContain('kubernetes.io/kube-apiserver-client-kubelet')
    expect(html).toContain('client auth')
    expect(html).toContain('system:bootstrap:abcdef')
    expect(html).toContain('system:bootstrappers')
    expect(html).toContain('authentication.kubernetes.io/credential-id')
    expect(html).toContain('Requested Identity')
    expect(html).toContain('system:node:worker-1')
    expect(html).toContain('It is not proof of what an approver')
    expect(html).not.toContain('Issued Certificate')
  })

  it('does not present approval as issuance', () => {
    const html = render(csr({ conditions: [approved] }), { request: decodedRequest, certificate: { status: 'absent' } })
    expect(html).toContain('Approved, not issued')
    expect(html).toContain('Approval alone does not issue one')
    expect(html).toContain('AutoApproved')
    expect(html).not.toContain('Issued Certificate')
  })

  it('shows the issued certificate once approved and returned', () => {
    const html = render(csr({ certificate: 'Y2VydA==', conditions: [approved] }), { request: decodedRequest, certificate: issuedCertificate })
    expect(html).toContain('Issued Certificate')
    expect(html).toContain('CN=system:node:worker-1,O=system:nodes')
    expect(html).toContain('1A2B')
    expect(html).not.toContain('Approved, not issued')
  })

  it.each([
    ['denied', { conditions: [{ type: 'Denied', status: 'True', reason: 'KubectlDeny', message: 'denied by an admin' }] }, 'Denied', 'KubectlDeny: denied by an admin'],
    ['failed', { conditions: [approved, { type: 'Failed', status: 'True', reason: 'SignerValidationFailure', message: 'invalid usage' }] }, 'Signing failed', 'SignerValidationFailure: invalid usage'],
  ])('explains a %s request with its reason', (_name, status, title, message) => {
    const html = render(csr(status), { request: decodedRequest, certificate: { status: 'absent' } })
    expect(html).toContain(title)
    expect(html).toContain(message)
  })

  it('warns about a certificate on a request that was never approved', () => {
    const html = render(csr({ certificate: 'Y2VydA==' }), { request: decodedRequest, certificate: issuedCertificate })
    expect(html).toContain('Certificate without approval')
    expect(html).toContain('Returned Certificate')
    expect(html).not.toContain('Issued Certificate')
  })

  it('reports malformed payloads instead of an empty state', () => {
    const html = render(csr({ certificate: 'Y2VydA==', conditions: [approved] }), {
      request: { status: 'invalid', error: 'spec.request holds no PEM block' },
      certificate: { status: 'invalid', problems: ['PEM block 1 is not a valid certificate: x509: malformed certificate'] },
    })
    expect(html).toContain('The request could not be decoded')
    expect(html).toContain('spec.request holds no PEM block')
    expect(html).toContain('The returned certificate could not be read')
    expect(html).toContain('PEM block 1 is not a valid certificate')
    expect(html).not.toContain('Certificate Info')
  })

  it('shows what it could read from partially unreadable certificate data', () => {
    const html = render(csr({ certificate: 'Y2VydA==', conditions: [approved] }), {
      request: decodedRequest,
      certificate: { ...issuedCertificate, status: 'partial', problems: ['PEM block 2 is a CERTIFICATE REQUEST, not a certificate'] },
    })
    expect(html).toContain('Part of the returned certificate data could not be read')
    expect(html).toContain('PEM block 2 is a CERTIFICATE REQUEST, not a certificate')
    expect(html).toContain('Issued Certificate')
  })

  it('reports a request that is not signed by its own key', () => {
    const html = render(csr({}), { request: { ...decodedRequest, signatureError: 'x509: ECDSA verification failure' }, certificate: { status: 'absent' } })
    expect(html).toContain('Signature does not verify')
    expect(html).toContain('x509: ECDSA verification failure')
  })

  it('does not call SANs of an unreadable type absent', () => {
    const unreadable = render(csr({}), { request: { ...decodedRequest, unreadableSANs: true }, certificate: { status: 'absent' } })
    const none = render(csr({}), { request: decodedRequest, certificate: { status: 'absent' } })
    expect(none).toContain('None requested')
    expect(unreadable).not.toContain('None requested')
  })

  it('says when the payloads were not decoded rather than calling them missing', () => {
    const html = render(csr({ certificate: 'Y2VydA==', conditions: [approved] }))
    expect(html).toContain('this Radar version does not decode certificate requests')
    expect(html).toContain('this Radar version does not decode returned certificates')
    expect(html).not.toContain('No request data')
  })

  it('leaves a foreign CRD sharing the plural to the generic renderer', () => {
    const html = render(csr({ conditions: [approved] }, 'example.com/v1'), { request: decodedRequest, certificate: { status: 'absent' } })
    expect(html).toContain('collision-probe-value')
    expect(html).not.toContain('Requested Identity')
  })
})

describe('CertificateSigningRequest status', () => {
  it('reads the same state in the drawer header and the table filter', () => {
    const request = csr({ conditions: [approved] })
    expect(getResourceStatus('certificatesigningrequests', request)).toMatchObject({ text: 'Approved, not issued', level: 'degraded' })
    expect(getCellFilterValue(request, 'status', 'certificatesigningrequests')).toBe('Approved, not issued')
  })

  it('does not describe a foreign CRD in CSR terms', () => {
    const foreign = csr({ conditions: [approved] }, 'example.com/v1')
    expect(getResourceStatus('certificatesigningrequests', foreign)?.text).not.toBe('Approved, not issued')
    expect(getCellFilterValue(foreign, 'status', 'certificatesigningrequests')).not.toBe('Approved, not issued')
  })
})
