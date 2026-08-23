import { afterEach, describe, expect, it, vi } from 'vitest'

import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'

import { installWailsClipboardShim } from './wails-clipboard'

function focusedMonacoEditor(text: string) {
  return {
    hasTextFocus: () => true,
    getSelection: () => ({}),
    getModel: () => ({ getValueInRange: () => text }),
    pushUndoStop: vi.fn(),
    executeEdits: vi.fn(),
  }
}

function setupDom({ monacoEditor = null as unknown, nativeCopyResult = true, insecureOrigin = false } = {}) {
  const origExecCommand = vi.fn(() => nativeCopyResult)
  const textarea = {
    value: '',
    style: {} as CSSStyleDeclaration,
    setAttribute: vi.fn(),
    select: vi.fn(),
    setSelectionRange: vi.fn(),
    remove: vi.fn(),
  }
  const doc = {
    execCommand: origExecCommand as Document['execCommand'],
    addEventListener: vi.fn(),
    createElement: vi.fn(() => textarea),
    body: { appendChild: vi.fn() },
    activeElement: null,
  }
  const win = { __radarMonacoEditor: monacoEditor, getSelection: () => null }
  // navigator.clipboard is a secure-context-only API — absent entirely on plain HTTP.
  const writeText = vi.fn(() => Promise.resolve())
  const nav = {
    clipboard: insecureOrigin ? undefined : { writeText, readText: vi.fn(() => Promise.resolve('')) },
  }
  vi.stubGlobal('document', doc)
  vi.stubGlobal('window', win)
  vi.stubGlobal('navigator', nav)
  installWailsClipboardShim()
  return { doc, writeText, origExecCommand, textarea }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('installWailsClipboardShim execCommand patch', () => {
  it('delegates copy to the native command when Monaco is not focused', () => {
    const { doc, writeText, origExecCommand } = setupDom()

    expect(doc.execCommand('copy')).toBe(true)

    expect(origExecCommand).toHaveBeenCalledWith('copy', undefined, undefined)
    expect(writeText).not.toHaveBeenCalled()
  })

  it('reports the native command failing instead of fabricating success', () => {
    const { doc } = setupDom({ nativeCopyResult: false })

    expect(doc.execCommand('copy')).toBe(false)
  })

  it('hijacks copy for Monaco virtual selections, which the native command cannot see', () => {
    const editor = focusedMonacoEditor('monaco-selection')
    const { doc, writeText, origExecCommand } = setupDom({ monacoEditor: editor })

    expect(doc.execCommand('copy')).toBe(true)

    expect(writeText).toHaveBeenCalledWith('monaco-selection')
    expect(origExecCommand).not.toHaveBeenCalled()
  })

  it('hijacks cut for Monaco and deletes the selection', () => {
    const editor = focusedMonacoEditor('monaco-selection')
    const { doc, writeText } = setupDom({ monacoEditor: editor })

    expect(doc.execCommand('cut')).toBe(true)

    expect(writeText).toHaveBeenCalledWith('monaco-selection')
    expect(editor.executeEdits).toHaveBeenCalledOnce()
  })

  it('delegates unrelated commands untouched', () => {
    const { doc, origExecCommand } = setupDom()

    doc.execCommand('insertText', false, 'x')

    expect(origExecCommand).toHaveBeenCalledWith('insertText', false, 'x')
  })

  it('lets copyText reach the native command through the patch when the Clipboard API is unavailable', async () => {
    const { doc, origExecCommand, textarea } = setupDom({ insecureOrigin: true })

    await expect(copyText('busybox', undefined, doc as unknown as Document)).resolves.toBe(true)

    expect(textarea.value).toBe('busybox')
    expect(origExecCommand).toHaveBeenCalledWith('copy', undefined, undefined)
  })

  it('surfaces copyText failure when the native command fails under the patch', async () => {
    const { doc } = setupDom({ nativeCopyResult: false, insecureOrigin: true })

    await expect(copyText('busybox', undefined, doc as unknown as Document)).resolves.toBe(false)
  })
})
