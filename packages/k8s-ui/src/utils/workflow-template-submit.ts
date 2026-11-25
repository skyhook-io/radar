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
  return 'a ConfigMap'
}

/** The template's entrypoint, or '' when it has none and so can't be run directly. */
export function workflowTemplateEntrypoint(template: unknown): string {
  const entrypoint = (template as { spec?: { entrypoint?: unknown } } | undefined)?.spec?.entrypoint
  return typeof entrypoint === 'string' ? entrypoint : ''
}

/**
 * What a submission can set: the template's spec.arguments.parameters, then
 * the entrypoint template's inputs those don't name (Argo passes a Workflow's
 * arguments to the entrypoint's inputs by name).
 */
export function workflowTemplateParameters(template: unknown): WorkflowTemplateParameter[] {
  const spec = (template as { spec?: { arguments?: { parameters?: unknown }; templates?: unknown } } | undefined)?.spec
  const entrypoint = workflowTemplateEntrypoint(template)
  const entry = entrypoint && Array.isArray(spec?.templates)
    ? (spec.templates as Array<{ name?: unknown; inputs?: { parameters?: unknown } } | null>).find(t => t?.name === entrypoint)
    : undefined
  const out: WorkflowTemplateParameter[] = []
  const add = (declared: unknown, isArgument: boolean) => {
    if (!Array.isArray(declared)) return
    for (const item of declared) {
      if (!item || typeof item !== 'object') continue
      const param = item as Record<string, unknown>
      const name = typeof param.name === 'string' ? param.name : ''
      if (!name) continue
      const enumValues = Array.isArray(param.enum)
        ? param.enum.map(argoString).filter((v): v is string => v !== undefined)
        : []
      const known = out.find(p => p.name === name)
      if (known) {
        // An argument feeds the entrypoint input of the same name, so the
        // input's choices bind it too.
        if (!known.enum && enumValues.length > 0) known.enum = enumValues
        continue
      }
      // A rendered `value: null` is no value.
      const has = (key: string) => param[key] !== undefined && param[key] !== null
      // Argo resolves a ConfigMap reference for a submitted run, ahead of an
      // input's default; other sources (supplied, event) have nothing to read.
      const fromConfigMap = Boolean((param.valueFrom as { configMapKeyRef?: unknown } | undefined)?.configMapKeyRef)
      // An entrypoint input backed by a ConfigMap is left to it: any literal
      // the form sent would replace the reference.
      const defaultValue = !isArgument && fromConfigMap
        ? undefined
        : has('value')
          ? argoString(param.value) ?? ''
          : !isArgument && has('default') ? argoString(param.default) ?? '' : undefined
      // Argo's validation wants a literal value for an argument with an enum.
      const required = isArgument && enumValues.length > 0
        ? !has('value')
        : !has('value') && !(!isArgument && has('default')) && !fromConfigMap
      out.push({
        name,
        description: typeof param.description === 'string' && param.description ? param.description : undefined,
        defaultValue,
        valueFromLabel: fromConfigMap && !required ? valueFromLabel(param.valueFrom) : undefined,
        required,
        enum: enumValues.length > 0 ? enumValues : undefined,
      })
    }
  }
  add(spec?.arguments?.parameters, true)
  add(entry?.inputs?.parameters, false)
  return out
}

/** The form's starting values: each parameter's default, or empty. */
export function initialParameterValues(parameters: WorkflowTemplateParameter[]): Record<string, string> {
  return Object.fromEntries(parameters.map(p => [p.name, p.defaultValue ?? '']))
}

// Records keyed by parameter name are built with Object.fromEntries and read
// with Object.hasOwn: a parameter may be named __proto__ or constructor.
/** A record's own entry for a parameter, or ''. */
export function parameterValue(values: Record<string, string>, name: string): string {
  return Object.hasOwn(values, name) ? values[name] : ''
}

/** Why the form can't be submitted, per parameter; empty when it can. */
export function parameterErrors(parameters: WorkflowTemplateParameter[], values: Record<string, string>): Record<string, string> {
  const errors: Array<[string, string]> = []
  for (const p of parameters) {
    const value = parameterValue(values, p.name)
    // Argo accepts an empty value for a required parameter; an empty field
    // here is a value nobody chose, so the form asks for one.
    if (p.required && value === '') errors.push([p.name, 'Required.'])
    else if (p.enum && (value !== '' || p.defaultValue !== undefined) && !p.enum.includes(value)) errors.push([p.name, `One of ${p.enum.join(', ')}.`])
  }
  return Object.fromEntries(errors)
}

/**
 * The parameters to send: every field with a value, and every field whose
 * template has a value, so what the user reviewed is what runs. An empty field
 * backed by valueFrom is left out, so Argo reads it from its source.
 */
export function submittedParameters(parameters: WorkflowTemplateParameter[], values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(parameters
    .map(p => [p.name, parameterValue(values, p.name), p.defaultValue] as const)
    .filter(([, value, defaultValue]) => value !== '' || defaultValue !== undefined)
    .map(([name, value]) => [name, value]))
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
