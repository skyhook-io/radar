import { useState } from 'react'
import { AlertTriangle, Copy, Check, Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../drawer-components'
import type { SecretCertificateInfo, CertificateInfo } from '../../../types'

interface SecretRendererProps {
  data: any
  certificateInfo?: SecretCertificateInfo
}

function formatDate(dateStr: string): string {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function SecretRenderer({ data, certificateInfo }: SecretRendererProps) {
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  const [copied, setCopied] = useState<string | null>(null)
  const dataKeys = Object.keys(data.data || {})

  function toggleReveal(key: string) {
    setRevealed(prev => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })
  }

  function decodeBase64(value: string): string {
    try {
      return atob(value)
    } catch {
      return '[binary data]'
    }
  }

  async function copyValue(key: string, decodedValue: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(decodedValue)
      setCopied(key)
      setTimeout(() => setCopied(null), 2000)
    } catch (err) {
      console.error('Failed to copy:', err)
    }
  }

  const certs = certificateInfo?.certificates
  const leafCert = certs?.[0]

  return (
    <>
      <Section title="Secret">
        <PropertyList>
          <Property label="Type" value={data.type || 'Opaque'} />
          <Property label="Keys" value={String(dataKeys.length)} />
          {data.immutable && <Property label="Immutable" value="Yes" />}
        </PropertyList>
      </Section>

      {/* Certificate expiry alerts */}
      {leafCert && leafCert.expired && (
        <AlertBanner
          variant="error"
          title="Certificate has expired"
          message={`Expired ${formatDate(leafCert.notAfter)}.${leafCert.daysLeft !== 0 ? ` ${Math.abs(leafCert.daysLeft)}d ago.` : ''}`}
        />
      )}

      {leafCert && !leafCert.expired && leafCert.daysLeft <= 7 && (
        <AlertBanner
          variant="error"
          title={`Certificate expires in ${leafCert.daysLeft} day${leafCert.daysLeft !== 1 ? 's' : ''}`}
          message="Check that cert-manager or your CA is renewing this certificate."
        />
      )}

      {leafCert && !leafCert.expired && leafCert.daysLeft > 7 && leafCert.daysLeft <= 30 && (
        <AlertBanner
          variant="warning"
          title={`Certificate expires in ${leafCert.daysLeft} day${leafCert.daysLeft !== 1 ? 's' : ''}`}
          message="Renewal should happen automatically before expiry."
        />
      )}

      {/* Certificate info section */}
      {certs && certs.length > 0 && (
        <>
          {certs.map((cert, i) => (
            <CertificateInfoSection
              key={cert.serialNumber}
              cert={cert}
              index={i}
              total={certs.length}
            />
          ))}
        </>
      )}

      <Section title="Data" defaultExpanded>
        <div className="space-y-2">
          {dataKeys.map((key) => {
            const decoded = decodeBase64(data.data[key])
            const isBinary = decoded === '[binary data]'

            return (
              <div key={key} className="bg-theme-elevated/30 rounded p-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm text-theme-text-primary truncate">{key}</span>
                  <div className="flex items-center gap-1 shrink-0">
                    {revealed.has(key) && !isBinary && (
                      <button
                        onClick={() => copyValue(key, decoded)}
                        className="p-1 text-theme-text-tertiary hover:text-theme-text-primary transition-colors"
                        title="Copy value"
                      >
                        {copied === key ? (
                          <Check className="w-3.5 h-3.5 text-green-400" />
                        ) : (
                          <Copy className="w-3.5 h-3.5" />
                        )}
                      </button>
                    )}
                    <button
                      onClick={() => toggleReveal(key)}
                      className="text-xs text-theme-text-secondary hover:text-theme-text-primary px-1.5 py-0.5 rounded hover:bg-theme-elevated transition-colors"
                    >
                      {revealed.has(key) ? 'Hide' : 'Reveal'}
                    </button>
                  </div>
                </div>
                {revealed.has(key) && (
                  <pre className="mt-2 bg-theme-base rounded p-2 text-xs text-theme-text-secondary overflow-x-auto max-h-40 whitespace-pre-wrap">
                    {decoded}
                  </pre>
                )}
              </div>
            )
          })}
          {dataKeys.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No data</div>
          )}
        </div>
      </Section>

      <div className="flex items-center gap-2 p-3 bg-red-500/10 border border-red-500/30 rounded text-red-400 text-sm">
        <AlertTriangle className="w-4 h-4" />
        Secret values are sensitive. Be careful when revealing.
      </div>
    </>
  )
}

function CertificateInfoSection({ cert, index, total }: { cert: CertificateInfo; index: number; total: number }) {
  const expiryTextColor = cert.expired || cert.daysLeft <= 7
    ? 'text-red-400'
    : cert.daysLeft <= 30
      ? 'text-yellow-400'
      : 'text-green-400'

  const title = total > 1
    ? `Certificate ${index + 1} of ${total}`
    : 'Certificate Info'

  return (
    <Section title={title} icon={Shield} defaultExpanded={index === 0}>
      <PropertyList>
        <Property label="Subject (CN)" value={cert.subject} />
        {cert.sans && cert.sans.length > 0 && (
          <Property label="SANs" value={
            <div className="flex flex-wrap gap-1">
              {cert.sans.map(san => (
                <span key={san} className="px-2 py-0.5 bg-theme-elevated rounded text-xs text-theme-text-secondary">
                  {san}
                </span>
              ))}
            </div>
          } />
        )}
        <Property label="Issuer" value={
          <span>
            {cert.issuer}
            {cert.selfSigned && (
              <span className="ml-2 text-[10px] px-1 py-0.5 bg-yellow-500/10 text-yellow-400 rounded">self-signed</span>
            )}
          </span>
        } />
        <Property label="Key Type" value={cert.keyType} />
        <Property label="Serial" value={
          <span className="font-mono text-xs">{cert.serialNumber}</span>
        } />
        <Property label="Not Before" value={formatDate(cert.notBefore)} />
        <Property label="Expires" value={
          <span>
            {formatDate(cert.notAfter)}
            <span className={clsx('ml-2 text-xs', expiryTextColor)}>
              {cert.expired
                ? `(expired ${Math.abs(cert.daysLeft)}d ago)`
                : `(${cert.daysLeft}d remaining)`}
            </span>
          </span>
        } />
      </PropertyList>
    </Section>
  )
}
