import { CNPG_KIND_BY_KEY } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'

const KIND_BY_PLURAL: Record<string, string> = Object.fromEntries(
  Object.values(CNPG_KIND_BY_KEY).map((k) => [k.plural, k.kind]),
)

/**
 * On a CloudNativePG workspace screen, the step back to the previous object in
 * the drawer trail, shown for whatever kind is open (a Pod or Secret reached
 * from a CNPG object included).
 */
export function CNPGDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  return <DrawerTrailBack resource={resource} pathPrefix="/cnpg" kindLabel={(plural) => KIND_BY_PLURAL[plural]} />
}
