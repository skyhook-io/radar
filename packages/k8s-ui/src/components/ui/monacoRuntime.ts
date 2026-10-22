/// <reference path="../../monaco-deep.d.ts" />

import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor/editor/editor.api'
// Tokenizers only — the set the YAML editor and the pod file viewer hand to
// Monaco. JSON deliberately absent: its language lives in vs/language/json and
// spawns a validation worker this runtime does not ship; viewers highlight
// JSON with the YAML tokenizer, which covers it.
import 'monaco-editor/languages/definitions/yaml/register'
import 'monaco-editor/languages/definitions/xml/register'
import 'monaco-editor/languages/definitions/html/register'
import 'monaco-editor/languages/definitions/css/register'
import 'monaco-editor/languages/definitions/javascript/register'
import 'monaco-editor/languages/definitions/typescript/register'
import 'monaco-editor/languages/definitions/python/register'
import 'monaco-editor/languages/definitions/shell/register'
import 'monaco-editor/languages/definitions/markdown/register'
import 'monaco-editor/languages/definitions/ini/register'
import 'monaco-editor/languages/definitions/dockerfile/register'
import './monacoEditorFeatures'

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
          new URL('monaco-editor/editor/editor.worker.js', import.meta.url),
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
