import { useNavigate } from 'react-router-dom'
import { WithTooltip } from '../ui/Tooltip'
import { Shield } from 'lucide-react'
import { Section } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { useAPIResources } from '../../api/apiResources'
import { ApiError, useRadarFeature } from '../../api/client'
import { isRadarFeatureUnsupported } from '../../api/radarFeatures'
import { hasVulnerabilityReports, useVulnerabilities, type VulnerabilityFinding } from '../../api/vulnerabilities'
import { SeverityBadge, SeverityCounts, gapSentence } from './parts'

interface Props {
  kind: string
  namespace: string
  name: string
}

/**
 * A workload's vulnerabilities, for its drawer: per container image, what
 * the current scan found, and the worst fixable findings. Absent where the
 * cluster has no scanner reports or the Radar predates the endpoint.
 */
export function WorkloadVulnerabilitiesSection({ kind, namespace, name }: Props) {
  const navigate = useNavigate()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  const workload = { kind, namespace, name }
  const { data, isLoading, error } = useVulnerabilities({ view: 'image', workload }, available && !!namespace && !!name)
  if (!available) return null
  // An older Radar, or a report kind removed since the page loaded, has
  // nothing to show here; anything else is a failure worth saying.
  if (error && (isRadarFeatureUnsupported(error) || (error instanceof ApiError && error.status === 404))) return null
  if (error) {
    return (
      <Section title="Vulnerabilities" icon={Shield}>
        <p className="text-xs text-warning-text">Couldn't read vulnerability reports: {error instanceof Error ? error.message : 'request failed'}.</p>
      </Section>
    )
  }

  const images = data?.images ?? []
  const unscanned = data?.unscanned ?? []
  const critical = images.reduce((n, img) => n + (img.findingsUnread ? 0 : img.counts.critical), 0)
  const fixFirst: VulnerabilityFinding[] = []
  const seen = new Set<string>()
  for (const img of images) {
    for (const f of img.findings) {
      if ((f.severity === 'critical' || f.severity === 'high') && f.fixedVersion && !seen.has(f.id)) {
        seen.add(f.id)
        fixFirst.push(f)
      }
    }
  }
  const isThis = (w: { kind: string; namespace: string; name: string }) => w.kind === kind && w.namespace === namespace && w.name === name
  const open = () => navigate({ pathname: '/checks/vulnerabilities', search: new URLSearchParams({ q: name, namespaces: namespace }).toString() })

  return (
    <Section title={critical > 0 ? `Vulnerabilities · ${critical} critical` : 'Vulnerabilities'} icon={Shield}>
      {isLoading ? (
        <p className="text-sm text-theme-text-tertiary">Reading vulnerability reports…</p>
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-xs text-theme-text-tertiary">Images this workload runs now, from Trivy Operator's reports. Older revisions are left out.</p>
          {images.map((img) => (
            <div key={img.id} className="flex flex-col gap-1 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium text-theme-text-primary">{[...new Set(img.workloads.filter(isThis).map((w) => w.container))].join(', ')}</span>
                <WithTooltip tip={img.image}><span className="truncate font-mono text-xs text-theme-text-tertiary">{img.image}</span></WithTooltip>
              </span>
              <span className="flex flex-wrap items-center gap-x-3">
                {img.findingsUnread ? <span className="text-xs text-warning-text">Too many findings to read here</span> : <SeverityCounts counts={img.counts} />}
                <span className="text-xs text-theme-text-secondary">{img.findingsUnread ? '' : `${img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown} with a fix · `}scanned {formatRelativeAgeTime(img.scannedAt)}</span>
              </span>
              <SharedWith others={[...new Set(img.workloads.filter((w) => !isThis(w)).map((w) => `${w.namespace}/${w.name}`))]} />
            </div>
          ))}
          {unscanned.map((u) => (
            <div key={u.image} className="flex flex-col gap-0.5 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium text-theme-text-primary">{u.workloads.map((w) => w.container).join(', ')}</span>
                <span className="truncate font-mono text-xs text-theme-text-tertiary">{u.image}</span>
              </span>
              <span className="text-xs text-warning-text">No report yet: Trivy hasn't scanned this image.</span>
            </div>
          ))}
          {images.length === 0 && unscanned.length === 0 && (
            <p className="text-xs text-theme-text-tertiary">
              {data && !data.coverage.complete
                ? gapSentence(data.coverage.gaps)?.replace(' Counts cover the rest.', '') ?? "Some reports couldn't be read."
                : 'No running containers in this workload.'}
            </p>
          )}
          {fixFirst.length > 0 && (
            <div className="flex flex-col gap-1">
              <span className="text-xs font-medium text-theme-text-secondary">Fix first</span>
              {fixFirst.slice(0, 3).map((f) => (
                <div key={f.id} className="grid grid-cols-[136px_70px_minmax(0,1fr)] items-center gap-2 text-xs">
                  <WithTooltip tip={f.title}><span className="truncate font-mono text-theme-text-primary">{f.id}</span></WithTooltip>
                  <SeverityBadge severity={f.severity} />
                  <span className="truncate font-mono text-theme-text-tertiary">{f.package} → <span className="text-emerald-700 dark:text-emerald-400">{f.fixedVersion}</span></span>
                </div>
              ))}
            </div>
          )}
          <button type="button" onClick={open} className="self-start text-xs text-accent-text hover:underline">Open in Vulnerabilities</button>
        </div>
      )}
    </Section>
  )
}

// The same image in other workloads: one rebuild fixes them all.
function SharedWith({ others }: { others: string[] }) {
  if (others.length === 0) return null
  const shown = others.slice(0, 2).join(', ')
  return (
    <span className="text-xs text-theme-text-tertiary">
      Also runs in {shown}{others.length > 2 ? ` and ${others.length - 2} more` : ''}
    </span>
  )
}
