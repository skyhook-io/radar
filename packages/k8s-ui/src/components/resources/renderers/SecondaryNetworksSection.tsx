import { useMemo } from 'react'
import { Network } from 'lucide-react'
import { AlertBanner, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import {
  MULTUS_NETWORKS_ANNOTATION,
  MULTUS_NETWORK_STATUS_ANNOTATION,
  NETWORK_ATTACHMENT_DEFINITION_GROUP,
  NETWORK_ATTACHMENT_DEFINITION_PLURAL,
  getPodNetworks,
  hasSecondaryNetworks,
  networkAttachmentKey,
  type NetworkAttachmentLookup,
  type NetworkAttachmentLookups,
  type NetworkAttachmentRow,
  type PodNetworks,
} from '../../../utils/multus'

const RAW_VALUE_LIMIT = 300

interface SecondaryNetworksSectionProps {
  pod: any
  /** Per-definition lookup results. Undefined when the host doesn't wire the
   *  lookup, which leaves every name unlinked and makes no claim about it. */
  attachmentLookups?: NetworkAttachmentLookups
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

function lookupNote(lookup: NetworkAttachmentLookup | undefined, namespace: string): string | null {
  switch (lookup) {
    case 'missing':
      return `No NetworkAttachmentDefinition with this name in namespace ${namespace}.`
    case 'forbidden':
      return 'You don’t have permission to read this NetworkAttachmentDefinition.'
    case 'unavailable':
      return 'Could not check whether this NetworkAttachmentDefinition exists.'
    case 'unchecked':
      return 'Not checked: this Pod names more definitions than Radar looks up at once.'
    default:
      return null
  }
}

function unreportedNote(row: NetworkAttachmentRow, reportedState: PodNetworks['reported']['state']): string | null {
  if (row.isDefaultNetwork) return null
  if (!row.reported) {
    if (reportedState === 'invalid') return null
    return reportedState === 'absent'
      ? 'Multus has not reported network status for this Pod.'
      : 'Multus has not reported an interface for this request.'
  }
  return row.requested ? null : 'Reported by Multus but not listed in the Pod’s networks annotation.'
}

function Values({ values }: { values: string[] }) {
  return (
    <div className="space-y-0.5 font-mono text-xs">
      {values.map((value, index) => <div key={`${value}-${index}`}>{value}</div>)}
    </div>
  )
}

function UnreadableAnnotation({ annotation, reason, value }: { annotation: string; reason: string; value?: string }) {
  return (
    <AlertBanner variant="warning" title={`Could not read ${annotation}`} message={reason} className="mb-0">
      {value && (
        <code className="mt-2 block break-all rounded bg-theme-elevated px-2 py-1 font-mono text-xs text-theme-text-secondary">
          {value.length > RAW_VALUE_LIMIT ? `${value.slice(0, RAW_VALUE_LIMIT)}…` : value}
        </code>
      )}
    </AlertBanner>
  )
}

function NetworkAttachmentCard({
  row,
  reportedState,
  lookup,
  onNavigate,
}: {
  row: NetworkAttachmentRow
  reportedState: PodNetworks['reported']['state']
  lookup?: NetworkAttachmentLookup
  onNavigate?: SecondaryNetworksSectionProps['onNavigate']
}) {
  const { requested, reported, ref } = row
  const label = requested
    ? (requested.namespace ? `${requested.namespace}/${requested.name}` : requested.name)
    : (reported?.name ?? '')
  const interfaceName = reported?.interfaceName
    ?? (requested?.interfaceName ? `${requested.interfaceName} (requested)` : undefined)
  const notes = [unreportedNote(row, reportedState), ref ? lookupNote(lookup, ref.namespace) : null]
    .filter((note): note is string => note !== null)

  return (
    <div className="card-inner">
      <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="break-all text-xs font-medium text-theme-text-primary">
          {ref && lookup === 'found' ? (
            <ResourceLink
              name={ref.name}
              namespace={ref.namespace}
              kind={NETWORK_ATTACHMENT_DEFINITION_PLURAL}
              group={NETWORK_ATTACHMENT_DEFINITION_GROUP}
              label={label}
              onNavigate={onNavigate}
            />
          ) : label}
        </span>
        <Badge severity="neutral" size="sm">
          {row.isDefaultNetwork ? 'Default network' : reported ? 'Reported' : 'Requested only'}
        </Badge>
      </div>
      <PropertyList>
        <Property label="Interface" value={interfaceName} />
        <Property label="IP addresses" value={reported?.ips.length ? <Values values={reported.ips} /> : undefined} />
        <Property label="MAC address" value={reported?.mac ? <Values values={[reported.mac]} /> : undefined} />
        <Property label="Requested IPs" value={requested?.ips ? <Values values={requested.ips} /> : undefined} />
        <Property label="Requested MAC" value={requested?.mac ? <Values values={[requested.mac]} /> : undefined} />
        <Property label="Requested default route" value={requested?.defaultRoute?.join(', ')} />
      </PropertyList>
      {notes.map(note => (
        <div key={note} className="mt-2 text-xs text-theme-text-tertiary">{note}</div>
      ))}
    </div>
  )
}

/** Multus attachments of a Pod: what its `networks` annotation asks for beside what
 *  Multus reports in `network-status`. Renders nothing for a Pod that has only its
 *  default network. */
export function SecondaryNetworksSection({ pod, attachmentLookups, onNavigate }: SecondaryNetworksSectionProps) {
  const networks = useMemo(() => getPodNetworks(pod), [pod])
  if (!hasSecondaryNetworks(networks)) return null

  const annotations: Record<string, string> = pod?.metadata?.annotations ?? {}
  const secondaryCount = networks.rows.filter(row => !row.isDefaultNetwork).length

  return (
    <Section
      title={secondaryCount > 0 ? `Secondary Networks (${secondaryCount})` : 'Secondary Networks'}
      icon={Network}
      defaultExpanded
    >
      <div className="space-y-3">
        {networks.requested.state === 'invalid' && (
          <UnreadableAnnotation
            annotation={MULTUS_NETWORKS_ANNOTATION}
            reason={networks.requested.reason}
            value={annotations[MULTUS_NETWORKS_ANNOTATION]}
          />
        )}
        {networks.reported.state === 'invalid' && (
          <UnreadableAnnotation
            annotation={MULTUS_NETWORK_STATUS_ANNOTATION}
            reason={networks.reported.reason}
            value={annotations[MULTUS_NETWORK_STATUS_ANNOTATION]}
          />
        )}
        {networks.rows.map(row => (
          <NetworkAttachmentCard
            key={row.key}
            row={row}
            reportedState={networks.reported.state}
            lookup={row.ref ? attachmentLookups?.[networkAttachmentKey(row.ref)] : undefined}
            onNavigate={onNavigate}
          />
        ))}
        <div className="text-xs text-theme-text-tertiary">
          Requested comes from the Pod’s networks annotation and reported from Multus’s network-status annotation.
          Neither shows that the Pod can reach anything on a network.
        </div>
      </div>
    </Section>
  )
}
