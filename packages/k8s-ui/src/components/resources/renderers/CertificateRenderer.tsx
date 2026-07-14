import { Shield, Clock, Globe, Key } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, useOperationalIssuesShown} from '../../ui/drawer-components'
import { pluralize } from '../../../utils/pluralize'

interface CertificateRendererProps {
  data: any
}

function formatDate(dateStr: string): string {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function getDaysUntil(dateStr: string): number {
  const target = new Date(dateStr).getTime()
  const now = Date.now()
  return Math.floor((target - now) / (1000 * 60 * 60 * 24))
}

export function CertificateRenderer({ data }: CertificateRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const dnsNames = spec.dnsNames || []
  const issuerRef = spec.issuerRef || {}
  const usages = spec.usages || []

  const privateKey = spec.privateKey
  const failedIssuanceAttempts = status.failedIssuanceAttempts
  const lastFailureTime = status.lastFailureTime

  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const isReady = readyCond?.status === 'True'
  const isNotReady = readyCond?.status === 'False'
  const operationalIssuesShown = useOperationalIssuesShown()

  const notAfter = status.notAfter
  const notBefore = status.notBefore
  const renewalTime = status.renewalTime

  const daysUntilExpiry = notAfter ? getDaysUntil(notAfter) : null
  const isExpired = daysUntilExpiry !== null && daysUntilExpiry < 0
  const expiresWithin7Days = daysUntilExpiry !== null && !isExpired && daysUntilExpiry <= 7
  const expiresWithin30Days = daysUntilExpiry !== null && !isExpired && !expiresWithin7Days && daysUntilExpiry <= 30

  // Progress bar calculation
  let progressPct = 0
  let progressColor = 'bg-green-500'
  if (notBefore && notAfter) {
    const now = Date.now()
    const start = new Date(notBefore).getTime()
    const end = new Date(notAfter).getTime()
    progressPct = Math.min(100, Math.max(0, ((now - start) / (end - start)) * 100))
    if (progressPct >= 90) {
      progressColor = 'bg-red-500'
    } else if (progressPct >= 70) {
      progressColor = 'bg-yellow-500'
    }
  }

  // Expiry remaining text color
  const expiryTextColor = isExpired || expiresWithin7Days
    ? 'text-red-400'
    : expiresWithin30Days
      ? 'text-yellow-400'
      : 'text-green-400'

  return (
    <>
      {/* Problem detection alerts */}
      {isNotReady && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title="Certificate Not Ready"
          message={<>{readyCond.reason && <span className="font-medium">{readyCond.reason}: </span>}{readyCond.message || 'The certificate is not in a ready state.'}</>}
        />
      )}

      {isExpired && (
        <AlertBanner
          variant="error"
          title="Certificate has expired"
          message={`Expired ${formatDate(notAfter)}. Renewal may be pending or failing.`}
        />
      )}

      {expiresWithin7Days && (
        <AlertBanner
          variant="error"
          title={`Certificate expires in ${pluralize(daysUntilExpiry, 'day')}`}
          message="Check that cert-manager is renewing this certificate."
        />
      )}

      {expiresWithin30Days && (
        <AlertBanner
          variant="warning"
          title={`Certificate expires in ${pluralize(daysUntilExpiry, 'day')}`}
          message="Renewal should happen automatically before expiry."
        />
      )}

      {failedIssuanceAttempts > 0 && (
        <AlertBanner
          variant="warning"
          title={pluralize(failedIssuanceAttempts, 'failed issuance attempt')}
          message={lastFailureTime ? `Last failure: ${formatDate(lastFailureTime)}` : undefined}
        />
      )}

      {/* Certificate Info */}
      <Section title="Certificate Info" icon={Shield}>
        <PropertyList>
          <Property
            label="Status"
            value={
              <span className="flex items-center gap-2">
                <span className={clsx(
                  'badge',
                  isReady
                    ? 'bg-green-500/20 text-green-400'
                    : 'bg-red-500/20 text-red-400'
                )}>
                  {isReady ? 'Ready' : 'Not Ready'}
                </span>
                {spec.isCA && (
                  <span className="badge bg-purple-500/20 text-purple-400">
                    CA Certificate
                  </span>
                )}
              </span>
            }
          />
          <Property label="Secret Name" value={spec.secretName} />
          <Property label="Revision" value={status.revision} />
          {failedIssuanceAttempts > 0 && (
            <Property label="Failed Issuance Attempts" value={String(failedIssuanceAttempts)} />
          )}
          {lastFailureTime && (
            <Property label="Last Failure Time" value={formatDate(lastFailureTime)} />
          )}
        </PropertyList>
      </Section>

      {/* Validity */}
      <Section title="Validity" icon={Clock}>
        <PropertyList>
          <Property label="Duration" value={spec.duration} />
          <Property label="Renew Before" value={spec.renewBefore} />
          <Property label="Not Before" value={notBefore ? formatDate(notBefore) : '-'} />
          <Property
            label="Not After"
            value={
              notAfter ? (
                <span>
                  {formatDate(notAfter)}
                  {daysUntilExpiry !== null && (
                    <span className={clsx('ml-2 text-xs', expiryTextColor)}>
                      {isExpired
                        ? `(expired ${-daysUntilExpiry}d ago)`
                        : `(${daysUntilExpiry}d remaining)`}
                    </span>
                  )}
                </span>
              ) : '-'
            }
          />
          <Property label="Renewal Time" value={renewalTime ? formatDate(renewalTime) : '-'} />
        </PropertyList>

        {/* Progress bar */}
        {notBefore && notAfter && (
          <div className="mt-3">
            <div className="flex items-center justify-between text-xs text-theme-text-tertiary mb-1">
              <span>{formatDate(notBefore)}</span>
              <span>{formatDate(notAfter)}</span>
            </div>
            <div className="h-2 bg-theme-hover rounded overflow-hidden">
              <div
                className={clsx('h-full transition-all', progressColor)}
                style={{ width: `${progressPct}%` }}
              />
            </div>
            <div className="text-xs text-theme-text-tertiary mt-1 text-center">
              {Math.round(progressPct)}% elapsed
            </div>
          </div>
        )}
      </Section>

      {/* Private Key */}
      {privateKey && (
        <Section title="Private Key" icon={Key}>
          <PropertyList>
            <Property label="Algorithm" value={privateKey.algorithm} />
            <Property label="Size" value={privateKey.size != null ? String(privateKey.size) : undefined} />
            <Property label="Encoding" value={privateKey.encoding} />
            <Property label="Rotation Policy" value={privateKey.rotationPolicy} />
          </PropertyList>
        </Section>
      )}

      {/* Domains */}
      {dnsNames.length > 0 && (
        <Section title="Domains" icon={Globe}>
          <div className="flex flex-wrap gap-1">
            {dnsNames.map((name: string) => (
              <span
                key={name}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {name}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Issuer */}
      <Section title="Issuer">
        <PropertyList>
          <Property label="Kind" value={issuerRef.kind} />
          <Property label="Name" value={issuerRef.name} />
          <Property label="Group" value={issuerRef.group} />
        </PropertyList>
      </Section>

      {/* Usages */}
      {usages.length > 0 && (
        <Section title="Usages">
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
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
