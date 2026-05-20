import { useCallback, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  ResourceCompareView,
  CompareResourcePicker,
  parseRef,
  refToParam,
  type CompareResourceRef,
  type CompareSide,
  type CompareSideError,
} from '@skyhook-io/k8s-ui'
import { useResource, useResources } from '../../api/client'
import { useTheme } from '../../context/ThemeContext'

export function CompareViewRoute() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { theme } = useTheme()

  const kind = (searchParams.get('kind') ?? '').toLowerCase()
  const group = searchParams.get('group') ?? undefined
  const aParsed = parseRef(searchParams.get('a'))
  const bParsed = parseRef(searchParams.get('b'))

  const [pickerOpen, setPickerOpen] = useState<CompareSide | null>(null)

  const a: CompareResourceRef | null = aParsed ? { kind, namespace: aParsed.namespace, name: aParsed.name, group } : null
  const b: CompareResourceRef | null = bParsed ? { kind, namespace: bParsed.namespace, name: bParsed.name, group } : null

  const aQuery = useResource<unknown>(a?.kind ?? '', a?.namespace ?? '', a?.name ?? '', a?.group)
  const bQuery = useResource<unknown>(b?.kind ?? '', b?.namespace ?? '', b?.name ?? '', b?.group)

  const candidatesQuery = useResources<{ metadata?: { name?: string; namespace?: string } }>(
    pickerOpen ? kind : '',
    undefined,
    group,
  )
  const candidates: CompareResourceRef[] = useMemo(() => {
    if (!candidatesQuery.data) return []
    return candidatesQuery.data
      .filter(r => r?.metadata?.name)
      .map(r => ({
        kind,
        namespace: r.metadata?.namespace ?? '',
        name: r.metadata!.name!,
        group,
      }))
  }, [candidatesQuery.data, kind, group])

  const updateParam = useCallback(
    (next: Record<string, string>) => {
      const params = new URLSearchParams(searchParams)
      for (const [k, v] of Object.entries(next)) params.set(k, v)
      setSearchParams(params, { replace: true })
    },
    [searchParams, setSearchParams],
  )

  const handleSwap = useCallback(() => {
    if (!a || !b) return
    updateParam({ a: refToParam(b), b: refToParam(a) })
  }, [a, b, updateParam])

  const handleClose = useCallback(() => {
    navigate(-1)
  }, [navigate])

  const handlePick = useCallback(
    (picked: CompareResourceRef) => {
      if (!pickerOpen) return
      updateParam({ [pickerOpen]: refToParam({ namespace: picked.namespace, name: picked.name }) })
      setPickerOpen(null)
    },
    [pickerOpen, updateParam],
  )

  if (!kind || !a || !b) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-secondary gap-3 p-8">
        <p className="text-sm">This compare link is missing required parameters.</p>
        <button
          onClick={() => navigate('/resources')}
          className="px-3 py-1.5 text-xs font-medium btn-brand rounded-lg"
        >
          Back to resources
        </button>
      </div>
    )
  }

  const loading = aQuery.isPending || bQuery.isPending

  const errors: CompareSideError[] = []
  if (aQuery.error) errors.push({ side: 'a', message: aQuery.error instanceof Error ? aQuery.error.message : String(aQuery.error) })
  if (bQuery.error) errors.push({ side: 'b', message: bQuery.error instanceof Error ? bQuery.error.message : String(bQuery.error) })

  const source = pickerOpen === 'a' ? a : pickerOpen === 'b' ? b : null

  return (
    <>
      <ResourceCompareView
        a={a}
        b={b}
        aData={aQuery.data}
        bData={bQuery.data}
        loading={loading}
        errors={errors}
        editorTheme={theme === 'dark' ? 'vs-dark' : 'vs'}
        onSwap={handleSwap}
        onClose={handleClose}
        onChangeA={() => setPickerOpen('a')}
        onChangeB={() => setPickerOpen('b')}
      />
      {source && (
        <CompareResourcePicker
          open={true}
          onClose={() => setPickerOpen(null)}
          source={source}
          candidates={candidates}
          loading={candidatesQuery.isPending}
          error={candidatesQuery.error}
          onPick={handlePick}
        />
      )}
    </>
  )
}
