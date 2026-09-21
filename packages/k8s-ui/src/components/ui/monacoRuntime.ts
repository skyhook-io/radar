/// <reference path="../../monaco-deep.d.ts" />

import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
// Tokenizers only — the set the YAML editor and the pod file viewer hand to
// Monaco. JSON deliberately absent: its language lives in vs/language/json and
// spawns a validation worker this runtime does not ship; viewers highlight
// JSON with the YAML tokenizer, which covers it.
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import 'monaco-editor/esm/vs/basic-languages/xml/xml.contribution'
import 'monaco-editor/esm/vs/basic-languages/html/html.contribution'
import 'monaco-editor/esm/vs/basic-languages/css/css.contribution'
import 'monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution'
import 'monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution'
import 'monaco-editor/esm/vs/basic-languages/python/python.contribution'
import 'monaco-editor/esm/vs/basic-languages/shell/shell.contribution'
import 'monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution'
import 'monaco-editor/esm/vs/basic-languages/ini/ini.contribution'
import 'monaco-editor/esm/vs/basic-languages/dockerfile/dockerfile.contribution'
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
