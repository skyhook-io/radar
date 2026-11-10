import { Timer } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'
import { formatRelativeAgeTime } from '../../../utils/format'

interface LeaseRendererProps {
  data: any
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
                  {formatRelativeAgeTime(spec.acquireTime)}
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
                  {formatRelativeAgeTime(renewTime)}
                </span>
              ) : undefined
            }
          />
        </PropertyList>
      </Section>
    </>
  )
}
