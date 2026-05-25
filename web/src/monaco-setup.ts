// Load the Monaco editor from the bundled npm package instead of the default
// jsdelivr CDN. Without this, @monaco-editor/react fetches the editor at runtime
// over the network, so the YAML editor never loads in airgapped / offline
// deployments. Bundling makes the binary fully self-contained.
//
// Imported for side effects from main.tsx (Radar's binary entry) only — library
// consumers (e.g. Radar Hub) keep the default CDN loader unless they opt in.
//
// Import the editor API + YAML grammar directly rather than the `monaco-editor`
// barrel: the barrel pulls in the JSON/CSS/HTML/TypeScript language services,
// each of which bundles a heavy web worker (the TS one alone is ~7MB) that Radar
// never uses — it only ever edits YAML.
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import { loader } from '@monaco-editor/react'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'

// YAML has no dedicated Monaco language worker — the base editor worker covers
// everything we use, so route every label to it.
;(self as typeof self & { MonacoEnvironment?: { getWorker(): Worker } }).MonacoEnvironment = {
  getWorker() {
    return new EditorWorker()
  },
}

loader.config({ monaco })
