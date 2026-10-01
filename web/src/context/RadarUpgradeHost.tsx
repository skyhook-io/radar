import { createContext, useContext } from 'react'
import type { RadarUpgradeRequirement } from '@skyhook-io/k8s-ui'

/** What the user is missing and which Radar serves it; see RadarUpgradeRequirement. */
export type RadarUpgradeRequest = RadarUpgradeRequirement

/**
 * What an embedding host knows about the connected Radar and how it upgrades
 * it: RadarApp's `radarVersion` and `onRequestRadarUpgrade` props. Kept apart
 * from RadarUpgrade.tsx so api/client.ts can read it without an import cycle.
 */
export interface RadarUpgradeHost {
  radarVersion?: string
  onRequestRadarUpgrade?: (request: RadarUpgradeRequest) => void
}

const RadarUpgradeHostContext = createContext<RadarUpgradeHost>({})

export const RadarUpgradeHostProvider = RadarUpgradeHostContext.Provider

export function useRadarUpgradeHost(): RadarUpgradeHost {
  return useContext(RadarUpgradeHostContext)
}
