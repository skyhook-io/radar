import { useMemo, type ReactNode } from 'react'
import { RadarUpgradeContext, type RadarUpgradeActions } from '@skyhook-io/k8s-ui'
import { useCapabilities, useVersionCheck } from '../api/client'
import { IN_CLUSTER_UPGRADE_URL, versionUpdateURL } from '../utils/version'
import { useNavCustomization } from './NavCustomization'

/**
 * Supplies the "Upgrade Radar" action to the inline notes k8s-ui renders for
 * features the connected Radar is too old to serve. A host that owns upgrades
 * (Radar Cloud) gets the request; standalone Radar links to the instructions
 * for how it was installed.
 */
export function RadarUpgradeProvider({ children }: { children: ReactNode }) {
  const { onRequestRadarUpgrade } = useNavCustomization()
  const { data: capabilities } = useCapabilities()
  const { data: versionInfo } = useVersionCheck()
  const mode = capabilities?.deployment?.mode ?? 'local'
  const upgradeHref = versionUpdateURL(mode, versionInfo?.releaseUrl) ?? IN_CLUSTER_UPGRADE_URL

  const value = useMemo<RadarUpgradeActions>(
    () => (onRequestRadarUpgrade ? { onRequestUpgrade: onRequestRadarUpgrade } : { upgradeHref }),
    [onRequestRadarUpgrade, upgradeHref],
  )

  return <RadarUpgradeContext.Provider value={value}>{children}</RadarUpgradeContext.Provider>
}
