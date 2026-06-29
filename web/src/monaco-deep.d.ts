// monaco-editor's package `exports` map ("./*": "./*") doesn't surface type
// declarations for deep ESM subpaths, so TS can't resolve these imports even
// though the .js/.d.ts files exist on disk. Re-export the root types for the
// editor API and declare the YAML grammar as a side-effect-only module.
declare module 'monaco-editor/esm/vs/editor/editor.api' {
  export * from 'monaco-editor'
}
declare module 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
