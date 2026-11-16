import { LonghornVolumeRenderer as BaseLonghornVolumeRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/LonghornVolumeRenderer'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { LONGHORN_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-longhorn'
import { useNamespaces, useResources } from '../../../api/client'
import { useCapabilitiesContext } from '../../../contexts/CapabilitiesContext'

/** Host wrapper: reads the Volume's Engine, the only source of healthy replicas and rebuild progress. */
export function LonghornVolumeRenderer({ data }: { data: any }) {
  const engines = useResources<any>('engines', data?.metadata?.namespace ?? '', LONGHORN_GROUP)
  const name = data?.metadata?.name
  // A migrating volume has two Engines; Longhorn's current one is spec.active.
  const candidates = engines.data?.filter((e) => e?.spec?.volumeName === name) ?? []
  const engine = candidates.length === 1 ? candidates[0] : candidates.find((e) => e?.spec?.active === true)
  const engineUnknown = !!engines.error || (candidates.length > 1 && !engine)
  const engineLookedUp = !engines.isLoading && !engines.error && engines.data !== undefined
  // With auth off every namespace is readable, including a deleted one whose
  // history matters most. Otherwise the caller's own namespace list decides;
  // while it or the capabilities load, Longhorn's recorded names stay hidden.
  const authOff = useCapabilitiesContext().authEnabled === false
  const readable = useNamespaces().data
  const canReadNamespace = (ns: string) => authOff || !!readable?.some((n) => n.name === ns)
  return (
    <>
      <BaseLonghornVolumeRenderer data={data} engine={engine} engineUnknown={engineUnknown} engineLookedUp={engineLookedUp} canReadNamespace={canReadNamespace} />
      <LookupFailureNote errors={[engines.error]} what="this volume's engine (healthy replicas, rebuilds)" />
    </>
  )
}
