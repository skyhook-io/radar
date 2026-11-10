// @vitest-environment jsdom
import { act, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { HelmValues } from '../../types'
import { ValuesViewer } from './ValuesViewer'

const mutations = vi.hoisted(() => ({
  apply: vi.fn(async () => undefined),
  preview: vi.fn(async () => undefined),
}))

const overridesQuery = vi.hoisted(() => ({
  current: {} as { data?: { userSupplied: Record<string, unknown> | null }; error: unknown; isFetching: boolean; refetch?: () => void },
  refetch: vi.fn(),
}))

vi.mock('@skyhook-io/k8s-ui', () => ({
  PaneLoader: ({ label }: { label: string }) => <div>{label}</div>,
  usePhoneLayout: () => false,
}))
vi.mock('../../api/client', () => ({
  useCanHelmAct: () => ({ allowed: true }),
  useHelmValues: () => ({ refetch: overridesQuery.refetch, ...overridesQuery.current }),
  useHelmApplyValues: () => ({
    mutateAsync: mutations.apply,
    isPending: false,
    error: null,
  }),
  useHelmPreviewValues: () => ({
    mutateAsync: mutations.preview,
    isPending: false,
    error: null,
  }),
}))
vi.mock('../ui/CodeViewer', () => ({
  CodeViewer: ({ code }: { code: string }) => (
    <pre data-testid="values-code">{code}</pre>
  ),
}))
vi.mock('../ui/YamlEditor', () => ({
  YamlEditor: ({ value }: { value: string }) => (
    <textarea aria-label="User Overrides YAML" value={value} readOnly />
  ),
}))
vi.mock('./ValuesDiffPreview', () => ({ ValuesDiffPreview: () => null }))
vi.mock('../ui/Tooltip', () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}))

const values: HelmValues = {
  userSupplied: { image: { tag: 'pinned' } },
  computed: {
    image: { repository: 'example/app', tag: 'pinned' },
    replicaCount: 2,
  },
}

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root

