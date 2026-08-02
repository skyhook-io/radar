import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import Editor, {
  DiffEditor,
  type DiffOnMount,
  type Monaco,
  type OnChange,
  type OnMount,
} from '@monaco-editor/react'
import type { editor } from 'monaco-editor'
import { AlertCircle, AlertTriangle, ChevronDown, ChevronRight, Info } from 'lucide-react'
import { parseAllDocuments, parseDocument } from 'yaml'
import { isMac } from '../../utils/platform'
import { splitYamlDocuments } from '../../utils/yaml'
import { PaneLoader } from './PaneLoader'

export interface YamlDocumentIdentity {
  index: number
  schemaIndex: number
  apiVersion: string
  kind: string
  startLine: number
}

export interface YamlSchemaLoadResult {
  schemas: Array<Record<string, unknown> | null>
  unavailable?: Array<{ index: number; reason: string }>
}

export type YamlSchemaLoader = (documents: YamlDocumentIdentity[]) => Promise<YamlSchemaLoadResult>

export interface YamlDiagnostic {
  severity: 'error' | 'warning' | 'info' | 'hint'
  message: string
  line: number
  column: number
  documentIndex: number
  blocking: boolean
}

export interface YamlEditorProps {
  value: string
  onChange?: (value: string) => void
  readOnly?: boolean
  height?: string | number
  onValidate?: (isValid: boolean, errors: string[]) => void
  onDiagnostics?: (diagnostics: YamlDiagnostic[]) => void
  schemaLoader?: YamlSchemaLoader
  kind?: string
  showProblems?: boolean
}

type SchemaStatus = 'idle' | 'loading' | 'ready' | 'partial' | 'unavailable'

export function parseYamlDocumentIdentities(value: string): YamlDocumentIdentity[] {
  return splitYamlDocuments(value).map(({ content, startLine, schemaIndex }, index) => {
    let parsed: unknown
    try {
      parsed = parseDocument(content).toJS({ maxAliasCount: 100 })
    } catch {
      parsed = undefined
    }
    const object = parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
    return {
      index,
      schemaIndex,
      apiVersion: typeof object.apiVersion === 'string' ? object.apiVersion : '',
      kind: typeof object.kind === 'string' ? object.kind : '',
      startLine,
    }
  })
}

function findPodEditableLines(yaml: string): number[] {
  const lines = yaml.split('\n')
  const editableLines: number[] = []
  let inContainers = false
  let inInitContainers = false
  let inTolerations = false
  let containerIndent = 0

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]
    const trimmed = line.trimStart()
    const indent = line.length - trimmed.length
    if (trimmed.startsWith('containers:')) {
      inContainers = true
      containerIndent = indent
      continue
    }
    if (trimmed.startsWith('initContainers:')) {
      inInitContainers = true
      containerIndent = indent
      continue
    }
    if (trimmed.startsWith('tolerations:')) {
      inTolerations = true
      editableLines.push(i + 1)
      containerIndent = indent
      continue
    }
    if (
      (inContainers || inInitContainers || inTolerations) &&
      indent <= containerIndent &&
      trimmed.length > 0 &&
      !trimmed.startsWith('-') &&
      !trimmed.startsWith('#')
    ) {
      inContainers = false
      inInitContainers = false
      inTolerations = false
    }
    if ((inContainers || inInitContainers) && trimmed.startsWith('image:')) {
      editableLines.push(i + 1)
    }
    if (inTolerations && trimmed.length > 0) editableLines.push(i + 1)
    if (trimmed.startsWith('activeDeadlineSeconds:')) editableLines.push(i + 1)
    if (trimmed.startsWith('terminationGracePeriodSeconds:')) editableLines.push(i + 1)
  }
  return editableLines
}

function documentIndexForLine(documents: YamlDocumentIdentity[], line: number) {
  let index = 0
  for (const document of documents) {
    if (document.startLine > line) break
    index = document.index
  }
  return index
}

export function parseFallbackYamlDiagnostics(value: string): YamlDiagnostic[] {
  const documents = parseYamlDocumentIdentities(value)
  return parseAllDocuments(value).flatMap((document) =>
    document.errors.map((error) => {
      const start = error.linePos?.[0]
      const line = start?.line ?? 1
      return {
        severity: 'error',
        message: error.message,
        line,
        column: start?.col ?? 1,
        documentIndex: documentIndexForLine(documents, line),
        blocking: true,
      }
    }),
  )
}

