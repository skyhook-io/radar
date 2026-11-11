import type { ReactNode } from 'react'
import { FileText, KeyRound, Shield, User } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ConditionsSection } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { TimeValue } from '../../ui/ScheduleValue'
import { CertificateInfoSection } from './SecretRenderer'
import { formatDuration } from '../resource-utils'
import { getCertificateSigningRequestState, type CSRCondition, type CSRStatus } from '../resource-utils-csr'
import { formatRelativeAgeTime } from '../../../utils/format'
import type { CertificateSigningRequestDetails, CSRCertificateDetails, CSRRequestDetails } from '../../../types'

interface CertificateSigningRequestRendererProps {
  data: any
  /** Decoded spec.request and status.certificate from the resource read. */
  details?: CertificateSigningRequestDetails
}

export function CertificateSigningRequestRenderer({ data, details }: CertificateSigningRequestRendererProps) {
  const spec = data.spec ?? {}
  const csr = getCertificateSigningRequestState(data)
  const conditions = Array.isArray(data.status?.conditions) ? data.status.conditions : []
  const signer = text(spec.signerName)
  const extra = extraEntries(spec.extra)

  return (
    <>
      <StateBanner csr={csr} signer={signer} />

      <Section title="Request" icon={FileText}>
        <PropertyList>
          <Property label="Signer" value={signer ?? <Unread>Not set</Unread>} />
          <Property label="Usages" value={chips(strings(spec.usages)) ?? 'None requested'} />
          <Property label="Requested duration" value={requestedDuration(spec.expirationSeconds)} />
          {csr.decision && <Property label={csr.decision.type} value={<ConditionMoment condition={csr.decision} />} />}
          {csr.failure && <Property label="Failed" value={<ConditionMoment condition={csr.failure} />} />}
        </PropertyList>
      </Section>

      <Section title="Requester" icon={User}>
        <Note>The account that created this request, as the API server recorded it.</Note>
        <PropertyList>
          <Property label="Username" value={text(spec.username) ?? <Unread>Not recorded</Unread>} />
          <Property label="UID" value={text(spec.uid)} />
          <Property label="Groups" value={chips(strings(spec.groups))} />
          {extra.length > 0 && (
            <Property
              label="Extra"
              value={
                <div className="space-y-1">
                  {extra.map(([key, values]) => (
                    <div key={key} className="break-words">
                      <span className="text-theme-text-tertiary">{key}:</span> {values.join(', ')}
                    </div>
                  ))}
                </div>
              }
            />
          )}
        </PropertyList>
      </Section>

      <RequestedIdentity request={details?.request} present={isPresent(spec.request)} />
      <ReturnedCertificate certificate={details?.certificate} csr={csr} />
      <ConditionsSection conditions={conditions} />
    </>
  )
}

function StateBanner({ csr, signer }: { csr: CSRStatus; signer?: string }) {
  const signerName = signer ? `the signer ${signer}` : 'the signer'
  switch (csr.state) {
    case 'denied':
      return (
        <AlertBanner
          variant="error"
          title="Denied"
          message={conditionMessage(csr.condition) ?? 'An approver denied this request, so no certificate will be issued.'}
        />
      )
    case 'failed':
      return (
        <AlertBanner
          variant="error"
          title="Signing failed"
          message={conditionMessage(csr.condition) ?? 'The signer reported that it could not issue a certificate.'}
        />
      )
    case 'approved': {
      const at = conditionTime(csr.condition)
      return (
        <AlertBanner
          variant="warning"
          title="Approved, not issued"
          message={`Approved${at ? ` ${formatRelativeAgeTime(at)}` : ''}, but no certificate has been returned yet. Approval alone does not issue one; ${signerName} has to sign it.`}
        />
      )
    }
    case 'pending':
      if (csr.certificatePresent) {
        return (
          <AlertBanner
            variant="warning"
            title="Certificate without approval"
            message="status.certificate is set, but the request has no Approved condition, so it does not count as issued."
          />
        )
      }
      return (
        <AlertBanner
          variant="info"
          title="Pending approval"
          message="No approver has approved or denied this request yet. Nothing is issued before it is approved."
        />
      )
    default:
      return null
  }
}

