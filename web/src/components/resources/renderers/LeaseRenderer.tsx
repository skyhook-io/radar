import { Timer } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../drawer-components'

interface LeaseRendererProps {
  data: any
}

function formatRelativeTime(timestamp: string): string {
  const diff = Date.now() - new Date(timestamp).getTime()
  if (diff < 0) return 'just now'
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

export function LeaseRenderer({ data }: LeaseRendererProps) {
  const spec = data.spec || {}

  const renewTime: string | undefined = spec.renewTime
  const leaseDurationSeconds: number | undefined = spec.leaseDurationSeconds

  // Staleness calculation
  let isStale = false
  let ageRatio = 0
  if (renewTime && leaseDurationSeconds) {
    const elapsedSeconds = (Date.now() - new Date(renewTime).getTime()) / 1000
    ageRatio = elapsedSeconds / leaseDurationSeconds
    isStale = elapsedSeconds > leaseDurationSeconds
  }

  const renewColor = !renewTime
    ? ''
    : isStale
      ? 'text-red-400'
      : ageRatio > 0.75
        ? 'text-amber-400'
        : 'text-green-400'

  return (
    <>
      {isStale && (
        <AlertBanner
          variant="warning"
          title="Lease May Be Expired"
          message="The lease has not been renewed within its duration period. The holder may be unhealthy."
        />
      )}

      <Section title="Lease" icon={Timer} defaultExpanded>
        <PropertyList>
          <Property label="Holder" value={spec.holderIdentity || '-'} />
          <Property
            label="Duration"
            value={leaseDurationSeconds != null ? `${leaseDurationSeconds}s` : '-'}
          />
          <Property
            label="Transitions"
            value={spec.leaseTransitions != null ? String(spec.leaseTransitions) : '0'}
          />
          <Property
            label="Acquired"
            value={
              spec.acquireTime ? (
                <span title={new Date(spec.acquireTime).toLocaleString()}>
                  {formatRelativeTime(spec.acquireTime)}
                </span>
              ) : undefined
            }
          />
          <Property
            label="Last Renewed"
            value={
              renewTime ? (
                <span
                  className={clsx(renewColor)}
                  title={new Date(renewTime).toLocaleString()}
                >
                  {formatRelativeTime(renewTime)}
                </span>
              ) : undefined
            }
          />
        </PropertyList>
      </Section>
    </>
  )
}
