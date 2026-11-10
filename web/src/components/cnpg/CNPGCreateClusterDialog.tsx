import { useState } from 'react'
import yaml from 'yaml'
import { ActionConfirmDialog, ConfirmDialog, isApiGroup } from '@skyhook-io/k8s-ui'
import { useNamespaces, useResources } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import type { SelectedResource } from '../../types'
import { getSkeletonYaml } from '../../utils/skeleton-yaml'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { CNPGTargetFields } from './creation/CNPGTargetFields'
import { cnpgTargetDraft, cnpgTargetFromManifest, cnpgTargetIssue, updateCNPGTargetYaml, withCNPGTarget, type CNPGTarget } from './creation/targetModel'
import { clusterResource } from './shared'

export function CNPGCreateClusterDialog({ namespaces, onClose, onCreated }: {
  namespaces: string[]
  onClose: () => void
  onCreated: (resource: SelectedResource) => void
}) {
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const [base] = useState(() => yaml.parse(getSkeletonYaml('Cluster', 'postgresql.cnpg.io')))
  const [target, setTarget] = useState<CNPGTarget>(() => ({ ...cnpgTargetFromManifest(base), name: '', namespace: namespaces.length === 1 ? namespaces[0] : '', size: '' }))
  const [manifest, setManifest] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [replace, setReplace] = useState(false)
  const availableNamespaces = useNamespaces()
  const storageClasses = useResources<any>('storageclasses', undefined, 'storage.k8s.io')
  const contextChanged = context !== connection.context
  const generated = () => yaml.stringify(withCNPGTarget(base, target))

  if (contextChanged) return <ActionConfirmDialog open onClose={onClose} onConfirm={() => {}} title="Create Cluster" subject={{ kind: 'Cluster', namespace: target.namespace, name: target.name || 'new cluster' }} context={context || undefined} effect="The Kubernetes context changed." confirmLabel="Review manifest" disabledReason="Close this dialog and start again in the current context." />
  if (replace) return <ConfirmDialog open onClose={() => setReplace(false)} onConfirm={() => { setManifest(generated()); setReplace(false); setEditing(true) }} variant="warning" showWarning={false} title="Replace the current manifest?" message="This YAML cannot be represented by the Cluster setup. Rebuilding uses your setup choices and discards the current YAML." confirmLabel="Replace manifest" cancelLabel="Keep editing setup" />
  if (editing && manifest !== null) return <CreateResourceDialog
    open
    onClose={onClose}
    onBack={(draft) => {
      setManifest(draft)
      const parsed = cnpgTargetDraft(draft)
      if (parsed) setTarget(cnpgTargetFromManifest(parsed))
      setEditing(false)
    }}
    backLabel="Back to cluster setup"
    initialYaml={manifest}
    initialMode="create"
    lockMode
    title="Create Cluster"
    onCreated={(created) => {
      if (created.kind === 'Cluster' && isApiGroup(created.apiVersion, 'postgresql.cnpg.io')) onCreated(clusterResource(created.namespace, created.name))
    }}
  />

  return <ActionConfirmDialog
    open
    onClose={onClose}
    onConfirm={() => {
      if (manifest !== null) {
        const updated = updateCNPGTargetYaml(manifest, target)
        if (updated === null) { setReplace(true); return }
        setManifest(updated)
      } else setManifest(generated())
      setEditing(true)
    }}
    title="Create Cluster"
    subject={{ kind: 'Cluster', namespace: target.namespace.trim(), name: target.name.trim() || 'new cluster' }}
    context={context || undefined}
    effect="Create a PostgreSQL Cluster with the placement and resources below, then review the exact manifest."
    confirmLabel="Review manifest"
    incompleteReason={cnpgTargetIssue(target)}
    notes={['An existing Cluster will not be updated. This does not change the namespace filter.', 'The new Cluster starts without backups or WAL archiving. Its Backups tab guides the next step.']}
  >
    {manifest !== null && <div className="mb-4 space-y-2 text-sm text-theme-text-secondary">{!cnpgTargetDraft(manifest) && <p>The current YAML cannot be represented by these fields. Rebuilding from setup requires confirmation.</p>}<button type="button" className="btn-secondary px-3 py-1.5 text-xs" onClick={() => setEditing(true)}>Continue editing current YAML</button></div>}
    <CNPGTargetFields value={target} onChange={setTarget} idPrefix="cnpg-create" namespaces={availableNamespaces.data?.map((item) => item.name)} storageClasses={storageClasses.data?.map((item) => item.metadata.name)} defaultClassDisabled={manifest !== null && cnpgTargetDraft(manifest)?.spec.storage?.pvcTemplate?.storageClassName === ''} />
    {(availableNamespaces.isError || storageClasses.isError) && <p className="mt-3 text-xs text-theme-text-tertiary">{availableNamespaces.isError && 'Namespace suggestions could not be read. '}{storageClasses.isError && 'Storage class suggestions could not be read. '}You can enter names directly; access and configuration are checked at manifest review.</p>}
  </ActionConfirmDialog>
}
