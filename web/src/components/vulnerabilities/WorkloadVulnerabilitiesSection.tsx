import { Fragment } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { WithTooltip } from '../ui/Tooltip'
import { ChevronRight, Shield } from 'lucide-react'
import { Section } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { formatRelativeAgeTime } from '@skyhook-io/k8s-ui/utils/format'
import { useAPIResources } from '../../api/apiResources'
import { useRadarFeature } from '../../api/client'
import { hasVulnerabilityReports, useVulnerabilities, workloadParam } from '../../api/vulnerabilities'
import { ClearedCount, FixVersion, ImageName, SeverityCounts } from './parts'

interface Props {
  kind: string
  namespace: string
  name: string
}

export function WorkloadVulnerabilitiesSection({ kind, namespace, name }: Props) {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  const workload = { kind, namespace, name }
  const { data } = useVulnerabilities({ view: 'image', workload }, available && !!namespace && !!name)
  const images = data?.images ?? []
  const { data: fixData } = useVulnerabilities({ view: 'fix', workload }, available && !!namespace && !!name && images.length > 0)
  if (!available || images.length === 0) return null

  const critical = images.reduce((n, img) => n + (img.findingsUnread ? 0 : img.counts.critical), 0)
  const upgrades = fixData?.fixes ?? []
  const isThis = (w: { kind: string; namespace: string; name: string }) => w.kind === kind && w.namespace === namespace && w.name === name
  // The link keeps the page's namespace view, as every page link does: the
  // picker follows the URL, so a narrower one would change it.
  const open = (more: Record<string, string> = {}) => {
    const search = new URLSearchParams({ workload: workloadParam({ kind, namespace, name }), ...more })
    const viewNamespaces = params.get('namespaces')
    if (viewNamespaces) search.set('namespaces', viewNamespaces)
    navigate({ pathname: '/checks/vulnerabilities', search: search.toString() })
  }

  // Each image box opens the tab narrowed to this workload: one destination,
  // so one obvious thing to click.
  const card = (key: string, children: React.ReactNode) => (
    <WithTooltip key={key} tip="Open in Vulnerabilities">
      <button type="button" onClick={() => open()} className="group flex w-full items-center gap-2 rounded-md border border-theme-border bg-theme-surface px-3 py-2 text-left transition-colors hover:border-theme-text-tertiary hover:bg-theme-hover/50">
        <span className="flex min-w-0 flex-1 flex-col gap-1">{children}</span>
        <ChevronRight className="h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform group-hover:translate-x-0.5" aria-hidden />
      </button>
    </WithTooltip>
  )
  const containerName = (workloads: { kind: string; namespace: string; name: string; container?: string }[]) => (
    <span className="font-medium text-theme-text-primary">{[...new Set(workloads.filter(isThis).map((w) => w.container))].join(', ')}</span>
  )

  return (
    <Section title={critical > 0 ? `Vulnerabilities · ${critical} critical` : 'Vulnerabilities'} icon={Shield}>
      <div className="flex flex-col gap-3 text-sm">
        {images.map((img) => card(img.id, (
          <>
            <span className="flex flex-wrap items-baseline gap-x-2">
              {containerName(img.workloads)}
              <ImageName image={img.image} className="text-xs text-theme-text-tertiary" />
            </span>
            <span className="flex flex-wrap items-center gap-x-3">
              {img.findingsUnread ? <span className="text-xs text-warning-text">Findings not read</span> : <SeverityCounts counts={img.counts} />}
              <span className="text-xs text-theme-text-secondary">{img.findingsUnread ? '' : `${img.fixable.critical + img.fixable.high + img.fixable.medium + img.fixable.low + img.fixable.unknown} with a fix · `}scanned {formatRelativeAgeTime(img.scannedAt)}</span>
            </span>
            <SharedWith others={[...new Set(img.workloads.filter((w) => !isThis(w)).map((w) => `${w.namespace}/${w.name}`))]} partial={img.workloads.length < img.workloadsTotal} />
          </>
        )))}
        {Boolean(data?.unscanned?.length) && (
          <button type="button" onClick={() => open()} className="self-start text-xs text-accent-text hover:underline">
            View unscanned images in this workload →
          </button>
        )}
        {upgrades.length > 0 && (
          // One grid so packages, targets and counts line up; Critical and
          // High are what decide which upgrade comes first.
          <div className="grid grid-cols-[minmax(0,max-content)_minmax(0,1fr)_2rem_2.5rem] items-baseline gap-x-3 gap-y-1 text-xs">
            <span className="col-span-2 font-medium text-theme-text-secondary">Upgrade first</span>
            <span className="text-right text-theme-text-tertiary">C</span>
            <span className="text-right text-theme-text-tertiary">H</span>
            {upgrades.slice(0, UPGRADES_SHOWN).map((f) => (
              <Fragment key={`${f.packageType ?? ''}/${f.package}/${f.to ?? (f.fixedVersions ?? []).join('|')}`}>
                <span className="truncate font-mono text-theme-text-primary">{f.package}</span>
                {f.to
                  ? <span className="flex min-w-0 items-baseline gap-1 font-mono"><span className="text-theme-text-tertiary">→</span><FixVersion installed={f.from.join(', ')} target={f.to} showInstalled={false} /></span>
                  : <span className="truncate font-mono text-theme-text-tertiary">fixed in {(f.fixedVersions ?? []).join(', ')}</span>}
                <ClearedCount count={f.counts.critical} severity="critical" />
                <ClearedCount count={f.counts.high} severity="high" />
              </Fragment>
            ))}
            {upgrades.length > UPGRADES_SHOWN && (
              <button type="button" onClick={() => open({ by: 'fix' })} className="col-span-4 justify-self-start text-accent-text hover:underline">
                {upgrades.length - UPGRADES_SHOWN} more {upgrades.length - UPGRADES_SHOWN === 1 ? 'upgrade' : 'upgrades'} →
              </button>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}

// Upgrades shown in the drawer; the rest are one click away in By fix.
const UPGRADES_SHOWN = 3

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
