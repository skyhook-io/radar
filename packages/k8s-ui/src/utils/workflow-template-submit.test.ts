import { describe, expect, it } from 'vitest'
import {
  initialParameterValues,
  parameterErrors,
  submittedParameters,
  suggestWorkflowName,
  workflowNameError,
  workflowTemplateParameters,
} from './workflow-template-submit'

const template = {
  metadata: { name: 'migrate', namespace: 'jobs' },
  spec: {
    entrypoint: 'main',
    templates: [
      { name: 'main', inputs: { parameters: [{ name: 'target' }, { name: 'message' }, { name: 'color', default: 'blue' }] } },
      { name: 'helper', inputs: { parameters: [{ name: 'unrelated' }] } },
    ],
    arguments: {
      parameters: [
        { name: 'target', description: 'Database to migrate' },
        { name: 'mode', value: 'dry-run', enum: ['dry-run', 'apply'] },
        { name: 'batch', value: 10 },
        { name: 'region', valueFrom: { configMapKeyRef: { name: 'defaults', key: 'region' } } },
        { name: 'level', enum: [1, 2] },
        { name: 'approval', valueFrom: { supplied: {} } },
        { name: 'target', value: 'duplicate ignored' },
        { value: 'nameless ignored' },
      ],
    },
  },
}

describe('workflowTemplateParameters', () => {
  it('reads each declared parameter as Argo does', () => {
    expect(workflowTemplateParameters(template)).toEqual([
      { name: 'target', description: 'Database to migrate', required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      { name: 'mode', description: undefined, required: false, defaultValue: 'dry-run', valueFromLabel: undefined, enum: ['dry-run', 'apply'] },
      { name: 'batch', description: undefined, required: false, defaultValue: '10', valueFromLabel: undefined, enum: undefined },
      { name: 'region', description: undefined, required: false, defaultValue: undefined, valueFromLabel: 'ConfigMap defaults, key region', enum: undefined },
      { name: 'level', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: ['1', '2'] },
      // Only a ConfigMap reference stands in for a value on a submitted run.
      { name: 'approval', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      // The entrypoint's inputs the arguments don't name; another template's are not offered.
      { name: 'message', description: undefined, required: true, defaultValue: undefined, valueFromLabel: undefined, enum: undefined },
      { name: 'color', description: undefined, required: false, defaultValue: 'blue', valueFromLabel: undefined, enum: undefined },
    ])
  })

  it('is empty for a template without arguments', () => {
    expect(workflowTemplateParameters({ spec: {} })).toEqual([])
    expect(workflowTemplateParameters(undefined)).toEqual([])
  })
})

describe('the form', () => {
  const parameters = workflowTemplateParameters(template)

  it('starts from the defaults and asks for each required value', () => {
    const values = initialParameterValues(parameters)
    expect(values).toEqual({ target: '', mode: 'dry-run', batch: '10', region: '', level: '', approval: '', message: '', color: 'blue' })
    expect(parameterErrors(parameters, values)).toEqual({ target: 'Required.', level: 'Required.', approval: 'Required.', message: 'Required.' })
  })

  it('refuses a value outside the enum, including a cleared default', () => {
    const filled = { target: 'x', level: '2', approval: 'yes', message: 'hi' }
    expect(parameterErrors(parameters, { ...filled, mode: 'destroy' })).toEqual({ mode: 'One of dry-run, apply.' })
    expect(parameterErrors(parameters, { ...filled, mode: '' })).toEqual({ mode: 'One of dry-run, apply.' })
  })

  it('sends what was reviewed, and leaves an empty valueFrom field to its source', () => {
    const values = { ...initialParameterValues(parameters), target: 'orders', level: '1', approval: 'yes', message: 'hi' }
    expect(submittedParameters(parameters, values)).toEqual({ target: 'orders', mode: 'dry-run', batch: '10', level: '1', approval: 'yes', message: 'hi', color: 'blue' })
    expect(submittedParameters(parameters, { ...values, region: 'eu', batch: '' })).toMatchObject({ batch: '', region: 'eu' })
  })

  it('keeps a parameter named __proto__', () => {
    const protoParams = workflowTemplateParameters({ spec: { arguments: { parameters: [{ name: '__proto__' }] } } })
    const values = { ...initialParameterValues(protoParams), ['__proto__']: 'set' }
    expect(parameterErrors(protoParams, values)).toEqual({})
    expect(Object.entries(submittedParameters(protoParams, values))).toEqual([['__proto__', 'set']])
  })
})

describe('run names', () => {
  it('suggests <template>-xxxxx within 63 characters', () => {
    expect(suggestWorkflowName('migrate')).toMatch(/^migrate-[a-z0-9]{5}$/)
    const long = suggestWorkflowName(`${'a'.repeat(56)}-.bbbb`)
    expect(long.length).toBeLessThanOrEqual(63)
    expect(workflowNameError(long)).toBeNull()
  })

  it('explains a name Argo or Kubernetes would refuse', () => {
    expect(workflowNameError('')).toBe('Enter a run name.')
    expect(workflowNameError('a'.repeat(64))).toContain('At most 63')
    expect(workflowNameError('Migrate_1')).toContain('lowercase')
    expect(workflowNameError('migrate.v2-1')).toBeNull()
  })
})
