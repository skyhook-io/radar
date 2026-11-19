import { AlertBanner } from '@skyhook-io/k8s-ui'
import type { SubstrateAPITier, SubstrateStatus } from '../../api/substrateWorkspace'
import { tierReading } from './readings'
import { CommandChip, Term } from './shared'

/**
 * Why actor values are missing, in a banner: not connected, or connected and
 * failing, each with what to do next. Nothing for a connected tier.
 */
export function SubstrateTierBanner({ tier }: { tier: SubstrateAPITier }) {
  if (!showsTierBanner(tier)) return null
  const r = tierReading(tier)
  return (
    <div role="status">
      <AlertBanner variant={r.tone === 'unhealthy' ? 'error' : 'unknown'} title={`Substrate API: ${r.title.toLowerCase()}`} message={r.detail} className="">
        {r.nextStep && (
          <div className="mt-1 break-words text-xs text-theme-text-primary">
            <span className="font-medium">Next step: </span>
            {r.nextStep}
            {r.command && <CommandChip command={r.command} />}
          </div>
        )}
      </AlertBanner>
    </div>
  )
}

/** A failure the banner states; the rest (not set, turned off, no sign-in) the connect notice states. */
function showsTierBanner(tier: SubstrateAPITier): boolean {
  if (tier.state === 'connected' || tier.state === 'notConfigured' || tier.state === 'connecting') return false
  return tier.code !== 'sharedRadar' && tier.code !== 'apiDisabled' && tier.code !== 'targetChanged'
}

/** Opens Settings at Agent Substrate; in Radar Hub, a Settings view with only that section. */
export function openSubstrateSettings() {
  window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'substrate' } }))
}

const SETTINGS_LINK = 'text-xs font-medium text-accent-text hover:underline'

/** The source line's way to the connection: "Change connection" where it can be edited here, else "Connection settings". */
export function SubstrateConnectionLink({ status }: { status?: SubstrateStatus }) {
  return (
    <button type="button" onClick={openSubstrateSettings} className={SETTINGS_LINK}>
      {status?.canConfigure ? 'Change connection' : 'Connection settings'}
    </button>
  )
}

/**
 * Where actors, templates and workers would be when Radar doesn't read
 * Substrate's API: why, and the way to Settings → Agent Substrate. A local
 * Radar sets the connection there; elsewhere it shows how this Radar reads
 * Substrate and the person's own access.
 */
export function SubstrateConnectNotice({ status, tier }: { status?: SubstrateStatus; tier: SubstrateAPITier }) {
  const local = !!status?.canConfigure
  const unset = tier.state === 'notConfigured' || tier.code === 'targetChanged'
  const r = tierReading(tier)
  // A failure is already in the banner above; here only the way to the settings.
  const reading = !showsTierBanner(tier)
  return (
    <section aria-labelledby="substrate-connect" className="rounded-xl border border-theme-border bg-theme-surface px-4 py-4 shadow-theme-sm">
      <h2 id="substrate-connect" className="text-sm font-semibold text-theme-text-primary">
        {local && unset ? 'Connect to Substrate’s API' : reading ? r.title : 'Actors, templates and workers aren’t read'}
      </h2>
      <p className="mt-1 text-sm text-theme-text-secondary">
        Substrate keeps <Term term="actor">actors</Term>, <Term term="template">templates</Term> and <Term term="worker">workers</Term> in its own API, not Kubernetes. Pools, worker Pods and Prometheus readings show below either way.
      </p>
      {reading && !(local && unset) && r.detail && <p className="mt-2 text-sm text-theme-text-secondary">{r.detail}</p>}
      {reading && r.nextStep && !(local && tier.state === 'notConfigured') && (
        <p className="mt-2 text-sm text-theme-text-primary">
          <span className="font-medium">Next step: </span>
          {r.nextStep}
          {r.command && <CommandChip command={r.command} />}
        </p>
      )}
      <button type="button" onClick={openSubstrateSettings} className={local ? 'btn-brand mt-3 px-3 py-1.5 text-sm font-medium' : `mt-3 ${SETTINGS_LINK}`}>
        {local ? 'Open Substrate settings' : 'Connection settings'}
      </button>
    </section>
  )
}

/** Whether a screen shows the connect notice in place of Substrate's values: whenever the API tier isn't connected. */
export function showsConnectNotice(tier: SubstrateAPITier): boolean {
  return tier.state !== 'connected' && tier.state !== 'connecting'
}
