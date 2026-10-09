import { useState } from 'react'
import {
  ActionConfirmDialog,
  FactGrid,
  FactRow,
  Input,
  RadarUpgradeNote,
  getRadarUpgradeRequirement,
} from '@skyhook-io/k8s-ui'
import { useCNPGArchivingPreview, useCNPGProtectionAction } from '../../../api/cnpg-protection'
import { useConnection } from '../../../context/ConnectionContext'
import { actionOutcomeLocked } from '../../../api/actions'
import { useCNPGWriteGuard } from '../actions/useCNPGWriteGuard'
import { useToast } from '../../ui/Toast'
import { CNPG_FORM_FIELD } from '../formFields'

export function CNPGAttachArchiveDialog({
  namespace,
  name,
  uid,
  stores,
  current,
  onClose,
}: {
  namespace: string
  name: string
  uid: string
  stores: any[]
  current?: { objectStore: string; serverName: string }
  onClose: () => void
}) {
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const [objectStore, setObjectStore] = useState(
    current?.objectStore || (stores.length === 1 ? stores[0].metadata.name : ''),
  )
  const [serverName, setServerName] = useState(current?.serverName || `${name.slice(0, 54)}-${uid.slice(0, 8)}`)
  const [acknowledged, setAcknowledged] = useState(false)
  const preview = useCNPGArchivingPreview(namespace, name)
  const write = useCNPGProtectionAction(namespace, name)
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'spec', paths: ['spec.plugins'] } })
  const { showSuccess } = useToast()
  const changed = connection.context !== context
  const reviewed = preview.data
  const upgrade = getRadarUpgradeRequirement(write.error || preview.error)
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onBack={
        reviewed
          ? () => {
              preview.reset()
              write.reset()
              setAcknowledged(false)
            }
          : undefined
      }
      backLabel="Back to storage"
      onConfirm={() => {
        if (!reviewed) {
          preview.mutate({ reviewedContext: context, objectStore: objectStore.trim(), serverName: serverName.trim() })
          return
        }
        write.mutate(
          {
            kind: 'clusters',
            action: 'configureArchiving',
            request: {
              reviewedContext: reviewed.context,
              uid: reviewed.uid,
              facts: reviewed.facts,
              params: {
                objectStore: reviewed.facts.objectStore,
                serverName: reviewed.facts.serverName,
                acknowledgeArchive: acknowledged,
              },
            },
          },
          {
            onSuccess: () => {
              showSuccess('Archive configuration saved. Check WAL uploads and a successful base backup next.')
              onClose()
            },
          },
        )
      }}
      title={reviewed ? 'Review archive attachment' : 'Choose existing backup storage'}
      subject={{ kind: 'Cluster', namespace, name }}
      context={context}
      effect="Enable the Barman plugin for WAL archiving and base backups. The ObjectStore itself is unchanged."
      disabledReason={
        changed ? 'The context changed. Close this guide and start again in the current context.' : undefined
      }
      incompleteReason={
        !reviewed && (!objectStore.trim() || !serverName.trim())
          ? 'Choose a store and archive server name'
          : reviewed && !acknowledged
            ? 'Confirm this archive identity is reserved for this Cluster'
            : undefined
      }
      guard={reviewed ? guard.node : undefined}
      guardSatisfied={!upgrade && (!reviewed || guard.satisfied)}
      confirmLabel={reviewed ? (reviewed.unchanged ? 'Keep configuration' : 'Enable archiving') : 'Review attachment'}
      isLoading={preview.isPending || write.isPending}
      error={upgrade ? undefined : write.error?.message || preview.error?.message}
      errorTitle={reviewed ? undefined : 'Attachment could not be reviewed'}
      outcomeUnknown={actionOutcomeLocked(write.error)}
      warnings={reviewed?.warnings}
      writes={
        reviewed
          ? [
              {
                summary: `patch Cluster ${namespace}/${name}`,
                detail: `spec.plugins: enable ${String(reviewed.plugin.name)} with barmanObjectName=${reviewed.facts.objectStore}, serverName=${reviewed.facts.serverName}; preserve other plugins and parameters`,
              },
            ]
          : undefined
      }
    >
      {upgrade && <RadarUpgradeNote requirement={upgrade} />}
      {reviewed ? (
        <div className="space-y-3">
          <FactGrid>
            <FactRow label="ObjectStore">{reviewed.facts.objectStore}</FactRow>
            <FactRow label="Archive identity">{reviewed.facts.serverName}</FactRow>
            <FactRow label="Destination">
              <span className="break-all font-mono">{reviewed.destination}</span>
            </FactRow>
            {reviewed.endpoint && (
              <FactRow label="Endpoint">
                <span className="break-all font-mono">{reviewed.endpoint}</span>
              </FactRow>
            )}
          </FactGrid>
          <p className="text-xs text-theme-text-secondary">
            Kubernetes accepted the server dry-run. This does not verify uploads or archive isolation. The selected
            ObjectStore is re-read on confirmation; it is not locked against a concurrent change.
          </p>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />
            <span>I confirm this archive identity is reserved for {name}.</span>
          </label>
        </div>
      ) : (
        <div className="space-y-3">
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">ObjectStore · namespace {namespace}</span>
            <Input
              aria-label="ObjectStore name"
              list="cnpg-protection-stores"
              value={objectStore}
              disabled={!!current?.objectStore || preview.isPending}
              onChange={(event) => setObjectStore(event.target.value)}
              className={CNPG_FORM_FIELD}
            />
            <datalist id="cnpg-protection-stores">
              {stores.map((store) => (
                <option key={store.metadata.name} value={store.metadata.name} />
              ))}
            </datalist>
          </label>
          <label className="block">
            <span className="text-xs font-medium text-theme-text-secondary">Archive server name</span>
            <Input
              aria-label="Archive server name"
              value={serverName}
              disabled={!!current?.objectStore || preview.isPending}
              onChange={(event) => setServerName(event.target.value)}
              className={CNPG_FORM_FIELD}
            />
          </label>
          <p className="text-xs text-theme-text-secondary">
            This name separates this Cluster’s archive inside the storage destination. A restored Cluster must use a
            different archive identity from its recovery source. Store names alone do not establish isolation.
          </p>
          <p className="text-xs text-theme-text-tertiary">
            Enter an existing store directly if suggestions are unavailable. Creating stores, configuring credentials
            and installing the plugin are separate prerequisites.
          </p>
        </div>
      )}
    </ActionConfirmDialog>
  )
}
