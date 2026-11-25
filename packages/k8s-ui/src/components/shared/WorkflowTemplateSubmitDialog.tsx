import { useId, useMemo, useRef, useState } from 'react'
import { ActionConfirmDialog } from './ActionConfirmDialog'
import { Input } from '../ui/Input'
import { SelectMenu } from '../ui/SelectMenu'
import {
  initialParameterValues,
  newSubmissionToken,
  parameterErrors,
  submittedParameters,
  suggestWorkflowName,
  workflowNameError,
  workflowTemplateEntrypoint,
  workflowTemplateParameters,
  type WorkflowTemplateSubmission,
} from '../../utils/workflow-template-submit'

const FIELD_CLASS = 'mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 font-mono text-sm text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-skyhook-500'

export interface WorkflowTemplateSubmitDialogProps {
  open: boolean
  onClose: () => void
  /** The template as it was when the dialog opened: what the user reviews. */
  template: any
  /** The template's current resourceVersion, to say when it changed under the dialog. */
  liveResourceVersion?: string
  /** Rejecting keeps the dialog open with its values; the host reports the error. */
  onConfirm: (submission: WorkflowTemplateSubmission) => void | Promise<unknown>
  isLoading?: boolean
}

/**
 * Mount it fresh for each review (a `key` per opening): the form starts from
 * the template it was given.
 */