beforeEach(() => {
  overridesQuery.current = {
    data: { userSupplied: { image: { tag: 'pinned' } } },
    error: null,
    isFetching: false,
  }
  mutations.apply.mockClear()
  mutations.preview.mockClear()
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

function Harness({
  helmValues = values,
  error,
  onRetry,
}: {
  helmValues?: HelmValues | null
  error?: unknown
  onRetry?: () => void
}) {
  const [showEffectiveValues, setShowEffectiveValues] = useState(true)
  return (
    <ValuesViewer
      values={helmValues ?? undefined}
      isLoading={false}
      error={error}
      showEffectiveValues={showEffectiveValues}
      onToggleEffectiveValues={setShowEffectiveValues}
      onCopy={() => undefined}
      copied={false}
      namespace="demo"
      name="example"
      onRetry={onRetry}
    />
  )
}

async function renderViewer(node: ReactNode = <Harness />) {
  await act(async () => root.render(node))
}

function button(label: string) {
  return Array.from(document.querySelectorAll('button')).find(
    (element) => element.textContent?.trim() === label,
  )
}

describe('ValuesViewer', () => {
  it('shows read-only effective values by default and switches to user overrides', async () => {
    await renderViewer()

    expect(document.body.textContent).toContain('Effective Values')
    expect(
      document.querySelector('[data-testid="values-code"]')?.textContent,
    ).toContain('replicaCount: 2')
    expect(document.querySelector('textarea')).toBeNull()

    await act(async () => button('User Overrides')!.click())

    expect(document.body.textContent).toContain('User Overrides')
    expect(
      document.querySelector('[data-testid="values-code"]')?.textContent,
    ).toContain('tag: pinned')
    expect(
      document.querySelector('[data-testid="values-code"]')?.textContent,
    ).not.toContain('replicaCount')
  })

  it('labels editing explicitly and applies only user overrides', async () => {
    await renderViewer()

    await act(async () => button('Edit Overrides')!.click())

    expect(document.body.textContent).toContain('Editing User Overrides')
    expect(
      (document.querySelector('textarea') as HTMLTextAreaElement).value,
    ).toContain('tag: pinned')
    expect(
      (document.querySelector('textarea') as HTMLTextAreaElement).value,
    ).not.toContain('replicaCount')

    await act(async () => button('Apply')!.click())

    expect(mutations.apply).toHaveBeenCalledWith({
      namespace: 'demo',
      name: 'example',
      values: { image: { tag: 'pinned' } },
    })
  })

  it('keeps user overrides and editing usable when only effective values fail to load', async () => {
    const onRetry = vi.fn()
    await renderViewer(<Harness helmValues={null} error={new Error('forbidden')} onRetry={onRetry} />)

    expect(document.body.textContent).toContain("Couldn't load effective values.")
    expect(document.querySelector('[data-testid="values-code"]')).toBeNull()
    await act(async () => button('Try again')!.click())
    expect(onRetry).toHaveBeenCalledTimes(1)

    await act(async () => button('User Overrides')!.click())
    expect(document.body.textContent).not.toContain("Couldn't load effective values")
    expect(document.querySelector('[data-testid="values-code"]')?.textContent).toContain('tag: pinned')

    await act(async () => button('Edit Overrides')!.click())
    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toContain('tag: pinned')
    await act(async () => button('Apply')!.click())
    expect(mutations.apply).toHaveBeenCalledWith({ namespace: 'demo', name: 'example', values: { image: { tag: 'pinned' } } })
  })

  it('blocks editing when both reads fail, and says so in each view', async () => {
    overridesQuery.current = { data: undefined, error: new Error('forbidden'), isFetching: false }
    await renderViewer(<Harness helmValues={null} error={new Error('forbidden')} />)

    expect(document.body.textContent).toContain("Couldn't load effective values.")
    expect(document.body.textContent).toContain("Couldn't load your overrides, so editing is off.")
    expect((button('Edit Overrides') as HTMLButtonElement).disabled).toBe(true)

    await act(async () => button('User Overrides')!.click())
    expect(document.body.textContent).toContain("Couldn't load your overrides. Editing is off until they load.")
    expect(document.body.textContent).not.toContain('Showing the last ones that loaded')
    expect((button('Edit Overrides') as HTMLButtonElement).disabled).toBe(true)
    expect(document.querySelector('textarea')).toBeNull()
    expect(mutations.apply).not.toHaveBeenCalled()
  })

  it('keeps showing loaded values when a later refetch fails, but blocks editing', async () => {
    overridesQuery.current = { ...overridesQuery.current, error: new Error('refetch failed') }
    await renderViewer(<Harness error={new Error('refetch failed')} />)

    expect(document.body.textContent).toContain("Couldn't refresh effective values. Showing the last ones that loaded.")
    expect(document.body.textContent).toContain("Couldn't refresh your overrides, so editing is off.")
    expect(
      document.querySelector('[data-testid="values-code"]')?.textContent,
    ).toContain('replicaCount: 2')
    expect((button('Edit Overrides') as HTMLButtonElement).disabled).toBe(true)

    await act(async () => button('User Overrides')!.click())
    expect(document.body.textContent).toContain("Couldn't refresh your overrides. Showing the last ones that loaded. Editing is off until they load again.")
    expect(document.querySelector('[data-testid="values-code"]')?.textContent).toContain('tag: pinned')
  })

  it('seeds the editor from the overrides-only read, not the all-values response', async () => {
    // An older Radar can answer all=true with empty overrides when its second read fails.
    await renderViewer(<Harness helmValues={{ ...values, userSupplied: {} }} />)

    await act(async () => button('Edit Overrides')!.click())

    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toContain('tag: pinned')
  })

  it('shows overrides from the overrides-only read', async () => {
    await renderViewer(<Harness helmValues={{ ...values, userSupplied: {} }} />)

    await act(async () => button('User Overrides')!.click())

    expect(document.querySelector('[data-testid="values-code"]')?.textContent).toContain('tag: pinned')
  })

  it('lets an older Radar edit a release with no overrides (null userSupplied)', async () => {
    overridesQuery.current = { ...overridesQuery.current, data: { userSupplied: null } }
    await renderViewer()

    await act(async () => button('Edit Overrides')!.click())

    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
  })

  it('explains a failed refresh in the empty state', async () => {
    overridesQuery.current = { ...overridesQuery.current, data: { userSupplied: {} }, error: new Error('refetch failed') }
    await renderViewer(<Harness helmValues={{ ...values, userSupplied: {} }} />)

    await act(async () => button('User Overrides')!.click())

    expect(document.body.textContent).toContain('No user overrides')
    expect(document.body.textContent).toContain("Couldn't refresh your overrides. Showing the last ones that loaded.")
  })

  it('never shows the all-values copy of overrides when the overrides-only read failed', async () => {
    overridesQuery.current = { data: undefined, error: new Error('forbidden'), isFetching: false }
    await renderViewer(<Harness helmValues={{ ...values, userSupplied: {} }} />)

    await act(async () => button('User Overrides')!.click())

    expect(document.body.textContent).toContain("Couldn't load your overrides. Editing is off until they load.")
    expect(document.body.textContent).not.toContain('No user overrides')
    expect(document.body.textContent).not.toContain('Showing the last ones that loaded')
    expect((button('Edit Overrides') as HTMLButtonElement).disabled).toBe(true)

    overridesQuery.refetch.mockClear()
    await act(async () => button('Try again')!.click())
    expect(overridesQuery.refetch).toHaveBeenCalledTimes(1)
  })

  it('keeps successfully loaded empty overrides editable', async () => {
    overridesQuery.current = { ...overridesQuery.current, data: { userSupplied: {} } }
    await renderViewer(
      <Harness helmValues={{ ...values, userSupplied: {} }} />,
    )

    await act(async () => button('Edit Overrides')!.click())

    expect((document.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
    await act(async () => button('Apply')!.click())
    expect(mutations.apply).toHaveBeenCalledWith({
      namespace: 'demo',
      name: 'example',
      values: {},
    })
  })

  it('keeps the edit and blocks apply while overrides reload or fail to reload', async () => {
    await renderViewer()
    await act(async () => button('Edit Overrides')!.click())

    overridesQuery.current = { ...overridesQuery.current, isFetching: true }
    await renderViewer()
    expect(document.body.textContent).toContain('Editing User Overrides')
    expect(document.body.textContent).toContain('Reloading user overrides. Your edits are kept.')
    expect(document.querySelector('textarea')).not.toBeNull()
    expect((button('Apply') as HTMLButtonElement).disabled).toBe(true)

    overridesQuery.current = { ...overridesQuery.current, isFetching: false, error: new Error('refetch failed') }
    await renderViewer()
    expect(document.body.textContent).toContain("Couldn't reload user overrides, so Apply is off.")
    expect(document.querySelector('textarea')).not.toBeNull()
    expect((button('Apply') as HTMLButtonElement).disabled).toBe(true)

    overridesQuery.refetch.mockClear()
    await act(async () => button('Try again')!.click())
    expect(overridesQuery.refetch).toHaveBeenCalledTimes(1)
    expect(document.querySelector('textarea')).not.toBeNull()
  })
})
