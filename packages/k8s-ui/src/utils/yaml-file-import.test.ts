import { describe, expect, it } from 'vitest'
import {
  describeFileDrag,
  MAX_YAML_FILE_BYTES,
  needsReplaceConfirmation,
  readYamlFile,
} from './yaml-file-import'

function drag(items: Array<{ kind: string; type: string }>, types: string[] = []): DataTransfer {
  return { items, types } as unknown as DataTransfer
}

// Safari keeps the dragged items protected until the drop, so `items` is empty
// mid-drag; only `types` reports that files are coming.
function safariDrag(types: string[]): DataTransfer {
  return { items: [], types } as unknown as DataTransfer
}

const MULTI_DOC = `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
apiVersion: v1
kind: Secret
metadata:
  name: second
`

function yamlFile(name: string, content: string, type = ''): File {
  return new File([content], name, { type })
}

describe('readYamlFile', () => {
  it('loads a multi-document file as text the editor can hold unchanged', async () => {
    const result = await readYamlFile([yamlFile('bundle.yaml', MULTI_DOC)])

    expect(result).toEqual({ ok: true, fileName: 'bundle.yaml', yaml: MULTI_DOC })
  })

  it('accepts .yml as readily as .yaml', async () => {
    const result = await readYamlFile([yamlFile('bundle.yml', MULTI_DOC)])

    expect(result.ok).toBe(true)
  })

  // Browsers disagree on the MIME type for YAML — macOS Chrome commonly reports
  // "", Windows reports assorted octet-stream variants — so the extension is the
  // only signal that means the same thing everywhere.
  it('accepts a YAML extension even when the browser reports no usable MIME type', async () => {
    const result = await readYamlFile([
      yamlFile('bundle.yaml', MULTI_DOC, 'application/octet-stream'),
    ])

    expect(result.ok).toBe(true)
  })

  it('rejects a non-YAML extension even when the browser claims it is YAML', async () => {
    const result = await readYamlFile([yamlFile('notes.txt', MULTI_DOC, 'text/yaml')])

    expect(result).toMatchObject({ ok: false, reason: 'extension' })
  })

  it('rejects a file that holds only whitespace', async () => {
    const result = await readYamlFile([yamlFile('empty.yaml', '  \n\n  ')])

    expect(result).toMatchObject({ ok: false, reason: 'empty' })
  })

  it('rejects a file larger than the cap without reading it', async () => {
    const oversize = yamlFile('huge.yaml', 'x'.repeat(MAX_YAML_FILE_BYTES + 1))

    expect(await readYamlFile([oversize])).toMatchObject({ ok: false, reason: 'too-large' })
  })

  it('rejects a drop carrying more than one file', async () => {
    const result = await readYamlFile([
      yamlFile('one.yaml', MULTI_DOC),
      yamlFile('two.yaml', MULTI_DOC),
    ])

    expect(result).toMatchObject({ ok: false, reason: 'multiple-files' })
  })

  it('rejects an empty selection', async () => {
    expect(await readYamlFile([])).toMatchObject({ ok: false, reason: 'no-file' })
  })

  it('reports a file the browser cannot read rather than throwing', async () => {
    const unreadable = {
      name: 'gone.yaml',
      size: 128,
      text: () => Promise.reject(new Error('NotReadableError')),
    } as unknown as File

    expect(await readYamlFile([unreadable])).toMatchObject({ ok: false, reason: 'unreadable' })
  })

  it('explains the cap in the message it hands the error banner', async () => {
    const result = await readYamlFile([yamlFile('huge.yaml', 'x'.repeat(MAX_YAML_FILE_BYTES + 1))])

    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.message).toContain('6 MiB')
  })
})

// A drag exposes only `kind` and `type`; the file name is withheld until drop,
// and YAML has no MIME type worth trusting. So the overlay promises "a file is
// coming", never "an acceptable YAML file is coming".
describe('describeFileDrag', () => {
  it('ignores a drag with nothing attached', () => {
    expect(describeFileDrag(null)).toBe('none')
  })

  it('leaves a text drag alone so the editor keeps its own drop behaviour', () => {
    expect(describeFileDrag(drag([{ kind: 'string', type: 'text/plain' }]))).toBe('none')
  })

  it('reports a single file even when the browser withholds its type', () => {
    expect(describeFileDrag(drag([{ kind: 'file', type: '' }]))).toBe('file')
  })

  it('counts only files when a drag carries companion text items', () => {
    const dragged = drag([
      { kind: 'file', type: '' },
      { kind: 'string', type: 'text/uri-list' },
    ])

    expect(describeFileDrag(dragged)).toBe('file')
  })

  it('distinguishes a multi-file drag, which is the one rejection knowable up front', () => {
    const dragged = drag([
      { kind: 'file', type: '' },
      { kind: 'file', type: '' },
    ])

    expect(describeFileDrag(dragged)).toBe('multiple-files')
  })
})

// The create dialog opens prefilled — ResourcesView with a kind skeleton,
// WorkloadView with a duplicate — so a non-empty editor is the normal state,
// not a sign the user has work worth protecting.
describe('needsReplaceConfirmation', () => {
  const SKELETON = 'apiVersion: v1\nkind: ConfigMap\n'

  it('loads straight over an untouched skeleton', () => {
    expect(needsReplaceConfirmation(SKELETON, SKELETON)).toBe(false)
  })

  it('loads straight into an empty editor', () => {
    expect(needsReplaceConfirmation('', '')).toBe(false)
  })

  it('asks before discarding edits the user actually made', () => {
    expect(needsReplaceConfirmation(`${SKELETON}  name: checkout\n`, SKELETON)).toBe(true)
  })

  it('asks before discarding a manifest pasted into an editor that opened empty', () => {
    expect(needsReplaceConfirmation(SKELETON, '')).toBe(true)
  })

  it('treats a whitespace-only difference as nothing worth protecting', () => {
    expect(needsReplaceConfirmation(`${SKELETON}\n  \n`, SKELETON)).toBe(false)
  })
})

describe('describeFileDrag across browsers', () => {
  it('sees a file drag in Safari, where items stay empty until the drop', () => {
    expect(describeFileDrag(safariDrag(['Files']))).toBe('file')
  })

  it('still ignores a Safari text drag, which advertises no Files type', () => {
    expect(describeFileDrag(safariDrag(['text/plain']))).toBe('none')
  })

  it('counts files when the browser does expose them alongside the Files type', () => {
    const dragged = drag(
      [
        { kind: 'file', type: '' },
        { kind: 'file', type: '' },
      ],
      ['Files'],
    )

    expect(describeFileDrag(dragged)).toBe('multiple-files')
  })
})
