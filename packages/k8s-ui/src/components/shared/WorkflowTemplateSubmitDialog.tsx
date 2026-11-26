import { useId, useMemo, useState } from 'react'
import { ActionConfirmDialog } from './ActionConfirmDialog'
import { Input } from '../ui/Input'
import { SelectMenu } from '../ui/SelectMenu'
import {
  initialParameterValues,
  isAmbiguousSubmitError,
  parameterValue,
  parameterErrors,
  submittedParameters,
  workflowNameError,
  workflowTemplateEntrypoint,
  workflowTemplateParameters,
  type WorkflowTemplateSubmission,
} from '../../utils/workflow-template-submit'

const FIELD_CLASS = 'mt-1 w-full rounded-lg border border-theme-border bg-theme-base px-3 py-1.5 font-mono text-sm text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-skyhook-500'
const AMBIGUOUS_SUBMIT_MESSAGE = 'The run may have started. Check the runs below before submitting again.'

export interface WorkflowTemplateSubmitDialogProps {
  open: boolean
  onClose: () => void
  /** The template as it was when the dialog opened: what the user reviews. */
  template: any
  /** Rejecting keeps the dialog open with its values; the host reports the error. */
  onConfirm: (submission: WorkflowTemplateSubmission) => void | Promise<unknown>
  isLoading?: boolean
}

/**
 * Mount it fresh for each review (a `key` per opening): the form starts from
 * the template it was given.
 */
export function WorkflowTemplateSubmitDialog({ open, onClose, template, onConfirm, isLoading = false }: WorkflowTemplateSubmitDialogProps) {
  const namespace: string = template?.metadata?.namespace ?? ''
  const name: string = template?.metadata?.name ?? ''
  const parameters = useMemo(() => workflowTemplateParameters(template), [template])
  const [values, setValues] = useState(() => initialParameterValues(parameters))
  const [runName, setRunName] = useState('')
  const [ambiguousFailure, setAmbiguousFailure] = useState(false)
  const nameErrorId = useId()
  const fieldId = useId()

  const trimmedName = runName.trim()
  const nameError = workflowNameError(trimmedName)
  const errors = parameterErrors(parameters, values)
  const missing = parameters.filter(p => parameterValue(errors, p.name) === 'Required.').length
  const invalid = Object.keys(errors).length - missing
  const sent = submittedParameters(parameters, values)
  const entrypoint = workflowTemplateEntrypoint(template)
  const serviceAccount: string | undefined = template?.spec?.serviceAccountName || undefined
  const controllerInstance: string | undefined = template?.metadata?.labels?.['workflows.argoproj.io/controller-instanceid'] || undefined

  const confirm = () => {
    if (nameError || Object.keys(errors).length > 0 || !entrypoint || isLoading || ambiguousFailure) return
    void Promise.resolve(onConfirm({
      namespace,
      name,
      ...(trimmedName ? { runName: trimmedName } : {}),
      parameters: sent,
    })).catch((err) => {
      if (isAmbiguousSubmitError(err)) setAmbiguousFailure(true)
    })
  }

  const incompleteReason = nameError
    ? 'Use a valid run name'
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
        : undefined}
      result={ambiguousFailure
        ? { state: 'unknown', message: AMBIGUOUS_SUBMIT_MESSAGE }
        : undefined}
      notes={[
        serviceAccount
          ? <>This template sets the service account <span className="font-mono">{serviceAccount}</span> for its pods; a step can set its own.</>
          : 'This template sets no workflow-level service account: its steps or Argo’s configured default decide which one the pods use.',
        ...(controllerInstance ? [<>The run is labelled for the Argo controller with instance ID <span className="font-mono">{controllerInstance}</span>, as the template is.</>] : []),
      ]}
      writes={[{
        summary: `create Workflow ${namespace}/${trimmedName || `${name}-…`} referencing WorkflowTemplate ${name}`,
        detail: JSON.stringify({ workflowTemplateRef: { name }, ...(Object.keys(sent).length > 0 ? { arguments: { parameters: Object.entries(sent).map(([n, value]) => ({ name: n, value })) } } : {}) }, null, 2),
      }]}
    >
      <label className="block">
        <span className="flex items-baseline gap-1.5 text-xs text-theme-text-secondary">
          <span>Run name</span>
          <span className="text-theme-text-tertiary">optional</span>
        </span>
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
          placeholder={`${name}-…`}
          autoFocus={parameters.length === 0}
          spellCheck={false}
          aria-invalid={nameError !== null}
          aria-describedby={nameError ? nameErrorId : undefined}
          className={FIELD_CLASS}
        />
      </label>
      {nameError && <p id={nameErrorId} className="text-xs text-warning-text">{nameError}</p>}

      {parameters.length > 0 ? (
        <fieldset className="space-y-3">
          <legend className="mb-2 text-xs font-medium text-theme-text-secondary">Parameters</legend>
          {parameters.map((p, i) => {
            const id = `${fieldId}-${i}`
            const error = parameterValue(errors, p.name)
            const hintId = `${id}-hint`
            // "Required." is already said beside the field and in the footer.
            const shownError = error && error !== 'Required.' ? error : undefined
            const hint = shownError ?? p.description
            return (
              <div key={p.name}>
                <label htmlFor={id} className="flex items-baseline gap-1.5 text-xs text-theme-text-secondary">
                  <span className="font-mono text-theme-text-primary">{p.name}</span>
                  {p.required && <span className="text-theme-text-tertiary">required</span>}
                </label>
                {p.enum ? (
                  <SelectMenu
                    id={id}
                    value={parameterValue(values, p.name)}
                    options={[
                      ...(p.valueFromLabel ? [{ value: '', label: `From ${p.valueFromLabel}` }] : []),
                      ...p.enum.map(v => ({ value: v, label: v })),
                    ]}
                    onChange={(v) => setValues({ ...values, [p.name]: v })}
                    ariaLabel={p.name}
                    ariaDescribedBy={hint ? hintId : undefined}
                    placeholder={p.valueFromLabel ? `From ${p.valueFromLabel}` : 'Choose a value'}
                    className="mt-1 w-full"
                  />
                ) : (
                  <Input
                    id={id}
                    value={parameterValue(values, p.name)}
                    onChange={(e) => setValues({ ...values, [p.name]: e.target.value })}
                    autoFocus={i === 0}
                    spellCheck={false}
                    placeholder={p.valueFromLabel ? `From ${p.valueFromLabel}` : undefined}
                    aria-invalid={Boolean(shownError)}
                    aria-required={p.required}
                    aria-describedby={hint ? hintId : undefined}
                    className={FIELD_CLASS}
                  />
                )}
                {hint && (
                  <p id={hintId} className={shownError ? 'mt-1 text-xs text-warning-text' : 'mt-1 text-xs text-theme-text-tertiary'}>
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
