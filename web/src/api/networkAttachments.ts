import { useQueries } from '@tanstack/react-query'
import {
  NETWORK_ATTACHMENT_DEFINITION_GROUP,
  NETWORK_ATTACHMENT_DEFINITION_PLURAL,
  networkAttachmentKey,
  type NetworkAttachmentLookup,
  type NetworkAttachmentLookups,
  type NetworkAttachmentRef,
} from '@skyhook-io/k8s-ui/utils/multus'
import { fetchResourceWithRelationships, isForbiddenError, isNotFoundError } from './client'

// The names come from a Pod annotation anyone who can create Pods controls.
const MAX_LOOKUPS = 20

export function networkAttachmentLookupFromError(error: unknown): NetworkAttachmentLookup {
  if (isNotFoundError(error)) return 'missing'
  if (isForbiddenError(error)) return 'forbidden'
  return 'unavailable'
}

/**
 * Whether each NetworkAttachmentDefinition a Pod names can be read. One read per
 * definition rather than a list: a denied list comes back empty, which would look
 * the same as a definition that does not exist. The query key matches the detail
 * drawer's, so opening a linked definition reuses the result.
 */
export function useNetworkAttachmentLookups(refs: NetworkAttachmentRef[]): NetworkAttachmentLookups {
  const bounded = refs.slice(0, MAX_LOOKUPS)
  const queries = useQueries({
    queries: bounded.map(ref => ({
      queryKey: ['resource', NETWORK_ATTACHMENT_DEFINITION_PLURAL, ref.namespace, ref.name, NETWORK_ATTACHMENT_DEFINITION_GROUP],
      queryFn: () => fetchResourceWithRelationships(
        NETWORK_ATTACHMENT_DEFINITION_PLURAL,
        ref.namespace,
        ref.name,
        NETWORK_ATTACHMENT_DEFINITION_GROUP,
      ),
      staleTime: 15_000,
      retry: false,
    })),
  })

  const lookups: NetworkAttachmentLookups = {}
  bounded.forEach((ref, index) => {
    const query = queries[index]
    if (query.isSuccess) lookups[networkAttachmentKey(ref)] = 'found'
    else if (query.isError) lookups[networkAttachmentKey(ref)] = networkAttachmentLookupFromError(query.error)
  })
  for (const ref of refs.slice(MAX_LOOKUPS)) lookups[networkAttachmentKey(ref)] = 'unchecked'
  return lookups
}
