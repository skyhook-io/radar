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
    arguments: {
      parameters: [
        { name: 'target', description: 'Database to migrate' },
        { name: 'mode', value: 'dry-run', enum: ['dry-run', 'apply'] },
        { name: 'batch', value: 10 },
        { name: 'region', valueFrom: { configMapKeyRef: { name: 'defaults', key: 'region' } } },
        { name: 'level', enum: [1, 2] },
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
    expect(values).toEqual({ target: '', mode: 'dry-run', batch: '10', region: '', level: '' })
    expect(parameterErrors(parameters, values)).toEqual({ target: 'Required.', level: 'Required.' })
  })

  it('refuses a value outside the enum, including a cleared default', () => {
    expect(parameterErrors(parameters, { target: 'x', mode: 'destroy', level: '2' })).toEqual({ mode: 'One of dry-run, apply.' })
    expect(parameterErrors(parameters, { target: 'x', mode: '', level: '2' })).toEqual({ mode: 'One of dry-run, apply.' })
  })

  it('sends what was reviewed, and leaves an empty valueFrom field to its source', () => {
    const values = { ...initialParameterValues(parameters), target: 'orders', level: '1' }
    expect(submittedParameters(parameters, values)).toEqual({ target: 'orders', mode: 'dry-run', batch: '10', level: '1' })
    expect(submittedParameters(parameters, { ...values, region: 'eu', batch: '' })).toEqual({ target: 'orders', mode: 'dry-run', batch: '', region: 'eu', level: '1' })
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
