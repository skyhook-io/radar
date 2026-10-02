import { useMemo, type ReactNode } from 'react'
import { RadarUpgradeContext, type RadarUpgradeActions } from '@skyhook-io/k8s-ui'
import { useCapabilities, useVersionCheck } from '../api/client'
import { IN_CLUSTER_UPGRADE_URL } from '../utils/version'
import { useRadarUpgradeHost } from './RadarUpgradeHost'

/**
 * Supplies the "Upgrade Radar" action to the inline notes k8s-ui renders for
 * features the connected Radar is too old to serve. A host that owns upgrades
 * (Radar Cloud) gets the request; standalone Radar links to the instructions
 * for how it was installed.
 */
export function RadarUpgradeProvider({ children }: { children: ReactNode }) {
  const { onRequestRadarUpgrade } = useRadarUpgradeHost()
  const { data: capabilities } = useCapabilities()
  const { data: versionInfo } = useVersionCheck()
  const mode = capabilities?.deployment?.mode ?? 'local'
  // A local binary upgrades from its release page; an in-cluster or Cloud
  // agent from the docs section that covers Helm, Argo CD and Flux installs.
  const upgradeHref = mode === 'local' && versionInfo?.releaseUrl
    ? versionInfo.releaseUrl
    : `${IN_CLUSTER_UPGRADE_URL}#upgrading`

  const value = useMemo<RadarUpgradeActions>(
    () => (onRequestRadarUpgrade ? { onRequestUpgrade: onRequestRadarUpgrade } : { upgradeHref }),
    [onRequestRadarUpgrade, upgradeHref],
  )

  return <RadarUpgradeContext.Provider value={value}>{children}</RadarUpgradeContext.Provider>
}
