// @monaco-editor/react and @monaco-editor/loader type monaco through this path,
// which monaco-editor's exports map does not serve.
declare module 'monaco-editor/esm/vs/editor/editor.api' {
  export * from 'monaco-editor/editor/editor.api'
}

declare module 'monaco-editor/languages/definitions/yaml/register'
declare module 'monaco-editor/languages/definitions/xml/register'
declare module 'monaco-editor/languages/definitions/html/register'
declare module 'monaco-editor/languages/definitions/css/register'
declare module 'monaco-editor/languages/definitions/javascript/register'
declare module 'monaco-editor/languages/definitions/typescript/register'
declare module 'monaco-editor/languages/definitions/python/register'
declare module 'monaco-editor/languages/definitions/shell/register'
declare module 'monaco-editor/languages/definitions/markdown/register'
declare module 'monaco-editor/languages/definitions/ini/register'
declare module 'monaco-editor/languages/definitions/dockerfile/register'
declare module 'monaco-editor/editor/contrib/*'
declare module 'monaco-editor/editor/standalone/*'
declare module 'monaco-editor/editor/browser/*'
declare module 'monaco-editor/editor/common/*'
declare module 'monaco-editor/features/*'
