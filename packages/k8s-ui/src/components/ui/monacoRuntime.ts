/// <reference path="../../monaco-deep.d.ts" />

import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import 'monaco-editor/esm/vs/editor/contrib/find/browser/findController.js'
import 'monaco-editor/esm/vs/editor/contrib/folding/browser/folding.js'
import 'monaco-editor/esm/vs/editor/contrib/format/browser/formatActions.js'
import 'monaco-editor/esm/vs/editor/contrib/gotoError/browser/gotoError.js'
import 'monaco-editor/esm/vs/editor/contrib/hover/browser/hoverContribution.js'
import 'monaco-editor/esm/vs/editor/contrib/suggest/browser/suggestController.js'

type MonacoGlobal = typeof globalThis & {
  MonacoEnvironment?: {
    getWorker(moduleId: string, label: string): Worker
  }
}

let monacoReady = false

export async function ensureMonaco() {
  if (!monacoReady) {
    ;(globalThis as MonacoGlobal).MonacoEnvironment = {
      getWorker() {
        return new Worker(
          new URL('monaco-editor/esm/vs/editor/editor.worker.js', import.meta.url),
          {
            name: 'radar-monaco-editor',
            type: 'module',
          },
        )
      },
    }
    loader.config({ monaco })
    monacoReady = true
  }
  return monaco
}

export { monaco }
export type YamlMonaco = typeof monaco
