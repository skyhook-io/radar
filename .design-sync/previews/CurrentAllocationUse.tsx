import { CurrentAllocationUse } from '@skyhook-io/k8s-ui'

const frame = { width: 580 } as const

export function WorkloadCurrent() {
  return (
    <div style={frame}>
      <CurrentAllocationUse
        currency="USD"
        dataAvailable
        cpuCost={0.182}
        memoryCost={0.064}
        hourlyCost={0.246}
        cpuAllocationUse={41.6}
        memoryAllocationUse={78.2}
        cpuUsageAvailable
        memoryUsageAvailable
      />
    </div>
  )
}

export function ApplicationKubecostDaily() {
  return (
    <div style={frame}>
      <CurrentAllocationUse
        currency="USD"
        dataAvailable
        cpuCost={1.94}
        memoryCost={1.21}
        hourlyCost={3.15}
        cpuAllocationUse={23.4}
        memoryAllocationUse={61.9}
        cpuUsageAvailable
        memoryUsageAvailable
        scopeNote="Included workloads only"
        window="1d"
      />
    </div>
  )
}

export function UsageUnavailable() {
  return (
    <div style={frame}>
      <CurrentAllocationUse
        currency="EUR"
        dataAvailable
        cpuCost={0.41}
        memoryCost={0.12}
        hourlyCost={0.53}
        cpuAllocationUse={0}
        memoryAllocationUse={0}
        cpuUsageAvailable={false}
        memoryUsageAvailable={false}
        window="1h"
      />
    </div>
  )
}

export function NoCostData() {
  return (
    <div style={frame}>
      <CurrentAllocationUse
        currency="USD"
        dataAvailable={false}
        cpuCost={0}
        memoryCost={0}
        hourlyCost={0}
        cpuAllocationUse={0}
        memoryAllocationUse={0}
        cpuUsageAvailable={false}
        memoryUsageAvailable={false}
      />
    </div>
  )
}
