import { configureMonacoYaml, type JSONSchema, type MonacoYaml } from 'monaco-yaml'
import { ensureMonaco, monaco } from './monacoRuntime'

interface LegacyWorkerOptions {
  createData: unknown
  host?: unknown
  keepIdleModels?: boolean
}

const schemaRegistrations = new Map<
  string,
  { token: symbol; schema: JSONSchema; schemaUri: string }
>()
let yamlService: MonacoYaml | undefined
let schemaUpdate = Promise.resolve<unknown>(undefined)
let schemaSerial = 0

function createYamlFacade(): typeof monaco {
  // monaco-yaml 5.5 still calls the worker factory shape that Monaco 0.55 replaced.
  const nativeCreateWebWorker = monaco.editor.createWebWorker.bind(monaco.editor)
  const createWebWorker = (options: LegacyWorkerOptions) => {
    const worker = new Worker(new URL('./monacoYaml.worker.ts', import.meta.url), {
      name: 'radar-monaco-yaml',
      type: 'module',
    })
    worker.postMessage({ type: 'bootstrap' })
    worker.postMessage(options.createData)
    return nativeCreateWebWorker({
      worker: Promise.resolve(worker),
      host: options.host,
      keepIdleModels: options.keepIdleModels,
    } as Parameters<typeof monaco.editor.createWebWorker>[0])
  }

  return {
    ...monaco,
    editor: {
      ...monaco.editor,
      createWebWorker,
    },
  } as unknown as typeof monaco
}

function configuredSchemas() {
  return [...schemaRegistrations.entries()].map(([modelUri, registration]) => ({
    uri: registration.schemaUri,
    fileMatch: [modelUri],
    schema: registration.schema,
  }))
}

function refreshSchemas() {
  if (!yamlService) return Promise.resolve()
  schemaUpdate = schemaUpdate
    .catch(() => undefined)
    .then(() => yamlService?.update({ schemas: configuredSchemas() }))
  return schemaUpdate
}

export async function registerYamlSchema(
  modelUri: string,
  schemaSequence: Array<Record<string, unknown> | null>,
) {
  const token = Symbol(modelUri)
  const rootSchema = schemaSequence.find((value) => value !== null)
  if (!rootSchema) {
    await refreshSchemas()
    return () => undefined
  }
  const definitions = Object.assign({}, ...schemaSequence.map((value) => value?.definitions ?? {}))
  const schema = (
    schemaSequence.length === 1
      ? { ...rootSchema, title: 'Kubernetes cluster schema' }
      : {
          title: 'Kubernetes cluster schema',
          definitions,
          schemaSequence: schemaSequence.map((value) => {
            if (!value) return {}
            const documentSchema = { ...value }
            delete documentSchema.definitions
            return { ...documentSchema, title: 'Kubernetes cluster schema' }
          }),
        }
  ) as JSONSchema
  schemaRegistrations.set(modelUri, {
    token,
    schema,
    schemaUri: `radar://schemas/schema-${++schemaSerial}.json`,
  })
  await refreshSchemas()
  return () => {
    if (schemaRegistrations.get(modelUri)?.token !== token) return
    schemaRegistrations.delete(modelUri)
    void refreshSchemas().catch(() => undefined)
  }
}

export async function ensureYamlMonaco() {
  await ensureMonaco()
  if (!yamlService) {
    yamlService = configureMonacoYaml(createYamlFacade(), {
      enableSchemaRequest: false,
      disableAdditionalProperties: true,
      isKubernetes: true,
      format: { enable: true, printWidth: 100 },
      schemas: configuredSchemas(),
    })
  }
  return monaco
}
