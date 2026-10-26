import type { ReadKindRef, ReadNameSet } from '../types/read-coverage'
import { englishPlural, isEnglishPlural, pluralize } from './pluralize'

/** Kinds collected in one and the same set of namespaces the caller can see. */
export interface NamespaceCollectionGroup {
  kinds: ReadKindRef[]
  namespaces: ReadNameSet
}

/**
 * Where Radar keeps the built-in kinds it collects in only some namespaces.
 * `mayBeIncomplete`: the namespace list was cut short, so Radar may collect in more than the groups name.
 */
export interface NamespaceCollection {
  groups: NamespaceCollectionGroup[]
  mayBeIncomplete?: boolean
}

const MAX_SHOWN = 3

function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

function listWithMore(names: string[], noun: string): string {
  if (names.length <= MAX_SHOWN) return joinNames(names)
  return `${names.slice(0, MAX_SHOWN).join(', ')} and ${pluralize(names.length - MAX_SHOWN, `more ${noun}`)}`
}

export interface CollectionGroupParts {
  /** Plural names when there are few ("Pods and ConfigMaps"), otherwise a count ("21 kinds"). */
  kinds: string
  kindNames: string[]
  /** `kinds` is only a count, so a list of `kindNames` adds something. */
  summarized: boolean
  namespaces: string
}

/** The kinds and the namespaces of a group as separate phrases, so a layout can set them apart. */
export function collectionGroupParts({ kinds, namespaces }: NamespaceCollectionGroup): CollectionGroupParts {
  const kindNames = kinds.map(({ kind }) => (isEnglishPlural(kind) ? kind : englishPlural(kind)))
  const summarized = kindNames.length > MAX_SHOWN
  return {
    kinds: summarized ? pluralize(kindNames.length, 'kind') : joinNames(kindNames),
    kindNames,
    summarized,
    namespaces: namespacesLabel(namespaces),
  }
}

function namespacesLabel({ count, names }: ReadNameSet): string {
  if (count === 0) return 'no namespace you can see'
  if (!names) return pluralize(count, 'namespace')
  return listWithMore(names, 'namespace')
}
