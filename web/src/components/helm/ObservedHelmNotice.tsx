import type { ReactNode } from 'react'
import { Info } from 'lucide-react'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'
import { gapReasonText } from '@skyhook-io/k8s-ui/utils/read-actions'
import type { HelmInventory } from '../../types'

const TITLE = 'Built from labels and annotations, not Helm’s own records'

function Code({ children }: { children: string }) {
  return <code className="inline-block max-w-full rounded bg-theme-elevated px-1 font-mono text-xs [overflow-wrap:anywhere] text-theme-text-primary">{children}</code>
}

const AFFECTED: { heading: string; items: { term: ReactNode; detail?: ReactNode }[] }[] = [
  {
    heading: 'Where this comes from',
    items: [
      {
        term: <><Code>meta.helm.sh/release-name</Code> and <Code>meta.helm.sh/release-namespace</Code></>,
        detail: 'annotations name the release.',
      },
      { term: <Code>helm.sh/chart</Code>, detail: 'label gives the chart and version.' },
      { term: <Code>app.kubernetes.io/version</Code>, detail: 'label gives the app version.' },
    ],
  },
  {
    heading: 'Not shown',
    items: [
      { term: 'Status, revision and last update.', detail: 'A listed release may be failed or part-way through an upgrade.' },
      {
        term: 'History, compare, values, manifest, notes and hooks.',
        detail: 'Helm keeps these in the same Secrets. For a Flux-managed release, you’ll see the history Flux recorded if you can read its HelmRelease.',
      },
      { term: 'Install, upgrade, rollback and uninstall.', detail: 'Helm records each of them in those Secrets.' },
    ],
  },
  {
    heading: 'Not listed',
    items: [
      { term: 'Releases that failed before creating any object.' },
      { term: 'Releases with no object you can read.' },
      { term: <>Charts that deploy only CRDs from <Code>crds/</Code>.</>, detail: 'Helm doesn’t label those.' },
    ],
  },
  {
    heading: 'Can differ from Helm',
    items: [
      {
        term: 'Objects kept after an uninstall',
        detail: <>can still show up as a release, for example with <Code>helm.sh/resource-policy: keep</Code>.</>,
      },
      {
        term: <>Charts without <Code>helm.sh/chart</Code></>,
        detail: <>show no chart or version. App version needs <Code>app.kubernetes.io/version</Code>.</>,
      },
      { term: 'Several charts on one release.', detail: 'If none matches the release name, Radar shows them all rather than pick one.' },
      { term: 'Object counts and health', detail: 'cover only what you can read, leaving out Secrets and kinds Radar isn’t watching.' },
    ],
  },
]

/** Where Helm's storage couldn't be read, for the opening sentence. */
function deniedScope(inventory: HelmInventory): string {
  const denied = inventory.storageDenied
  if (!denied || denied.extent === 'all') return 'Radar can’t read Helm’s release Secrets here'
  if (denied.extent === 'unknown') return 'Radar can’t read Helm’s release Secrets in some namespaces'
  const names = denied.names ?? []
  if (names.length > 0 && names.length <= 3) return `Radar can’t read Helm’s release Secrets in ${names.join(', ')}`
  return `Radar can’t read Helm’s release Secrets in ${denied.count} namespaces`
}

/** What the object read itself didn't reach, on top of what stamps can't say. */
function coverageSentence(inventory: HelmInventory): string | null {
  const gaps = inventory.objectCoverage?.gaps ?? []
  if (gaps.length === 0) return null
  const reasons = [...new Set(gaps.map((gap) => gapReasonText(gap.code)))]
  return `Some objects weren’t read (${reasons.join(', ')}), so this list may be missing more than usual.`
}

function NoticeCard({ children }: { children: ReactNode }) {
  return (
    <section aria-label={TITLE} className="mx-4 mt-4 rounded-xl border border-theme-border bg-theme-surface p-4 shadow-theme-sm [contain:inline-size]">
      <div className="flex items-start gap-3">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent" aria-hidden />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold text-theme-text-primary">{TITLE}</h2>
          <div className="mt-1 text-sm text-theme-text-secondary">{children}</div>
        </div>
      </div>
    </section>
  )
}

/** The callout above a Helm list that isn't entirely from Helm's storage. */
export function ObservedHelmNotice({ inventory }: { inventory: HelmInventory }) {
  const coverage = coverageSentence(inventory)
  const mixed = inventory.releases.length > 0
  return (
    <NoticeCard>
      <p>
        {deniedScope(inventory)}, so {mixed ? 'releases there come' : 'this list comes'} from labels and annotations on deployed
        objects. Those Secrets are the source of truth Helm itself uses, so this view can miss things.
      </p>
      {coverage && <p className="mt-1">{coverage}</p>}
      <p className="mt-2">
        <span className="font-medium text-theme-text-primary">How to get the full view:</span> get and list access to Secrets in the
        namespace where Helm stores a release, usually the release’s namespace.
      </p>
      <Disclosure summary="What’s affected" className="mt-3" summaryClassName="font-medium text-accent-text">
        <div className="mt-3 grid grid-cols-[repeat(auto-fit,minmax(15rem,1fr))] gap-x-8 gap-y-5 border-t border-theme-border pt-3">
          {AFFECTED.map(({ heading, items }) => (
            <section key={heading} aria-label={heading}>
              <h3 className="mb-2 font-semibold text-theme-text-primary">{heading}</h3>
              <ul className="space-y-2 text-xs leading-5">
                {items.map(({ term, detail }, index) => (
                  <li key={index}>
                    <span className="font-medium text-theme-text-primary">{term}</span>{detail && <> {detail}</>}
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </Disclosure>
    </NoticeCard>
  )
}

/** The drawer's short form: the list carries the detail. */
export function ObservedHelmDrawerNote() {
  return <NoticeCard>Radar can’t read this release’s Helm Secret, which is Helm’s source of truth, so this view can miss things.</NoticeCard>
}
