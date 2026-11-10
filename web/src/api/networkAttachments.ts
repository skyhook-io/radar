import {
  NETWORK_ATTACHMENT_DEFINITION_GROUP,
  NETWORK_ATTACHMENT_DEFINITION_PLURAL,
  networkAttachmentKey,
  type NetworkAttachmentLookups,
  type NetworkAttachmentRef,
} from '@skyhook-io/k8s-ui/utils/multus'
import { useRelatedReads } from '../components/kueue/useRelatedReads'

// The names come from a Pod annotation anyone who can create Pods controls.
const MAX_LOOKUPS = 20

/**
 * Whether each NetworkAttachmentDefinition a Pod names can be read, as the
 * viewer. One read per definition rather than a list: a denied list comes back
 * empty, which would look the same as a definition that does not exist.
 */
export function useNetworkAttachmentLookups(refs: NetworkAttachmentRef[]): NetworkAttachmentLookups {
  const bounded = refs.slice(0, MAX_LOOKUPS)
  const reads = useRelatedReads(bounded.map(ref => ({
    kind: NETWORK_ATTACHMENT_DEFINITION_PLURAL,
    group: NETWORK_ATTACHMENT_DEFINITION_GROUP,
    namespace: ref.namespace,
    name: ref.name,
  })))
  const lookups: NetworkAttachmentLookups = {}
  bounded.forEach((ref, index) => { lookups[networkAttachmentKey(ref)] = reads[index] })
  for (const ref of refs.slice(MAX_LOOKUPS)) lookups[networkAttachmentKey(ref)] = { state: 'unchecked' }
  return lookups
}
