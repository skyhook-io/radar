import { useId, useState } from 'react'
import yaml from 'yaml'
import { ActionConfirmDialog, Input, isApiGroup } from '@skyhook-io/k8s-ui'
import { useNamespaces } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import type { SelectedResource } from '../../types'
import { getSkeletonYaml } from '../../utils/skeleton-yaml'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { clusterResource } from './shared'

export function CNPGCreateClusterDialog({ namespaces, onClose, onCreated }: {
  namespaces: string[]
  onClose: () => void
  onCreated: (resource: SelectedResource) => void
}) {
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const [namespace, setNamespace] = useState(namespaces.length === 1 ? namespaces[0] : '')
  const [manifest, setManifest] = useState<string | null>(null)
  const availableNamespaces = useNamespaces()
  const namespaceListId = useId()
  const contextChanged = context !== connection.context

  if (manifest && !contextChanged) {
    return (
      <CreateResourceDialog
        open
        onClose={onClose}
        initialYaml={manifest}
        initialMode="create"
        lockMode
        title="Create Cluster"
        onCreated={(created) => {
          if (created.kind === 'Cluster' && isApiGroup(created.apiVersion, 'postgresql.cnpg.io')) {
            onCreated(clusterResource(created.namespace, created.name))
          }
        }}
      />
    )
  }

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        const document = yaml.parseDocument(getSkeletonYaml('Cluster', 'postgresql.cnpg.io'))
        document.setIn(['metadata', 'namespace'], namespace.trim())
        setManifest(document.toString())
      }}
      title="Create Cluster"
      subject={{ kind: 'Cluster', namespace: namespace.trim(), name: 'new cluster' }}
      context={context || undefined}
      effect="Choose where to create the Cluster, then edit and review its manifest. An existing Cluster will not be updated."
      confirmLabel="Edit manifest"
      incompleteReason={!namespace.trim() ? 'Choose a namespace.' : undefined}
      disabledReason={contextChanged ? 'The Kubernetes context changed. Close this dialog and start again in the current context.' : undefined}
    >
      <label className="block">
        <span className="text-xs text-theme-text-secondary">Namespace</span>
        <Input
          value={namespace}
          onChange={(event) => setNamespace(event.target.value)}
          list={namespaceListId}
          placeholder="Enter a namespace"
          autoComplete="off"
          spellCheck={false}
          className="mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 text-sm text-theme-text-primary"
        />
        <datalist id={namespaceListId}>
          {availableNamespaces.data?.map((item) => <option key={item.name} value={item.name} />)}
        </datalist>
      </label>
      <p className="text-xs text-theme-text-tertiary">
        {availableNamespaces.isError
          ? 'Namespace suggestions could not be read. Enter the namespace; access is checked when you review the manifest.'
          : 'You can enter a namespace directly. This does not change the namespace filter.'}
      </p>
    </ActionConfirmDialog>
  )
}
