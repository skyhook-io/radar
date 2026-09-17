// The same limit /resources/apply enforces, so the number in this message is the
// number the server uses. Loading the file blocks the main thread while the
// editor parses the buffer — around 90ms at 128 KiB, 460ms at 1 MiB, 2.4s at
// 5.5 MiB — which the import covers with a loading state rather than by capping
// below what the server would accept.
export const MAX_YAML_FILE_BYTES = 6 * 1024 * 1024

const YAML_EXTENSIONS = ['.yaml', '.yml'] as const

export const YAML_FILE_ACCEPT = '.yaml,.yml'

export type YamlFileRejectionReason =
  | 'no-file'
  | 'multiple-files'
  | 'extension'
  | 'too-large'
  | 'empty'
  | 'unreadable'

export type YamlFileResult =
  | { ok: true; fileName: string; yaml: string }
  | { ok: false; reason: YamlFileRejectionReason; message: string }

function reject(reason: YamlFileRejectionReason, message: string): YamlFileResult {
  return { ok: false, reason, message }
}

function hasYamlExtension(name: string): boolean {
  const lower = name.toLowerCase()
  return YAML_EXTENSIONS.some((extension) => lower.endsWith(extension))
}

export async function readYamlFile(
  files: ArrayLike<File> | null | undefined,
): Promise<YamlFileResult> {
  if (!files || files.length === 0) {
    return reject('no-file', 'No file selected.')
  }
  if (files.length > 1) {
    return reject('multiple-files', 'Select a single YAML file — one file at a time.')
  }

  const file = files[0]
  if (!hasYamlExtension(file.name)) {
    return reject('extension', `${file.name} is not a YAML file. Choose a .yaml or .yml file.`)
  }
  if (file.size > MAX_YAML_FILE_BYTES) {
    const limit = Math.round(MAX_YAML_FILE_BYTES / (1024 * 1024))
    return reject('too-large', `${file.name} is larger than the ${limit} MiB limit.`)
  }

  let content: string
  try {
    content = await file.text()
  } catch {
    return reject('unreadable', `${file.name} could not be read. Check the file and try again.`)
  }

  if (!content.trim()) {
    return reject('empty', `${file.name} is empty.`)
  }

  return { ok: true, fileName: file.name, yaml: content }
}

export type FileDragState = 'none' | 'file' | 'multiple-files'

export function describeFileDrag(transfer: DataTransfer | null | undefined): FileDragState {
  if (!transfer) return 'none'

  // Safari keeps the dragged items protected until the drop, so `items` is
  // empty for the whole drag. Every browser advertises "Files" in `types`
  // though, which makes that the signal for whether files are coming; `items`
  // only adds the count, where the browser is willing to give one.
  const carriesFiles = transfer.types ? Array.from(transfer.types).includes('Files') : false

  let files = 0
  const items = transfer.items
  if (items) {
    for (let index = 0; index < items.length; index++) {
      if (items[index].kind === 'file') files++
    }
  }

  if (!carriesFiles && files === 0) return 'none'
  return files > 1 ? 'multiple-files' : 'file'
}

// The dialog opens prefilled in both of its entry points, so "the editor holds
// YAML" does not mean the user has work to lose. Only a divergence from what
// the dialog put there does.
export function needsReplaceConfirmation(current: string, initial: string): boolean {
  const edited = current.trim()
  return edited !== '' && edited !== initial.trim()
}
