import { useState } from 'react'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import {
  collectionGroupParts,
  type NamespaceCollection,
  type NamespaceCollectionGroup,
} from '../../utils/namespace-collection'

const INCOMPLETE = 'Radar can\u2019t list every namespace here, so it may collect in more than these.'

export const COLLECTION_NOTE = 'Radar collects some kinds only in some namespaces.'

/** Which kinds Radar collects in which namespaces, laid out for a panel. */
export function NamespaceCollectionDetails({ collection }: { collection: NamespaceCollection }) {
  return (
    <>
      <p className="font-medium text-theme-text-primary">{COLLECTION_NOTE}</p>
      <ul className="mt-2 divide-y divide-theme-border border-t border-theme-border">
        {collection.groups.map(group => (
          <GroupRow key={group.kinds.map(({ kind }) => kind).join(',')} group={group} />
        ))}
      </ul>
      {collection.mayBeIncomplete && <p className="mt-2 leading-snug text-theme-text-secondary">{INCOMPLETE}</p>}
    </>
  )
}

function GroupRow({ group }: { group: NamespaceCollectionGroup }) {
  const { kinds, kindNames, summarized, namespaces } = collectionGroupParts(group)
  const [showKinds, setShowKinds] = useState(false)
  const disclosure = useDisclosure(showKinds)

  const sentence = (
    <>
      <span className="font-medium text-theme-text-primary">{kinds}</span> in{' '}
      <span className="font-medium text-theme-text-primary">{namespaces}</span>
    </>
  )

  return (
    <li className="py-2 leading-snug text-theme-text-secondary">
      {summarized ? (
        <>
          <button
            type="button"
            {...disclosure.buttonProps}
            onClick={() => setShowKinds(v => !v)}
            className="flex w-full items-start gap-1 rounded text-left hover:text-theme-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent any-pointer-coarse:py-2"
          >
            <CollapseChevron open={showKinds} className="mt-0.5 h-3 w-3" />
            <span>{sentence}</span>
          </button>
          <Collapse open={showKinds} id={disclosure.panelId}>
            <p className="break-words pl-4 pt-1">{kindNames.join(', ')}</p>
          </Collapse>
        </>
      ) : (
        <p className="pl-4">{sentence}</p>
      )}
    </li>
  )
}
