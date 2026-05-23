import { useCallback, useState } from 'react'
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
import { useResource } from '../../api/client'
import { useTheme } from '../../context/ThemeContext'
import { useCompareCandidates } from './useCompareCandidates'

export function CompareViewRoute() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { theme } = useTheme()

  const kind = (searchParams.get('kind') ?? '').toLowerCase()
  // Matches Radar's repo-wide URL convention. The bare `group` param is
  // reserved for topology grouping mode and gets stripped by App.tsx's URL
  // sync on every non-topology view.
  const group = searchParams.get('apiGroup') ?? undefined
  const aParsed = parseRef(searchParams.get('a'))
  const bParsed = parseRef(searchParams.get('b'))

  const [pickerOpen, setPickerOpen] = useState<CompareSide | null>(null)

  const a: CompareResourceRef | null = aParsed ? { kind, namespace: aParsed.namespace, name: aParsed.name, group } : null
  const b: CompareResourceRef | null = bParsed ? { kind, namespace: bParsed.namespace, name: bParsed.name, group } : null

  const aQuery = useResource<unknown>(a?.kind ?? '', a?.namespace ?? '', a?.name ?? '', a?.group)
  const bQuery = useResource<unknown>(b?.kind ?? '', b?.namespace ?? '', b?.name ?? '', b?.group)

  const { candidates, isPending: candidatesPending, error: candidatesError } = useCompareCandidates(kind, group, !!pickerOpen)

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

  // A refetch failure with cached data is not worth shouting about — show the
  // stale data instead of blanking the side with a misleading "failed" banner.
  const errors: CompareSideError[] = []
  if (aQuery.error && !aQuery.data) errors.push({ side: 'a', message: aQuery.error instanceof Error ? aQuery.error.message : String(aQuery.error) })
  if (bQuery.error && !bQuery.data) errors.push({ side: 'b', message: bQuery.error instanceof Error ? bQuery.error.message : String(bQuery.error) })

  const source = pickerOpen === 'a' ? a : pickerOpen === 'b' ? b : null

  return (
    <>
      <ResourceCompareView
        a={a}
        b={b}
        aData={aQuery.data}
        bData={bQuery.data}
        aLoading={aQuery.isPending}
        bLoading={bQuery.isPending}
        errors={errors}
        editorTheme={theme === 'dark' ? 'vs-dark' : 'vs'}
        onSwap={handleSwap}
        onClose={handleClose}
        onChangeA={() => setPickerOpen('a')}
        onChangeB={() => setPickerOpen('b')}
      />
      {source && pickerOpen && (
        <CompareResourcePicker
          open={true}
          onClose={() => setPickerOpen(null)}
          source={source}
          sourceSide={pickerOpen}
          candidates={candidates}
          loading={candidatesPending}
          error={candidatesError}
          onPick={handlePick}
        />
      )}
    </>
  )
}