function useDocumentMonacoTheme() {
  const readTheme = () =>
    typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
  const [dark, setDark] = useState(readTheme)
  useEffect(() => {
    if (typeof document === 'undefined') return
    const observer = new MutationObserver(() => setDark(readTheme()))
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })
    return () => observer.disconnect()
  }, [])
  return dark ? ('vs-dark' as const) : ('vs' as const)
}

function markerSeverity(value: number, monaco: Monaco): YamlDiagnostic['severity'] {
  if (value === monaco.MarkerSeverity.Error) return 'error'
  if (value === monaco.MarkerSeverity.Warning) return 'warning'
  if (value === monaco.MarkerSeverity.Info) return 'info'
  return 'hint'
}

export function isBlockingYamlDiagnostic(
  severity: YamlDiagnostic['severity'],
  code?: string,
  message?: string,
  source?: string,
) {
  if (source?.startsWith('yaml-schema:')) return false
  if (severity === 'error') return true
  if (severity !== 'warning') return false
  return code !== '2' && !message?.startsWith('[radar-advisory:deprecated]')
}

export function shouldAutoTriggerYamlSuggestions(
  insertedTexts: readonly string[],
  lineContent: string,
  cursorColumn: number,
) {
  const insertedBlankLine = insertedTexts.some(
    (text) => /\r?\n/.test(text) && text.trim().length === 0,
  )
  return (
    insertedBlankLine && lineContent.trim().length === 0 && cursorColumn === lineContent.length + 1
  )
}

function displayDiagnosticMessage(message: string) {
  return message.replace(/^\[radar-advisory:deprecated\]\s*/, '')
}

