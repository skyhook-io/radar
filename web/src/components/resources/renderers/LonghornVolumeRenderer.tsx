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
  // After a failed refresh the query keeps the last list; don't show a count from it.
  const candidates = engines.error ? [] : engines.data?.filter((e) => e?.spec?.volumeName === name) ?? []
  const active = candidates.filter((e) => e?.spec?.active === true)
  const engine = candidates.length === 1 ? candidates[0] : active.length === 1 ? active[0] : undefined
  const engineUnknown = !!engines.error || (candidates.length > 1 && !engine)
  // Only a complete read proves the volume has no Engine.
  const engineLookedUp = !engines.isLoading && !engines.error && engines.coverage?.status === 'complete'
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
