import { Shield, FileText, Info } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'

interface CertificateRequestRendererProps {
  data: any
}

export function CertificateRequestRenderer({ data }: CertificateRequestRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const issuerRef = spec.issuerRef || {}
  const usages = spec.usages || []
  const ownerRefs = data.metadata?.ownerReferences || []

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const approvedCond = conditions.find((c: any) => c.type === 'Approved')
  const deniedCond = conditions.find((c: any) => c.type === 'Denied')

  const isReady = readyCond?.status === 'True'
  const isNotReady = readyCond?.status === 'False'
  const isApproved = approvedCond?.status === 'True'
  const isDenied = deniedCond?.status === 'True'

  const ownerCertificate = ownerRefs.find((ref: any) => ref.kind === 'Certificate')

  return (
    <>
      {/* Problem detection alerts */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="Certificate Request Not Ready"
          message={<>{readyCond.reason && <span className="font-medium">{readyCond.reason}: </span>}{readyCond.message || 'The certificate request is not in a ready state.'}</>}
        />
      )}

      {isDenied && (
        <AlertBanner
          variant="error"
          title="Certificate request was denied"
          message={<>{deniedCond.reason && <span className="font-medium">{deniedCond.reason}: </span>}{deniedCond.message || 'The certificate request has been denied by the approver.'}</>}
        />
      )}

      {/* Status */}
      <Section title="Status" icon={Shield}>
        <PropertyList>
          <Property
            label="Ready"
            value={
              <span className={clsx(
                'badge',
                isReady
                  ? 'bg-green-500/20 text-green-400'
                  : 'bg-red-500/20 text-red-400'
              )}>
                {isReady ? 'Ready' : 'Not Ready'}
              </span>
            }
          />
          <Property
            label="Approved"
            value={
              <span className={clsx(
                'badge',
                isApproved
                  ? 'bg-green-500/20 text-green-400'
                  : isDenied
                    ? 'bg-red-500/20 text-red-400'
                    : 'bg-yellow-500/20 text-yellow-400'
              )}>
                {isApproved ? 'Yes' : isDenied ? 'No' : 'Pending'}
              </span>
            }
          />
          <Property label="Reason" value={readyCond?.reason} />
          {ownerCertificate && (
            <Property label="Owner Certificate" value={ownerCertificate.name} />
          )}
        </PropertyList>
      </Section>

      {/* Issuer */}
      <Section title="Issuer" icon={Info}>
        <PropertyList>
          <Property label="Kind" value={issuerRef.kind} />
          <Property label="Name" value={issuerRef.name} />
          <Property label="Group" value={issuerRef.group} />
        </PropertyList>
      </Section>

      {/* Request Details */}
      <Section title="Request Details" icon={FileText}>
        <PropertyList>
          <Property label="Duration" value={spec.duration} />
          {usages.length > 0 && (
            <Property
              label="Usages"
              value={
                <div className="flex flex-wrap gap-1">
                  {usages.map((usage: string) => (
                    <span
                      key={usage}
                      className="badge bg-theme-elevated text-theme-text-secondary"
                    >
                      {usage}
                    </span>
                  ))}
                </div>
              }
            />
          )}
          <Property label="Has Certificate" value={status.certificate ? 'Yes' : 'No'} />
        </PropertyList>
      </Section>

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
