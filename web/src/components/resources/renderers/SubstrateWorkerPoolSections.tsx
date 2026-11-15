import { SubstrateHealthCharts } from '../../resource/SubstrateHealthCharts'

export function SubstrateWorkerPoolSections({ data }: { data: any }) {
  return <SubstrateHealthCharts target={{ kind: 'workerpool', namespace: data?.metadata?.namespace ?? '', name: data?.metadata?.name ?? '' }} />
}
