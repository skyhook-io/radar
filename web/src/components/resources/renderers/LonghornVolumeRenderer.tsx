import { LonghornVolumeRenderer as BaseLonghornVolumeRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/LonghornVolumeRenderer'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { LONGHORN_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-longhorn'
import { useResources } from '../../../api/client'

/** Host wrapper: reads the Volume's Engine, the only source of healthy replicas and rebuild progress. */
export function LonghornVolumeRenderer({ data }: { data: any }) {
  const engines = useResources<any>('engines', data?.metadata?.namespace ?? '', LONGHORN_GROUP)
  const name = data?.metadata?.name
  const engine = engines.data?.find((e) => e?.spec?.volumeName === name)
  return (
    <>
      <BaseLonghornVolumeRenderer data={data} engine={engine} />
      <LookupFailureNote errors={[engines.error]} what="this volume's engine (healthy replicas, rebuilds)" />
    </>
  )
}
