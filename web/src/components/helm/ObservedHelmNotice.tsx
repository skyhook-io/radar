import { Info } from 'lucide-react'
import { AlertBanner } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'
import { gapReasonText } from '@skyhook-io/k8s-ui/utils/read-actions'
import type { HelmInventory } from '../../types'

const TITLE = 'Built from labels and annotations, not Helm’s own records'

const SOURCES = (
  <>
    the <Code>meta.helm.sh/release-name</Code> and <Code>meta.helm.sh/release-namespace</Code> annotations and
    the <Code>helm.sh/chart</Code> and <Code>app.kubernetes.io/version</Code> labels on deployed objects
  </>
)

function Code({ children }: { children: string }) {
  return <code className="font-mono text-[11px]">{children}</code>
}

const DETAILS = [
  'Status, revision and last update are unknown, so a release listed here may be failed or part-way through an upgrade.',
  'History, compare, values, rendered manifest, notes and hooks live in the same Secrets, so they aren’t shown. For a release managed by Flux, you’ll see the history Flux recorded if you can read its HelmRelease.',
  'A release isn’t listed if it failed before creating any object, if you can’t read any of its objects, or if it deploys only CRDs from a chart’s crds/ directory, which Helm doesn’t label.',
  'Objects kept after an uninstall, for example with helm.sh/resource-policy: keep, can still show up as a release.',
  'Charts that don’t set helm.sh/chart show no chart or version, and app version appears only if the chart sets app.kubernetes.io/version. If a release’s objects name several charts and none matches the release name, Radar shows them all rather than pick one.',
  'Object counts and health cover only what you can read, and leave out Secrets and any kinds Radar isn’t watching.',
  'Install, upgrade, rollback and uninstall aren’t available here, because Helm records each of them in those Secrets.',
  'Get and list access to Secrets in the namespace where Helm stores a release gives the full view of it. That’s usually the release’s namespace.',
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

/** The callout above a Helm list that isn't entirely from Helm's storage. */
export function ObservedHelmNotice({ inventory }: { inventory: HelmInventory }) {
  const coverage = coverageSentence(inventory)
  const mixed = inventory.releases.length > 0
  return (
    <AlertBanner
      variant="info"
      icon={Info}
      title={TITLE}
      className="mx-4 mt-4 mb-0"
      message={
        <>
          <p>
            {deniedScope(inventory)}, so {mixed ? 'releases there come' : 'this list comes'} from {SOURCES}. Those Secrets are the
            source of truth Helm itself uses, so this view can miss things.
          </p>
          {coverage && <p className="mt-1">{coverage}</p>}
          <Disclosure summary="What’s affected, and how to get the full view" className="mt-2" summaryClassName="font-medium">
            <ul className="mt-1.5 list-disc space-y-1 pl-4">
              {DETAILS.map((detail) => <li key={detail}>{detail}</li>)}
            </ul>
          </Disclosure>
        </>
      }
    />
  )
}

/** The drawer's short form: the list carries the detail. */
export function ObservedHelmDrawerNote() {
  return (
    <AlertBanner
      variant="info"
      icon={Info}
      title={TITLE}
      className="mx-4 mt-4 mb-0"
      message="Radar can’t read this release’s Helm Secret, which is Helm’s source of truth, so this view can miss things."
    />
  )
}
