import { ResourceBar } from '@skyhook-io/k8s-ui'

const col = { display: 'flex', flexDirection: 'column', gap: 14, width: 300, padding: '12px 8px' } as const

export function Utilization() {
  return (
    <div style={col}>
      <ResourceBar used="847m" total="1930m" percent={44} />
      <ResourceBar used="5.4Gi" total="7.6Gi" percent={71} />
      <ResourceBar used="38Gi" total="42Gi" percent={91} />
    </div>
  )
}

export function WithRequestMarker() {
  return (
    <div style={col}>
      <ResourceBar used="1.2" total="2" percent={60} markerPercent={50} />
      <ResourceBar used="612Mi" total="1Gi" percent={60} markerPercent={25} />
    </div>
  )
}

export function PodCounts() {
  return (
    <div style={col}>
      <ResourceBar used="84" total="110" percent={76} colorScheme="count" />
      <ResourceBar used="104" total="110" percent={95} colorScheme="count" />
    </div>
  )
}

export function QuietInline() {
  return (
    <div style={{ ...col, gap: 8 }}>
      <ResourceBar used="41.2" total="96" percent={43} colorScheme="quiet" layout="inline" label="CPU" />
      <ResourceBar used="212Gi" total="384Gi" percent={55} colorScheme="quiet" layout="inline" label="MEM" />
      <ResourceBar used="6" total="8" percent={75} colorScheme="quiet" layout="inline" label="GPU" />
    </div>
  )
}