export function WorkflowTemplateSubmitDialog({ open, onClose, template, liveResourceVersion, onConfirm, isLoading = false }: WorkflowTemplateSubmitDialogProps) {
  const namespace: string = template?.metadata?.namespace ?? ''
  const name: string = template?.metadata?.name ?? ''
  const reviewedVersion: string | undefined = template?.metadata?.resourceVersion
  const parameters = useMemo(() => workflowTemplateParameters(template), [template])
  const [values, setValues] = useState(() => initialParameterValues(parameters))
  const [runName, setRunName] = useState(() => suggestWorkflowName(name))
  // One token per distinct request: repeating an unchanged request (its
  // answer lost) returns the run it created; any edit makes a new request.
  const attempt = useRef<{ fingerprint: string; token: string } | null>(null)
  const nameErrorId = useId()
  const fieldId = useId()

  const trimmedName = runName.trim()
  const nameError = workflowNameError(trimmedName)
  const errors = parameterErrors(parameters, values)
  const missing = parameters.filter(p => errors[p.name] === 'Required.').length
  const invalid = Object.keys(errors).length - missing
  const sent = submittedParameters(parameters, values)
  const changedUnderneath = Boolean(reviewedVersion && liveResourceVersion && liveResourceVersion !== reviewedVersion)
  const fingerprint = JSON.stringify([trimmedName, sent])
  // After an attempt whose answer may have been lost, repeating it unchanged
  // stays possible even if the template changed: Radar answers with the run
  // that attempt created, or refuses it as changed when there is none.
  const repeatsAttempt = attempt.current?.fingerprint === fingerprint
  const blockedByChange = changedUnderneath && !repeatsAttempt
  const entrypoint = workflowTemplateEntrypoint(template)
  const serviceAccount: string | undefined = template?.spec?.serviceAccountName || undefined

  const confirm = () => {
    if (nameError || Object.keys(errors).length > 0 || blockedByChange || !entrypoint || isLoading) return
    const current = repeatsAttempt && attempt.current ? attempt.current : { fingerprint, token: newSubmissionToken() }
    attempt.current = current
    void Promise.resolve(onConfirm({
      namespace,
      name,
      resourceVersion: reviewedVersion,
      runName: trimmedName,
      submission: current.token,
      parameters: sent,
    })).catch(() => {})
  }

  const incompleteReason = nameError
    ? (trimmedName ? 'Use a valid run name' : 'Enter a run name')
    : missing > 0
      ? (missing === 1 ? 'Fill in the required parameter' : `Fill in ${missing} required parameters`)
      : invalid > 0 ? 'Choose an allowed value' : undefined

  return (
    <ActionConfirmDialog
      open={open}
      onClose={onClose}
      onConfirm={confirm}
      isLoading={isLoading}
      title="Submit workflow template"
      subject={{ kind: 'WorkflowTemplate', namespace, name }}
      effect={<>Creates a Workflow that references this template{entrypoint && <>, starting at <span className="font-mono">{entrypoint}</span></>}, with the values below.</>}
      confirmLabel="Submit"
      incompleteReason={incompleteReason}
      disabledReason={!entrypoint
        ? 'This template has no entrypoint, so a Workflow that references it cannot start. It is meant to be called from other workflows.'
        : blockedByChange ? 'The template changed since you opened this. Close and open Submit again to review the new version.' : undefined}
      warnings={changedUnderneath && repeatsAttempt ? ['The template changed since you opened this. Submitting again repeats your last attempt: Radar returns the run it created, or refuses it if it created none.'] : []}
      notes={[
        serviceAccount
          ? <>This template sets the service account <span className="font-mono">{serviceAccount}</span> for its pods.</>
          : 'This template sets no service account, so Argo uses its configured default.',
        'The run refers to the template by name. Argo reads the template again when it starts the run, so an edit made after you submit can still apply to it.',
      ]}
      writes={[{
        summary: `create Workflow ${namespace}/${trimmedName || '…'} referencing WorkflowTemplate ${name}`,
        detail: JSON.stringify({ workflowTemplateRef: { name }, ...(Object.keys(sent).length > 0 ? { arguments: { parameters: Object.entries(sent).map(([n, value]) => ({ name: n, value })) } } : {}) }, null, 2),
      }]}
    >
      <label className="block">
        <span className="text-xs text-theme-text-secondary">Run name</span>
        <Input
          aria-label="Run name"
          value={runName}
          onChange={(e) => setRunName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              confirm()
            }
          }}
          autoFocus={parameters.length === 0}
          spellCheck={false}
          aria-invalid={nameError !== null}
          aria-describedby={nameError ? nameErrorId : undefined}
          className={FIELD_CLASS}
        />
      </label>
      {nameError && trimmedName && <p id={nameErrorId} className="text-xs text-warning-text">{nameError}</p>}

      {parameters.length > 0 ? (
        <fieldset className="space-y-3">
          <legend className="mb-2 text-xs font-medium text-theme-text-secondary">Parameters</legend>
          {parameters.map((p, i) => {
            const id = `${fieldId}-${i}`
            const error = errors[p.name]
            const hintId = `${id}-hint`
            const hint = error && (values[p.name] ?? '') !== '' ? error : p.description
            return (
              <div key={p.name}>
                <label htmlFor={id} className="flex items-baseline gap-1.5 text-xs text-theme-text-secondary">
                  <span className="font-mono text-theme-text-primary">{p.name}</span>
                  {p.required && <span className="text-theme-text-tertiary">required</span>}
                </label>
                {p.enum ? (
                  <SelectMenu
                    id={id}
                    value={values[p.name] ?? ''}
                    options={p.enum.map(v => ({ value: v, label: v }))}
                    onChange={(v) => setValues({ ...values, [p.name]: v })}
                    ariaLabel={p.name}
                    ariaDescribedBy={hint ? hintId : undefined}
                    placeholder={p.valueFromLabel ? `From ${p.valueFromLabel}` : 'Choose a value'}
                    className="mt-1 w-full"
                  />
                ) : (
                  <Input
                    id={id}
                    value={values[p.name] ?? ''}
                    onChange={(e) => setValues({ ...values, [p.name]: e.target.value })}
                    autoFocus={i === 0}
                    spellCheck={false}
                    placeholder={p.valueFromLabel ? `From ${p.valueFromLabel}` : undefined}
                    aria-invalid={Boolean(error && (values[p.name] ?? '') !== '')}
                    aria-required={p.required}
                    aria-describedby={hint ? hintId : undefined}
                    className={FIELD_CLASS}
                  />
                )}
                {hint && (
                  <p id={hintId} className={error && (values[p.name] ?? '') !== '' ? 'mt-1 text-xs text-warning-text' : 'mt-1 text-xs text-theme-text-tertiary'}>
                    {hint}
                  </p>
                )}
              </div>
            )
          })}
        </fieldset>
      ) : (
        <p className="text-xs text-theme-text-tertiary">This template declares no parameters.</p>
      )}
    </ActionConfirmDialog>
  )
}