function RequestedIdentity({ request, present }: { request?: CSRRequestDetails; present: boolean }) {
  let body: ReactNode
  if (!request) {
    body = present
      ? <Note>Not decoded: this Radar version does not decode certificate requests.</Note>
      : <AlertBanner variant="warning" title="No request data" message="spec.request is empty." />
  } else if (request.status === 'absent') {
    body = <AlertBanner variant="warning" title="No request data" message="spec.request is empty, so there is no requested identity to show." />
  } else if (request.status !== 'decoded') {
    body = <AlertBanner variant="error" title="The request could not be decoded" message={request.error} />
  } else {
    const sans: Array<[string, string[] | undefined]> = [
      ['DNS names', request.dnsNames],
      ['IP addresses', request.ipAddresses],
      ['Email addresses', request.emailAddresses],
      ['URIs', request.uris],
    ]
    const hasSANs = sans.some(([, values]) => values && values.length > 0)
    body = (
      <>
        {request.signatureError && (
          <AlertBanner
            variant="warning"
            title="Signature does not verify"
            message={`The request is not signed by the key it contains: ${request.signatureError}`}
          />
        )}
        <PropertyList>
          <Property label="Common name" value={request.commonName || <Unread>Empty</Unread>} />
          <Property label="Organizations" value={chips(request.organizations)} />
          <Property label="Subject" value={request.subject ? <span className="font-mono text-xs">{request.subject}</span> : <Unread>Empty</Unread>} />
          {sans.map(([label, values]) => <Property key={label} label={label} value={chips(values)} />)}
          {!hasSANs && (
            <Property
              label="SANs"
              value={request.unreadableSANs ? <Unread>Present, but of a type Radar can't read</Unread> : 'None requested'}
            />
          )}
          <Property label="Public key" value={request.publicKey} />
          <Property label="Signature algorithm" value={request.signatureAlgorithm} />
        </PropertyList>
      </>
    )
  }

  return (
    <Section title="Requested Identity" icon={KeyRound}>
      <Note>
        Decoded from the PKCS#10 request: what the requester asked for. It is not proof of what an approver
        authorized or what the signer issued.
      </Note>
      {body}
    </Section>
  )
}

function ReturnedCertificate({ certificate, csr }: { certificate?: CSRCertificateDetails; csr: CSRStatus }) {
  const label = csr.state === 'issued' ? 'Issued Certificate' : 'Returned Certificate'
  if (!certificate) {
    if (!csr.certificatePresent) return null
    return (
      <Section title={label} icon={Shield}>
        <Note>Not decoded: this Radar version does not decode returned certificates.</Note>
      </Section>
    )
  }
  if (certificate.status === 'absent') return null

  const certs = certificate.certificates ?? []
  return (
    <>
      {certificate.status === 'invalid' && (
        <AlertBanner
          variant="error"
          title="The returned certificate could not be read"
          message="status.certificate holds data, but none of it is a readable certificate."
          items={certificate.problems}
        />
      )}
      {certificate.status === 'partial' && (
        <AlertBanner
          variant="warning"
          title="Part of the returned certificate data could not be read"
          items={certificate.problems}
        />
      )}
      {certs.map((cert, i) => (
        <CertificateInfoSection
          key={`${i}-${cert.serialNumber}`}
          cert={cert}
          index={i}
          total={certs.length}
          title={certs.length > 1 ? `${label} ${i + 1} of ${certs.length}` : label}
        >
          {cert.subjectDN && <Property label="Subject" value={<span className="font-mono text-xs">{cert.subjectDN}</span>} />}
          <Property label="Email SANs" value={chips(cert.emailAddresses)} />
          <Property label="URI SANs" value={chips(cert.uris)} />
        </CertificateInfoSection>
      ))}
    </>
  )
}

function ConditionMoment({ condition }: { condition: CSRCondition }) {
  const at = conditionTime(condition)
  return (
    <span>
      {condition.reason && <>{condition.reason} · </>}
      <TimeValue timestamp={at} fallback="time not recorded" />
    </span>
  )
}

function Note({ children }: { children: ReactNode }) {
  return <p className="mb-2 text-xs text-theme-text-tertiary">{children}</p>
}

function Unread({ children }: { children: ReactNode }) {
  return <span className="text-theme-text-tertiary">{children}</span>
}

function chips(values?: string[]): ReactNode | undefined {
  if (!values || values.length === 0) return undefined
  return (
    <div className="flex flex-wrap gap-1">
      {values.map((value, i) => (
        <Badge key={`${i}-${value}`} tone="structural" size="sm">{value}</Badge>
      ))}
    </div>
  )
}

function conditionTime(condition?: CSRCondition): string | undefined {
  return condition?.lastTransitionTime ?? condition?.lastUpdateTime
}

function conditionMessage(condition?: CSRCondition): string | undefined {
  const parts = [condition?.reason, condition?.message].filter(Boolean)
  return parts.length > 0 ? parts.join(': ') : undefined
}

function requestedDuration(value: unknown): string {
  return typeof value === 'number' && value > 0 ? formatDuration(value * 1000, true) : 'Signer default'
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string' && v !== '') : []
}

function isPresent(value: unknown): boolean {
  return value !== undefined && value !== null && value !== ''
}

function extraEntries(value: unknown): Array<[string, string[]]> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value as Record<string, unknown>)
    .map(([key, values]): [string, string[]] => [key, strings(values)])
    .filter(([, values]) => values.length > 0)
    .sort(([a], [b]) => a.localeCompare(b))
}
