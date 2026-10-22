import { useState } from 'react'
import yaml from 'yaml'
import {
  ActionConfirmDialog,
  CNPGSchedulePreviewFacts,
  ConfirmDialog,
  FactGrid,
  Input,
  RadarUpgradeNote,
  getRadarUpgradeRequirement,
  useDebouncedValue,
} from '@skyhook-io/k8s-ui'
import { validateRFC1123Label } from '@skyhook-io/k8s-ui/utils/validators'
import { useCNPGDraftSchedulePreview } from '../../../api/cnpg-protection'
import { useConnection } from '../../../context/ConnectionContext'
import { CreateResourceDialog } from '../../shared/CreateResourceDialog'
import { CNPGScheduleInput } from './CNPGScheduleInput'
import { useToast } from '../../ui/Toast'
import { CNPG_FORM_FIELD } from '../formFields'

export function CNPGCreateScheduleDialog({
  namespace,
  cluster,
  onClose,
  onCreated,
}: {
  namespace: string
  cluster: string
  onClose: () => void
  onCreated: () => void
}) {
  const { connection } = useConnection()
  const [context] = useState(connection.context)
  const [name, setName] = useState(`${cluster.slice(0, 56)}-backup`)
  const [schedule, setSchedule] = useState('0 0 2 * * *')
  const [immediate, setImmediate] = useState(false)
  const [draft, setDraft] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [replace, setReplace] = useState(false)
  const debounced = useDebouncedValue(schedule.trim(), 300)
  const preview = useCNPGDraftSchedulePreview(namespace, cluster, debounced, !!debounced)
  const { showSuccess } = useToast()
  const current =
    debounced === schedule.trim() && preview.data?.schedule === debounced && !preview.isFetching
      ? preview.data
      : undefined
  const generated = () =>
    yaml.stringify({
      apiVersion: 'postgresql.cnpg.io/v1',
      kind: 'ScheduledBackup',
      metadata: { name: name.trim(), namespace },
      spec: {
        cluster: { name: cluster },
        method: 'plugin',
        pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io' },
        schedule: schedule.trim(),
        immediate,
      },
    })
  const upgrade = getRadarUpgradeRequirement(preview.error)
  const changed = context !== connection.context
  if (changed)
    return (
      <ActionConfirmDialog
        open
        onClose={onClose}
        onConfirm={() => {}}
        title="Create backup schedule"
        subject={{ kind: 'Cluster', namespace, name: cluster }}
        context={context}
        effect="The context changed."
        confirmLabel="Review manifest"
        disabledReason="Close this dialog and start again in the current context."
      />
    )
  if (replace)
    return (
      <ConfirmDialog
        open
        onClose={() => setReplace(false)}
        onConfirm={() => {
          setDraft(generated())
          setEditing(true)
          setReplace(false)
        }}
        title="Replace the current schedule YAML?"
        message="This uses the setup choices below and discards the YAML you edited."
        variant="warning"
        showWarning={false}
        confirmLabel="Replace YAML"
      />
    )
  if (editing && draft !== null)
    return (
      <CreateResourceDialog
        open
        onClose={onClose}
        onBack={(value) => {
          setDraft(value)
          setEditing(false)
        }}
        backLabel="Back to schedule setup"
        initialYaml={draft}
        initialMode="create"
        lockMode
        title="Create backup schedule"
        onCreated={(result) => {
          onCreated()
          showSuccess(
            `${result.kind} ${result.name} created. Check its settings and the next backup; creation alone does not prove protection.`,
          )
        }}
      />
    )
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        if (draft !== null && draft !== generated()) {
          setReplace(true)
          return
        }
        setDraft(generated())
        setEditing(true)
      }}
      title="Create matching backup schedule"
      subject={{ kind: 'Cluster', namespace, name: cluster }}
      context={context}
      effect="Create a separate ScheduledBackup using this Cluster’s Barman plugin, then review its exact manifest."
      size="wide"
      confirmLabel="Review manifest"
      guardSatisfied={!upgrade}
      disabledReason={preview.error && !upgrade ? `Timing could not be checked: ${preview.error.message}` : undefined}
      incompleteReason={
        !name.trim()
          ? 'Enter a schedule name'
          : !validateRFC1123Label(name.trim()).valid
            ? 'Use a valid schedule name'
            : !current
              ? 'Checking the schedule…'
              : !current.valid
                ? 'Choose a schedule the operator can run'
                : undefined
      }
      notes={['Each scheduled run creates a Backup. This does not change the Cluster or validate credentials.']}
    >
      {upgrade && <RadarUpgradeNote requirement={upgrade} />}
      {draft !== null && (
        <div className="space-y-2 text-xs text-theme-text-secondary">
          <p>
            The current YAML is retained. Returning to review keeps it; rebuilding from these fields requires
            confirmation.
          </p>
          <button type="button" className="btn-secondary px-3 py-1.5" onClick={() => setEditing(true)}>
            Continue editing current YAML
          </button>
        </div>
      )}
      <label className="block">
        <span className="text-xs font-medium text-theme-text-secondary">Schedule name · namespace {namespace}</span>
        <Input
          aria-label="Schedule name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          className={CNPG_FORM_FIELD}
        />
      </label>
      <CNPGScheduleInput value={schedule} onChange={setSchedule} />
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" checked={immediate} onChange={(event) => setImmediate(event.target.checked)} />
        <span>
          Also request a backup when the schedule is created.
          <span className="mt-1 block text-xs text-theme-text-tertiary">
            Off by default. Otherwise the first backup waits for its scheduled run.
          </span>
        </span>
      </label>
      {current && (
        <FactGrid>
          <CNPGSchedulePreviewFacts preview={current} />
        </FactGrid>
      )}
      {preview.error && (
        <button
          type="button"
          className="text-xs text-accent-text hover:underline"
          onClick={() => void preview.refetch()}
          disabled={preview.isFetching}
        >
          Retry timing check
        </button>
      )}
    </ActionConfirmDialog>
  )
}
