// What a WorkflowTemplate asks for when it is submitted, read as Argo reads a
// Workflow's arguments. Mirrors workflowArguments in
// internal/server/workflow_template_submit.go.

// Argo refuses a Workflow name over 63 characters: the name is copied into
// label values on the run's Pods.
const MAX_WORKFLOW_NAME = 63
const DNS1123_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const RANDOM_LENGTH = 5

export interface WorkflowTemplateParameter {
  name: string
  description?: string
  /** The template's value, as a string; absent when it has none. */
  defaultValue?: string
  /** Where the value comes from when it is left empty, e.g. "ConfigMap defaults, key region". */
  valueFromLabel?: string
  /** Neither a value nor valueFrom: the run fails without one. */
  required: boolean
  enum?: string[]
}

/** The submit request a host sends, minus the template's identity. */
export interface WorkflowTemplateSubmission {
  namespace: string
  name: string
  /** The template version the user reviewed. */
  resourceVersion?: string
  /** The Workflow's name. */
  runName: string
  /** Identifies this confirmation, so an unchanged retry returns the run it created. */
  submission: string
  parameters: Record<string, string>
}

/** The created (or, for a repeated request, existing) Workflow. */
export interface SubmittedWorkflow {
  namespace: string
  name: string
  uid: string
}

// YAML numbers and booleans are strings to Argo.
function argoString(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return undefined
}

function valueFromLabel(valueFrom: unknown): string {
  const ref = (valueFrom as { configMapKeyRef?: { name?: string; key?: string } } | undefined)?.configMapKeyRef
  if (ref?.name && ref.key) return `ConfigMap ${ref.name}, key ${ref.key}`
  return 'the template'
}

export function workflowTemplateParameters(template: unknown): WorkflowTemplateParameter[] {
  const declared = (template as { spec?: { arguments?: { parameters?: unknown } } } | undefined)?.spec?.arguments?.parameters
  if (!Array.isArray(declared)) return []
  const seen = new Set<string>()
  const out: WorkflowTemplateParameter[] = []
  for (const item of declared) {
    if (!item || typeof item !== 'object') continue
    const param = item as Record<string, unknown>
    const name = typeof param.name === 'string' ? param.name : ''
    if (!name || seen.has(name)) continue
    seen.add(name)
    const hasValue = 'value' in param
    const hasValueFrom = 'valueFrom' in param
    const enumValues = Array.isArray(param.enum)
      ? param.enum.map(argoString).filter((v): v is string => v !== undefined)
      : []
    out.push({
      name,
      description: typeof param.description === 'string' && param.description ? param.description : undefined,
      defaultValue: hasValue ? argoString(param.value) ?? '' : undefined,
      valueFromLabel: hasValueFrom ? valueFromLabel(param.valueFrom) : undefined,
      required: !hasValue && !hasValueFrom,
      enum: enumValues.length > 0 ? enumValues : undefined,
    })
  }
  return out
}

/** The form's starting values: each parameter's default, or empty. */
export function initialParameterValues(parameters: WorkflowTemplateParameter[]): Record<string, string> {
  return Object.fromEntries(parameters.map(p => [p.name, p.defaultValue ?? '']))
}

/** Why the form can't be submitted, per parameter; empty when it can. */
export function parameterErrors(parameters: WorkflowTemplateParameter[], values: Record<string, string>): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const p of parameters) {
    const value = values[p.name] ?? ''
    // Argo accepts an empty value for a required parameter; an empty field
    // here is a value nobody chose, so the form asks for one.
    if (p.required && value === '') errors[p.name] = 'Required.'
    else if (p.enum && (value !== '' || p.defaultValue !== undefined) && !p.enum.includes(value)) errors[p.name] = `One of ${p.enum.join(', ')}.`
  }
  return errors
}

/**
 * The parameters to send: every field with a value, and every field whose
 * template has a value, so what the user reviewed is what runs. An empty field
 * backed by valueFrom is left out, so Argo reads it from its source.
 */
export function submittedParameters(parameters: WorkflowTemplateParameter[], values: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of parameters) {
    const value = values[p.name] ?? ''
    if (value !== '' || p.defaultValue !== undefined) out[p.name] = value
  }
  return out
}

/** Why `name` can't name a Workflow, or null when it can. */
export function workflowNameError(name: string): string | null {
  if (!name) return 'Enter a run name.'
  if (name.length > MAX_WORKFLOW_NAME) return `At most ${MAX_WORKFLOW_NAME} characters: Argo copies the name into labels on the run's Pods.`
  if (!DNS1123_SUBDOMAIN.test(name)) return "Use lowercase letters, digits, '-' and '.', starting and ending with a letter or digit."
  return null
}

/** `<template>-xxxxx`, as Argo names a submitted run, cut to fit 63 characters. */
export function suggestWorkflowName(templateName: string): string {
  const base = templateName.slice(0, MAX_WORKFLOW_NAME - RANDOM_LENGTH - 1).replace(/[-.]+$/, '')
  return `${base}-${randomToken(RANDOM_LENGTH)}`
}

/** A submission token: 8–64 letters, digits or hyphens. */
export function newSubmissionToken(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `s-${randomToken(16)}`
}

function randomToken(length: number): string {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  return Array.from(crypto.getRandomValues(new Uint8Array(length)), (b) => alphabet[b % alphabet.length]).join('')
}
