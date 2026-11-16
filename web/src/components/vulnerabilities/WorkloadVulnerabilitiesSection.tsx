import { useNavigate, useSearchParams } from 'react-router-dom'
import { WithTooltip } from '../ui/Tooltip'
import { Shield } from 'lucide-react'
import { Section } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { useAPIResources } from '../../api/apiResources'
import { useRadarFeature } from '../../api/client'
import { isRadarFeatureUnsupported } from '../../api/radarFeatures'
import { hasVulnerabilityReports, isKindUnknown, useVulnerabilities, workloadParam, type VulnerabilityFinding } from '../../api/vulnerabilities'
import { FixVersion, ImageName, SeverityBadge, SeverityCounts, gapSentence } from './parts'

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
  const [params] = useSearchParams()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  const workload = { kind, namespace, name }
  const { data, isLoading, error } = useVulnerabilities({ view: 'image', workload }, available && !!namespace && !!name)
  if (!available) return null
  // An older Radar, or a report kind removed since the page loaded, has
  // nothing to show here; anything else is a failure worth saying.
  if (error && (isRadarFeatureUnsupported(error) || isKindUnknown(error))) return null
  if (error) {
    return (
      <Section title="Vulnerabilities" icon={Shield}>
        <p className="text-xs text-warning-text">Couldn't read vulnerability reports: {error instanceof Error ? error.message : 'request failed'}.</p>
      </Section>
    )
  }

  const images = data?.images ?? []
  // The tab folds clean images away; a workload's drawer shows every container.
  const clean = data?.clean ?? []
  const unscanned = data?.unscanned ?? []
  const critical = images.reduce((n, img) => n + (img.findingsUnread ? 0 : img.counts.critical), 0)
  // Images arrive ordered by their whole counts: rank the fixable findings
  // across all of them, keeping each CVE's worst occurrence, so a Critical
  // in the second image beats a High in the first.
  const worse = (a: VulnerabilityFinding, b: VulnerabilityFinding) => (a.severity === b.severity ? (b.score ?? 0) - (a.score ?? 0) : a.severity === 'critical' ? -1 : 1)
  const worst = new Map<string, VulnerabilityFinding>()
  for (const img of images) {
    for (const f of img.findings) {
      if ((f.severity === 'critical' || f.severity === 'high') && f.fixedVersion) {
        const prev = worst.get(f.id)
        if (!prev || worse(f, prev) < 0) worst.set(f.id, f)
      }
    }
  }
  const fixFirst = [...worst.values()].sort(worse)
  // Fix first shows three; an image also lists its worst findings only up to
  // a cap, past which fixable Critical and High findings may not have
  // arrived.
  const fixableUnlisted = fixFirst.length > FIX_FIRST || images.some((img) => {
    const listed = img.findings.filter((f) => (f.severity === 'critical' || f.severity === 'high') && f.fixedVersion).length
    return !img.findingsUnread && img.findings.length < img.findingsTotal && listed < img.fixable.critical + img.fixable.high
  })
  // The read spans the caller's scope; a gap names this namespace, or might.
  const relevantGaps = (data?.coverage.gaps ?? []).filter((g) => {
    const ns = g.namespaces
    return !ns || ns.extent !== undefined || (ns.names ?? []).length < ns.count || (ns.names ?? []).includes(namespace)
  })
  const isThis = (w: { kind: string; namespace: string; name: string }) => w.kind === kind && w.namespace === namespace && w.name === name
  // The link keeps the page's namespace view, as every page link does: the
  // picker follows the URL, so a narrower one would change it.
  const open = (more: Record<string, string> = {}) => {
    const search = new URLSearchParams({ workload: workloadParam({ kind, namespace, name }), ...more })
    const viewNamespaces = params.get('namespaces')
    if (viewNamespaces) search.set('namespaces', viewNamespaces)
    navigate({ pathname: '/checks/vulnerabilities', search: search.toString() })
  }

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
                <ImageName image={img.image} className="text-xs text-theme-text-tertiary" />
              </span>
              <span className="flex flex-wrap items-center gap-x-3">
                {img.findingsUnread ? <span className="text-xs text-warning-text">Too many findings to read here</span> : <SeverityCounts counts={img.counts} />}
                <span className="text-xs text-theme-text-secondary">{img.findingsUnread ? '' : `${img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown} with a fix · `}scanned {formatRelativeAgeTime(img.scannedAt)}</span>
              </span>
              <SharedWith others={[...new Set(img.workloads.filter((w) => !isThis(w)).map((w) => `${w.namespace}/${w.name}`))]} partial={img.workloads.length < img.workloadsTotal} />
            </div>
          ))}
          {unscanned.map((u) => (
            <div key={u.image} className="flex flex-col gap-0.5 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium text-theme-text-primary">{[...new Set(u.workloads.filter(isThis).map((w) => w.container))].join(', ')}</span>
                <ImageName image={u.image} className="text-xs text-theme-text-tertiary" />
              </span>
              <span className="text-xs text-warning-text">No report yet: Trivy hasn't scanned this image.</span>
            </div>
          ))}
          {clean.map((img) => (
            <div key={img.id} className="flex flex-col gap-1 rounded-md border border-theme-border bg-theme-surface px-3 py-2">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium text-theme-text-primary">{[...new Set(img.workloads.filter(isThis).map((w) => w.container))].join(', ')}</span>
                <ImageName image={img.image} className="text-xs text-theme-text-tertiary" />
              </span>
              <span className="text-xs text-theme-text-secondary">No CVEs reported · scanned {formatRelativeAgeTime(img.scannedAt)}</span>
            </div>
          ))}
          {images.length === 0 && clean.length === 0 && unscanned.length === 0 && (
            <p className="text-xs text-theme-text-tertiary">
              {gapSentence(relevantGaps, { counts: false }) ?? 'No running containers in this workload.'}
            </p>
          )}
          {fixFirst.length > 0 && (
            <div className="flex flex-col gap-1">
              <span className="text-xs font-medium text-theme-text-secondary">Fix first</span>
              {fixFirst.slice(0, FIX_FIRST).map((f) => (
                <div key={f.id} className="grid grid-cols-[136px_70px_minmax(0,1fr)] items-center gap-2 text-xs">
                  <WithTooltip tip={f.title}>
                    {f.link
                      ? <a href={f.link} target="_blank" rel="noreferrer" className="truncate font-mono text-accent-text hover:underline">{f.id}</a>
                      : <span className="truncate font-mono text-theme-text-primary">{f.id}</span>}
                  </WithTooltip>
                  <SeverityBadge severity={f.severity} />
                  <span className="flex min-w-0 gap-1.5"><span className="shrink-0 font-mono text-theme-text-secondary">{f.package}</span><FixVersion installed={f.installedVersion} fixed={f.fixedVersion} target={f.fixTarget} showInstalled={false} /></span>
                </div>
              ))}
            </div>
          )}
          {fixableUnlisted && (
            <button type="button" onClick={() => open({ severity: 'critical,high', fixable: '1' })} className="self-start text-xs text-accent-text hover:underline">
              All Critical and High with a fix
            </button>
          )}
          <button type="button" onClick={() => open()} className="self-start text-xs text-accent-text hover:underline">Open in Vulnerabilities</button>
        </div>
      )}
    </Section>
  )
}

const FIX_FIRST = 3

// The same image in other workloads: one rebuild fixes them all.
// partial: the response lists only some of the image's workloads.
function SharedWith({ others, partial }: { others: string[]; partial: boolean }) {
  if (others.length === 0) return null
  const shown = others.slice(0, 2).join(', ')
  const rest = others.length - 2
  return (
    <span className="text-xs text-theme-text-tertiary">
      Also runs in {shown}{rest > 0 ? ` and ${rest}${partial ? '+' : ''} more` : partial ? ', maybe more' : ''}
    </span>
  )
}
