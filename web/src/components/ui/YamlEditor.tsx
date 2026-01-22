import { useRef, useCallback, useEffect } from 'react'
import Editor, { OnMount, OnChange } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'

interface YamlEditorProps {
  value: string
  onChange?: (value: string) => void
  readOnly?: boolean
  height?: string | number
  onValidate?: (isValid: boolean, errors: string[]) => void
  /** Resource kind - used to highlight editable fields for restricted resources like Pods */
  kind?: string
}

// Find line numbers for editable fields in a Pod YAML
function findPodEditableLines(yaml: string): number[] {
  const lines = yaml.split('\n')
  const editableLines: number[] = []

  let inContainers = false
  let inInitContainers = false
  let inTolerations = false
  let containerIndent = 0

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const trimmed = line.trimStart()
    const indent = line.length - trimmed.length

    // Track when we enter/exit containers section
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
      editableLines.push(i + 1) // 1-indexed
      containerIndent = indent
      continue
    }

    // Exit section when we hit same or lower indent level with a new key
    if ((inContainers || inInitContainers || inTolerations) &&
        indent <= containerIndent &&
        trimmed.length > 0 &&
        !trimmed.startsWith('-') &&
        !trimmed.startsWith('#')) {
      inContainers = false
      inInitContainers = false
      inTolerations = false
    }

    // Mark image lines as editable
    if ((inContainers || inInitContainers) && trimmed.startsWith('image:')) {
      editableLines.push(i + 1) // 1-indexed
    }

    // Mark toleration lines as editable (whole section)
    if (inTolerations && trimmed.length > 0) {
      editableLines.push(i + 1)
    }

    // activeDeadlineSeconds is editable
    if (trimmed.startsWith('activeDeadlineSeconds:')) {
      editableLines.push(i + 1)
    }

    // terminationGracePeriodSeconds is editable (with restrictions)
    if (trimmed.startsWith('terminationGracePeriodSeconds:')) {
      editableLines.push(i + 1)
    }
  }

  return editableLines
}

export function YamlEditor({
  value,
  onChange,
  readOnly = false,
  height = '100%',
  onValidate,
  kind,
}: YamlEditorProps) {
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null)
  const decorationsRef = useRef<string[]>([])
  const monacoRef = useRef<typeof import('monaco-editor') | null>(null)

  // Apply decorations for editable fields
  const applyDecorations = useCallback(() => {
    const editor = editorRef.current
    const monaco = monacoRef.current
    if (!editor || !monaco || !kind) return

    const isPod = kind.toLowerCase() === 'pods' || kind.toLowerCase() === 'pod'
    if (!isPod) {
      // Clear decorations for non-pods
      if (decorationsRef.current.length > 0) {
        decorationsRef.current = editor.deltaDecorations(decorationsRef.current, [])
      }
      return
    }

    const editableLines = findPodEditableLines(value)

    const decorations: editor.IModelDeltaDecoration[] = editableLines.map(lineNumber => ({
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
    }))

    decorationsRef.current = editor.deltaDecorations(decorationsRef.current, decorations)
  }, [value, kind])

  // Re-apply decorations when value changes
  useEffect(() => {
    applyDecorations()
  }, [applyDecorations])

  const handleEditorMount: OnMount = useCallback((editor, monaco) => {
    editorRef.current = editor
    monacoRef.current = monaco

    // Add CSS for editable line highlighting
    const styleId = 'yaml-editor-styles'
    if (!document.getElementById(styleId)) {
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

    // Configure YAML diagnostics
    monaco.languages.yaml?.yamlDefaults?.setDiagnosticsOptions({
      enableSchemaRequest: false,
      validate: true,
      format: true,
    })

    // Set editor options
    editor.updateOptions({
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
    })

    // Listen for validation markers
    if (onValidate) {
      monaco.editor.onDidChangeMarkers((uris: readonly { toString(): string }[]) => {
        const uri = uris[0]
        if (uri && uri.toString() === editor.getModel()?.uri.toString()) {
          const markers = monaco.editor.getModelMarkers({ resource: uri as Parameters<typeof monaco.editor.getModelMarkers>[0]['resource'] })
          const errors = markers
            .filter((m: { severity: number }) => m.severity === monaco.MarkerSeverity.Error)
            .map((m: { startLineNumber: number; message: string }) => `Line ${m.startLineNumber}: ${m.message}`)
          onValidate(errors.length === 0, errors)
        }
      })
    }

    // Apply initial decorations
    setTimeout(applyDecorations, 100)
  }, [onValidate, applyDecorations])

  const handleChange: OnChange = useCallback((newValue) => {
    if (onChange && newValue !== undefined) {
      onChange(newValue)
    }
  }, [onChange])

  return (
    <div className="rounded-lg overflow-hidden border border-theme-border" style={{ height }}>
      <Editor
        defaultLanguage="yaml"
        value={value}
        onChange={handleChange}
        onMount={handleEditorMount}
        theme="vs-dark"
        options={{
          readOnly,
          domReadOnly: readOnly,
        }}
        loading={
          <div className="flex items-center justify-center h-full bg-theme-surface text-theme-text-secondary">
            Loading editor...
          </div>
        }
      />
    </div>
  )
}

// Diff editor for showing changes before apply
interface YamlDiffEditorProps {
  original: string
  modified: string
  height?: string | number
}

export function YamlDiffEditor({
  original: _original,
  modified,
  height = '100%',
}: YamlDiffEditorProps) {
  // TODO: Use Monaco DiffEditor to show actual diff
  void _original
  return (
    <div className="rounded-lg overflow-hidden border border-theme-border" style={{ height }}>
      <Editor
        defaultLanguage="yaml"
        value={modified}
        theme="vs-dark"
        options={{
          readOnly: true,
          minimap: { enabled: false },
          lineNumbers: 'on',
          scrollBeyondLastLine: false,
          fontSize: 13,
          fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
          padding: { top: 12, bottom: 12 },
        }}
        loading={
          <div className="flex items-center justify-center h-full bg-theme-surface text-theme-text-secondary">
            Loading diff...
          </div>
        }
      />
    </div>
  )
}