export function YamlEditor({
  value,
  onChange,
  readOnly = false,
  height = '100%',
  onValidate,
  onDiagnostics,
  schemaLoader,
  kind,
  showProblems = true,
}: YamlEditorProps) {
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null)
  const decorationsRef = useRef<string[]>([])
  const markerSubscriptionRef = useRef<{ dispose(): void } | null>(null)
  const suggestionSubscriptionRef = useRef<{ dispose(): void } | null>(null)
  const suggestionTimerRef = useRef<number | null>(null)
  const schemaDisposerRef = useRef<(() => void) | null>(null)
  const monacoRef = useRef<Monaco | null>(null)
  const schemaRequestRef = useRef(0)
  const schemaLoadedOnceRef = useRef(false)
  const schemaStatusRef = useRef<SchemaStatus>(schemaLoader ? 'loading' : 'idle')
  const schemaLoaderRef = useRef(schemaLoader)
  const documentsRef = useRef<YamlDocumentIdentity[]>([])
  const onDiagnosticsRef = useRef(onDiagnostics)
  const onValidateRef = useRef(onValidate)
  const [runtimeReady, setRuntimeReady] = useState(false)
  const [runtimeError, setRuntimeError] = useState(false)
  const [runtimeAttempt, setRuntimeAttempt] = useState(0)
  const [diagnostics, setDiagnostics] = useState<YamlDiagnostic[]>([])
  const [problemsOpen, setProblemsOpen] = useState(false)
  const [schemaStatus, setSchemaStatus] = useState<SchemaStatus>(schemaLoader ? 'loading' : 'idle')
  const [schemaMessage, setSchemaMessage] = useState('')
  const [schemaUnavailable, setSchemaUnavailable] = useState<
    Array<{ index: number; reason: string }>
  >([])
  const editorId = useId()
  const modelPath = useMemo(
    () => `radar://yaml/${editorId.replace(/[^a-zA-Z0-9_-]/g, '') || 'editor'}.yaml`,
    [editorId],
  )
  const theme = useDocumentMonacoTheme()
  const documents = useMemo(() => parseYamlDocumentIdentities(value), [value])
  const identitySignature = documents
    .map(
      ({ schemaIndex, apiVersion, kind: documentKind }) =>
        `${schemaIndex}:${apiVersion}|${documentKind}`,
    )
    .join('\n')
  documentsRef.current = documents
  schemaStatusRef.current = schemaStatus
  schemaLoaderRef.current = schemaLoader
  onDiagnosticsRef.current = onDiagnostics
  onValidateRef.current = onValidate
  const suggestionShortcut = isMac() ? '⌃Space' : 'Ctrl+Space'

  useEffect(() => {
    let active = true
    setRuntimeReady(false)
    setRuntimeError(false)
    import('./yamlMonacoRuntime')
      .then(({ ensureYamlMonaco }) => ensureYamlMonaco())
      .then(() => {
        if (active) setRuntimeReady(true)
      })
      .catch(() => {
        if (active) setRuntimeError(true)
      })
    return () => {
      active = false
    }
  }, [runtimeAttempt])

  useEffect(() => {
    if (!runtimeError) return
    const next = parseFallbackYamlDiagnostics(value)
    setDiagnostics(next)
    if (next.length > 0) setProblemsOpen(true)
    onDiagnosticsRef.current?.(next)
    onValidateRef.current?.(
      next.length === 0,
      next.map((diagnostic) => `Line ${diagnostic.line}: ${diagnostic.message}`),
    )
  }, [runtimeError, value])

  useEffect(() => {
    return () => {
      markerSubscriptionRef.current?.dispose()
      suggestionSubscriptionRef.current?.dispose()
      if (suggestionTimerRef.current !== null) {
        window.clearTimeout(suggestionTimerRef.current)
      }
      schemaDisposerRef.current?.()
      const editorWindow = window as typeof window & {
        __radarMonacoEditor?: unknown
      }
      if (editorWindow.__radarMonacoEditor === editorRef.current) {
        delete editorWindow.__radarMonacoEditor
      }
    }
  }, [])

  useEffect(() => {
    schemaDisposerRef.current?.()
    schemaDisposerRef.current = null
    if (!schemaLoaderRef.current) {
      setSchemaStatus('idle')
      setSchemaMessage('')
      setSchemaUnavailable([])
      return
    }
    const completeDocuments = documents.filter((document) => document.apiVersion && document.kind)
    if (completeDocuments.length !== documents.length || documents.length === 0) {
      setSchemaStatus('unavailable')
      setSchemaMessage('Add apiVersion and kind to enable cluster-aware guidance.')
      setSchemaUnavailable(
        documents
          .filter((document) => !document.apiVersion || !document.kind)
          .map((document) => ({
            index: document.index,
            reason: 'Add apiVersion and kind to enable cluster-aware guidance.',
          })),
      )
      return
    }

    const request = ++schemaRequestRef.current
    setSchemaStatus('loading')
    setSchemaMessage('Loading schemas from this cluster…')
    setSchemaUnavailable([])
    const delay = schemaLoadedOnceRef.current ? 250 : 0
    schemaLoadedOnceRef.current = true
    const timer = window.setTimeout(() => {
      const loadSchemas = schemaLoaderRef.current
      if (!loadSchemas) return
      loadSchemas(completeDocuments)
        .then(async (result) => {
          if (request !== schemaRequestRef.current) return
          const { registerYamlSchema } = await import('./yamlMonacoRuntime')
          if (request !== schemaRequestRef.current) return
          const schemaSequence = Array.from(
            {
              length: Math.max(...documents.map((document) => document.schemaIndex)) + 1,
            },
            () => null as Record<string, unknown> | null,
          )
          documents.forEach((document, index) => {
            schemaSequence[document.schemaIndex] = result.schemas[index] ?? null
          })
          const disposeSchema = await registerYamlSchema(modelPath, schemaSequence)
          if (request !== schemaRequestRef.current) {
            disposeSchema()
            return
          }
          schemaDisposerRef.current = disposeSchema
          const unavailable = result.unavailable ?? []
          setSchemaUnavailable(unavailable)
          setSchemaStatus(
            unavailable.length === 0
              ? 'ready'
              : unavailable.length < documents.length
                ? 'partial'
                : 'unavailable',
          )
          setSchemaMessage(
            unavailable.length === 0
              ? `${documents.length} cluster schema${documents.length === 1 ? '' : 's'} active`
              : unavailable.length === documents.length
                ? unavailable[0]?.reason || 'Cluster schemas are unavailable.'
                : `${documents.length - unavailable.length} of ${documents.length} cluster schemas active`,
          )
        })
        .catch((error: unknown) => {
          if (request !== schemaRequestRef.current) return
          const reason = error instanceof Error ? error.message : 'Cluster schemas are unavailable.'
          setSchemaStatus('unavailable')
          setSchemaMessage(reason)
          setSchemaUnavailable(documents.map((document) => ({ index: document.index, reason })))
        })
    }, delay)
    return () => {
      window.clearTimeout(timer)
      schemaRequestRef.current += 1
    }
  }, [Boolean(schemaLoader), identitySignature, modelPath])

  const applyDecorations = useCallback(() => {
    const mountedEditor = editorRef.current
    const monaco = monacoRef.current
    if (!mountedEditor || !monaco || !kind) return
    const isPod = kind.toLowerCase() === 'pods' || kind.toLowerCase() === 'pod'
    if (!isPod) {
      if (decorationsRef.current.length > 0) {
        decorationsRef.current = mountedEditor.deltaDecorations(decorationsRef.current, [])
      }
      return
    }
    const decorations: editor.IModelDeltaDecoration[] = findPodEditableLines(value).map(
      (lineNumber) => ({
        range: {
          startLineNumber: lineNumber,
          startColumn: 1,
          endLineNumber: lineNumber,
          endColumn: 1,
        },
        options: {
          isWholeLine: true,
          className: 'editable-line-highlight',
          glyphMarginClassName: 'editable-line-glyph',
          overviewRuler: {
            color: 'rgba(34, 197, 94, 0.5)',
            position: monaco.editor.OverviewRulerLane.Left,
          },
        },
      }),
    )
    decorationsRef.current = mountedEditor.deltaDecorations(decorationsRef.current, decorations)
  }, [value, kind])

  useEffect(() => applyDecorations(), [applyDecorations])

  const publishDiagnostics = useCallback(
    (mountedEditor: editor.IStandaloneCodeEditor, monaco: Monaco) => {
      const model = mountedEditor.getModel()
      if (!model) return
      const next = monaco.editor.getModelMarkers({ resource: model.uri }).map((marker) => {
        const severity = markerSeverity(marker.severity, monaco)
        return {
          severity,
          message: displayDiagnosticMessage(marker.message),
          line: marker.startLineNumber,
          column: marker.startColumn,
          documentIndex: documentIndexForLine(documentsRef.current, marker.startLineNumber),
          blocking: isBlockingYamlDiagnostic(
            severity,
            typeof marker.code === 'string' ? marker.code : undefined,
            marker.message,
            marker.source,
          ),
        } satisfies YamlDiagnostic
      })
      setDiagnostics(next)
      if (next.some((diagnostic) => diagnostic.blocking)) setProblemsOpen(true)
      const blocking = next.filter((diagnostic) => diagnostic.blocking)
      onDiagnosticsRef.current?.(next)
      onValidateRef.current?.(
        blocking.length === 0,
        blocking.map((diagnostic) => `Line ${diagnostic.line}: ${diagnostic.message}`),
      )
    },
    [],
  )

  const handleEditorMount: OnMount = useCallback(
    (mountedEditor, monaco) => {
      editorRef.current = mountedEditor
      monacoRef.current = monaco
      ;(window as typeof window & { __radarMonacoEditor?: unknown }).__radarMonacoEditor =
        mountedEditor
      ensureEditableLineStyles()
      mountedEditor.updateOptions({
        minimap: { enabled: false },
        lineNumbers: 'on',
        scrollBeyondLastLine: false,
        wordWrap: 'on',
        wrappingStrategy: 'advanced',
        folding: true,
        foldingStrategy: 'indentation',
        renderLineHighlight: 'line',
        selectOnLineNumbers: true,
        roundedSelection: true,
        cursorStyle: 'line',
        automaticLayout: true,
        tabSize: 2,
        insertSpaces: true,
        fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
        padding: { top: 12, bottom: 12 },
        glyphMargin: true,
        ariaLabel: 'YAML editor',
      })
      markerSubscriptionRef.current?.dispose()
      markerSubscriptionRef.current = monaco.editor.onDidChangeMarkers((uris) => {
        if (uris.some((uri) => uri.toString() === mountedEditor.getModel()?.uri.toString())) {
          publishDiagnostics(mountedEditor, monaco)
        }
      })
      suggestionSubscriptionRef.current?.dispose()
      suggestionSubscriptionRef.current = mountedEditor.onDidChangeModelContent((event) => {
        if (
          !schemaLoaderRef.current ||
          !event.changes.some(({ text }) => /\r?\n/.test(text) && text.trim().length === 0)
        ) {
          return
        }
        if (suggestionTimerRef.current !== null) {
          window.clearTimeout(suggestionTimerRef.current)
        }
        const insertedTexts = event.changes.map(({ text }) => text)
        suggestionTimerRef.current = window.setTimeout(() => {
          suggestionTimerRef.current = null
          if (
            !mountedEditor.hasTextFocus() ||
            mountedEditor.getOption(monaco.editor.EditorOption.readOnly) ||
            !['ready', 'partial'].includes(schemaStatusRef.current)
          ) {
            return
          }
          const model = mountedEditor.getModel()
          const position = mountedEditor.getPosition()
          if (
            !model ||
            !position ||
            !shouldAutoTriggerYamlSuggestions(
              insertedTexts,
              model.getLineContent(position.lineNumber),
              position.column,
            )
          ) {
            return
          }
          mountedEditor.trigger('radar-yaml', 'editor.action.triggerSuggest', { auto: true })
        }, 120)
      })
      publishDiagnostics(mountedEditor, monaco)
      window.setTimeout(applyDecorations, 100)
    },
    [applyDecorations, publishDiagnostics],
  )

  const handleChange: OnChange = useCallback(
    (nextValue) => {
      if (onChange && nextValue !== undefined) onChange(nextValue)
    },
    [onChange],
  )

  const focusDiagnostic = useCallback((diagnostic: YamlDiagnostic) => {
    const mountedEditor = editorRef.current
    if (!mountedEditor) return
    mountedEditor.setPosition({
      lineNumber: diagnostic.line,
      column: diagnostic.column,
    })
    mountedEditor.revealLineInCenter(diagnostic.line)
    mountedEditor.focus()
  }, [])

  const blockingCount = diagnostics.filter((diagnostic) => diagnostic.blocking).length
  const problemSummary =
    blockingCount > 0
      ? `${blockingCount} blocking`
      : diagnostics.length > 0
        ? `${diagnostics.length} advisory`
        : schemaUnavailable.length > 0
          ? `${schemaUnavailable.length} schema unavailable`
          : 'None'

  return (
    <div
      className="flex flex-col rounded-lg overflow-hidden border border-theme-border bg-theme-surface"
      style={{ height }}
    >
      <div className="flex-1 min-h-0">
        {runtimeReady ? (
          <Editor
            path={modelPath}
            defaultLanguage="yaml"
            value={value}
            onChange={handleChange}
            onMount={handleEditorMount}
            theme={theme}
            options={{
              readOnly,
              domReadOnly: readOnly,
              suggest: {
                selectionMode: 'never',
              },
            }}
            loading={<PaneLoader label="Loading YAML editor…" className="h-full" />}
          />
        ) : runtimeError ? (
          <div className="flex h-full min-h-0 flex-col bg-theme-base">
            <div
              role="alert"
              className="flex shrink-0 items-center gap-2 border-b border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary"
            >
              <AlertTriangle className="h-4 w-4 shrink-0 text-warning-text" />
              <span>
                Rich YAML editing is unavailable. Basic editing and syntax validation remain
                available without completion or cluster schema guidance.
              </span>
              <button
                type="button"
                onClick={() => setRuntimeAttempt((attempt) => attempt + 1)}
                className="ml-auto rounded border border-theme-border bg-theme-surface px-2 py-1 text-theme-text-primary hover:bg-theme-hover"
              >
                Retry rich editor
              </button>
            </div>
            <textarea
              aria-label="YAML editor fallback"
              value={value}
              onChange={(event) => onChange?.(event.target.value)}
              readOnly={readOnly}
              spellCheck={false}
              className="min-h-0 flex-1 resize-none bg-theme-base p-3 font-mono text-xs leading-5 text-theme-text-primary outline-none"
            />
          </div>
        ) : (
          <PaneLoader label="Loading YAML editor…" className="h-full" />
        )}
      </div>
      {showProblems && (
        <div className="shrink-0 border-t border-theme-border bg-theme-elevated/60">
          <button
            type="button"
            onClick={() => setProblemsOpen((open) => !open)}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
            aria-expanded={problemsOpen}
          >
            {problemsOpen ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
            <span className="font-medium text-theme-text-primary">Problems</span>
            <span>{problemSummary}</span>
            <span className="ml-auto flex min-w-0 items-center gap-3 text-theme-text-tertiary">
              <span className="truncate" aria-live="polite">
                {schemaStatus === 'loading' ? 'Loading cluster schemas…' : schemaMessage}
              </span>
              {!readOnly && (schemaStatus === 'ready' || schemaStatus === 'partial') && (
                <span
                  className="flex shrink-0 items-center gap-1.5"
                  aria-label={`Select a suggestion with arrow keys, accept with Enter or Tab; show suggestions with ${suggestionShortcut}`}
                >
                  <span>Accept selected</span>
                  <kbd className="rounded border border-theme-border bg-theme-surface px-1 font-mono text-theme-text-secondary">
                    Enter / Tab
                  </kbd>
                  <span>Suggestions</span>
                  <kbd className="rounded border border-theme-border bg-theme-surface px-1 font-mono text-theme-text-secondary">
                    {suggestionShortcut}
                  </kbd>
                </span>
              )}
            </span>
          </button>
          {problemsOpen && (
            <div className="max-h-36 overflow-auto border-t border-theme-border py-1">
              {diagnostics.length === 0 && schemaUnavailable.length === 0 ? (
                <div className="px-3 py-2 text-xs text-theme-text-tertiary">
                  {schemaStatus === 'ready'
                    ? 'No syntax or schema problems found.'
                    : schemaMessage || 'No syntax problems found.'}
                </div>
              ) : (
                diagnostics.map((diagnostic, index) => {
                  const document = documents[diagnostic.documentIndex]
                  const Icon =
                    diagnostic.severity === 'error'
                      ? AlertCircle
                      : diagnostic.severity === 'warning'
                        ? AlertTriangle
                        : Info
                  return (
                    <button
                      type="button"
                      key={`${diagnostic.line}:${diagnostic.column}:${index}`}
                      onClick={() => focusDiagnostic(diagnostic)}
                      className="flex w-full items-start gap-2 px-3 py-1.5 text-left text-xs hover:bg-theme-hover"
                    >
                      <Icon
                        className={`mt-0.5 h-3.5 w-3.5 shrink-0 ${diagnostic.blocking ? 'text-red-500' : 'text-theme-text-tertiary'}`}
                      />
                      <span className="min-w-0 flex-1 text-theme-text-secondary">
                        {diagnostic.message}
                      </span>
                      <span className="shrink-0 text-theme-text-tertiary">
                        {document?.kind ? `${document.kind} · ` : ''}Ln {diagnostic.line}
                      </span>
                    </button>
                  )
                })
              )}
              {schemaUnavailable.map(({ index, reason }) => {
                const document = documents[index]
                return (
                  <div
                    key={`schema:${index}`}
                    className="flex items-start gap-2 px-3 py-1.5 text-xs text-theme-text-secondary"
                  >
                    <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning-text" />
                    <span className="min-w-0 flex-1">{reason}</span>
                    <span className="shrink-0 text-theme-text-tertiary">
                      {document?.kind || `Document ${index + 1}`}
                    </span>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export interface YamlDiffEditorProps {
  original: string
  modified: string
  height?: string | number
  unified?: boolean
  hideUnchanged?: boolean
  theme?: 'vs-dark' | 'vs'
  bleed?: boolean
  onReadyChange?: (ready: boolean) => void
}

export function YamlDiffEditor({
  original,
  modified,
  height = '100%',
  unified = false,
  hideUnchanged = false,
  theme,
  bleed = false,
  onReadyChange,
}: YamlDiffEditorProps) {
  const [runtimeReady, setRuntimeReady] = useState(false)
  const [runtimeError, setRuntimeError] = useState(false)
  const [runtimeAttempt, setRuntimeAttempt] = useState(0)
  const documentTheme = useDocumentMonacoTheme()
  const diffModelsRef = useRef<editor.IDiffEditorModel | null>(null)
  const onReadyChangeRef = useRef(onReadyChange)
  onReadyChangeRef.current = onReadyChange
  const handleDiffMount = useCallback<DiffOnMount>((diffEditor) => {
    diffModelsRef.current = diffEditor.getModel()
    onReadyChangeRef.current?.(true)
  }, [])
  useEffect(() => {
    let active = true
    setRuntimeReady(false)
    setRuntimeError(false)
    onReadyChangeRef.current?.(false)
    import('./monacoRuntime')
      .then(({ ensureMonaco }) => ensureMonaco())
      .then(() => {
        if (active) setRuntimeReady(true)
      })
      .catch(() => {
        if (active) setRuntimeError(true)
      })
    return () => {
      active = false
    }
  }, [runtimeAttempt])
  useEffect(
    () => () => {
      const models = diffModelsRef.current
      diffModelsRef.current = null
      window.setTimeout(() => {
        models?.original.dispose()
        models?.modified.dispose()
      })
    },
    [],
  )
  useEffect(() => {
    if (runtimeError) onReadyChangeRef.current?.(true)
  }, [runtimeError])
  return (
    <div
      className={
        bleed ? 'overflow-hidden' : 'rounded-lg overflow-hidden border border-theme-border'
      }
      style={{ height }}
    >
      {runtimeReady ? (
        <DiffEditor
          original={original}
          modified={modified}
          language="yaml"
          theme={theme ?? documentTheme}
          keepCurrentOriginalModel
          keepCurrentModifiedModel
          onMount={handleDiffMount}
          options={{
            readOnly: true,
            renderSideBySide: !unified,
            useInlineViewWhenSpaceIsLimited: false,
            hideUnchangedRegions: { enabled: hideUnchanged },
            minimap: { enabled: false },
            lineNumbers: 'on',
            scrollBeyondLastLine: false,
            wordWrap: 'on',
            fontSize: 13,
            fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
            padding: { top: 12, bottom: 12 },
            renderOverviewRuler: true,
            ignoreTrimWhitespace: false,
            automaticLayout: true,
          }}
          loading={<PaneLoader label="Loading diff…" className="h-full" />}
        />
      ) : runtimeError ? (
        <div className="flex h-full min-h-0 flex-col bg-theme-base">
          <div
            role="alert"
            className="flex shrink-0 items-center gap-2 border-b border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary"
          >
            <AlertTriangle className="h-4 w-4 shrink-0 text-warning-text" />
            <span>Rich diff unavailable. Review the plain YAML below.</span>
            <button
              type="button"
              onClick={() => setRuntimeAttempt((attempt) => attempt + 1)}
              className="ml-auto rounded border border-theme-border bg-theme-surface px-2 py-1 text-theme-text-primary hover:bg-theme-hover"
            >
              Retry rich diff
            </button>
          </div>
          <div className="grid min-h-0 flex-1 grid-cols-1 divide-y divide-theme-border overflow-hidden md:grid-cols-2 md:divide-x md:divide-y-0">
            <section className="flex min-h-0 flex-col">
              <div className="shrink-0 border-b border-theme-border px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
                Before
              </div>
              <pre className="min-h-0 flex-1 overflow-auto whitespace-pre p-3 font-mono text-xs text-theme-text-primary">
                {original}
              </pre>
            </section>
            <section className="flex min-h-0 flex-col">
              <div className="shrink-0 border-b border-theme-border px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
                After
              </div>
              <pre className="min-h-0 flex-1 overflow-auto whitespace-pre p-3 font-mono text-xs text-theme-text-primary">
                {modified}
              </pre>
            </section>
          </div>
        </div>
      ) : (
        <PaneLoader label="Loading diff…" className="h-full" />
      )}
    </div>
  )
}

function ensureEditableLineStyles() {
  const styleId = 'yaml-editor-styles'
  if (document.getElementById(styleId)) return
  const style = document.createElement('style')
  style.id = styleId
  style.textContent = `
    .editable-line-highlight {
      background-color: rgba(34, 197, 94, 0.1) !important;
      border-left: 3px solid rgba(34, 197, 94, 0.6) !important;
    }
    .editable-line-glyph {
      background-color: rgba(34, 197, 94, 0.6);
      width: 4px !important;
      margin-left: 3px;
      border-radius: 2px;
    }
  `
  document.head.appendChild(style)
}
