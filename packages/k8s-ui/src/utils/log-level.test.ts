import { describe, expect, it } from 'vitest'
import {
  associateContinuations,
  detectLevel,
  groupContinuations,
  isContinuationLine,
  withoutRecordsOf,
  normalizeLevel,
  selectLevelField,
  type LevelSource,
  type LogLevel,
} from './log-level'
import { applySearchMode } from '../components/logs/useLogSearch'

// Lines taken from real clusters (lightly shortened), grouped by the library that wrote them.
const CASES: [string, string, LogLevel, LevelSource][] = [
  // klog — most Kubernetes controllers
  ['klog error', 'E0929 22:27:48.228549       1 controller.go:157] "re-queuing item due to error processing" err="secrets \\"capi-serving-cert-\\" is forbidden"', 'error', 'header'],
  ['klog info', 'I0929 22:27:51.404929       1 reflector.go:376] Caches populated for *v1.Gateway from k8s.io/client-go@v0.32.0/tools/cache/reflector.go:251', 'info', 'header'],
  ['klog warning', 'W0930 10:00:00.000001       1 warnings.go:70] v1 Endpoints is deprecated in v1.33+', 'warn', 'header'],
  ['klog fatal', 'F0930 10:00:00.000001       1 main.go:12] cannot start', 'error', 'header'],

  // logfmt — Argo CD, Grafana, slog text (Prometheus, Beyla), logrus
  ['logfmt level beats message words', 'time=2026-09-30T10:00:00Z level=info msg="retrying after error: connection reset"', 'info', 'structured'],
  ['two-pair logfmt', 'level=info msg="retrying after error"', 'info', 'structured'],
  ['argocd', 'time="2026-09-29T22:28:21Z" level=info msg="started call" grpc.component=server grpc.method=GetGitFiles', 'info', 'structured'],
  ['slog uppercase', 'time=2026-09-12T22:00:43.702Z level=WARN msg="can\'t fetch Kubernetes Cluster Name"', 'warn', 'structured'],
  ['prometheus warning text at INFO', 'time=2026-09-25T09:12:00.796Z level=INFO source=warnings.go:107 msg="Warning: v1 Endpoints is deprecated in v1.33+; use discovery.k8s.io/v1 EndpointSlice"', 'info', 'structured'],
  ['grafana', 'logger=dashboard-service t=2026-09-29T22:28:31.732506197Z level=info msg="No last resource version found, starting from scratch" orgID=1', 'info', 'structured'],
  ['level inside quoted message', 'msg="upstream said level=error" level=info', 'info', 'structured'],
  ['unrecognized structured level', 'level=custom_level msg="error happened"', 'unknown', 'structured'],

  // JSON — zap, controller-runtime, pino, GCP, ECS
  ['zap json', '{"level":"error","ts":"2026-09-29T22:20:12.561715963Z","msg":"Reconciler error","controller":"cluster"}', 'error', 'structured'],
  ['json info with error key', '{"level":"info","ts":"2026-09-29T20:42:20.831Z","message":"RequestCompleted HTTP/1.1 GET / 404","error":null}', 'info', 'structured'],
  ['pino numeric wins over severity text', '{"severity":"ERROR","level":50,"time":1790720767281,"errmsg":"Authentication failed."}', 'error', 'structured'],
  ['gcp severity', '{"time":"2026-09-29T12:18:58.483192609Z","severity":"INFO","message":"[audit] retention sweep"}', 'info', 'structured'],
  ['ecs dotted key', '{"@timestamp":"2026-09-30T10:00:00Z","log.level":"warn","message":"slow"}', 'warn', 'structured'],
  ['ecs nested', '{"log":{"level":"debug"},"message":"cache miss"}', 'debug', 'structured'],
  ['empty level falls through to next field', '{"level":"","severity":"error","msg":"failed"}', 'error', 'structured'],
  ['json without level, null error', '{"msg":"ok","error":null}', 'unknown', 'none'],

  // Console encoders and Java/Python loggers — level word in the line header
  ['zap console', '2026-09-29T22:19:30.112Z\tERROR\tprovider\tkubernetes/controller.go:639\tfailed to get Service\t{"runner": "provider"}', 'error', 'header'],
  ['zap console info', '2026-09-29T22:26:08.897Z\tINFO\tinfrastructure\trunner/runner.go:100\treceived an update', 'info', 'header'],
  ['zap verbosity', '2026-09-29T22:20:31Z\tLEVEL(-2)\tReconciling JobSet\t{"controller": "jobset"}', 'debug', 'header'],
  ['zerolog console', '2026-09-25T11:58:46Z WRN Controller is running', 'warn', 'header'],
  ['zerolog console info', '2026-09-25T11:58:46Z INF Starting controllers', 'info', 'header'],
  ['log4j kafka', '[2026-09-29 22:12:42,867] INFO [SnapshotEmitter id=0] Successfully wrote snapshot (org.apache.kafka.image.publisher.SnapshotEmitter)', 'info', 'header'],
  ['java agent prefix', '[otel.javaagent 2026-09-29 22:31:56:986 +0000] [EndpointMetricCollector] INFO software.amazon.opentelemetry.EndpointCollector - Error rate 0', 'info', 'header'],
  ['python logging', 'WARNING:root:disk almost full', 'warn', 'header'],
  ['nginx error log', '2026/09/30 10:00:00 [error] 29#29: *1 connect() failed (111: Connection refused)', 'error', 'header'],
  ['nestjs with ANSI colours', '\x1b[31m[Nest] 1  - \x1b[39m09/29/2026, 10:23:14 PM \x1b[31m  ERROR\x1b[39m \x1b[38;5;3m[MongooseModule] \x1b[39m\x1b[31mUnable to connect to the database.\x1b[39m', 'error', 'header'],
  ['nestjs LOG', '[Nest] 1  - 09/29/2026, 10:23:14 PM     LOG [RoutesResolver] AppController {/}:', 'info', 'header'],

  ['clickhouse', '2026.09.29 22:30:12.093808 [ 31 ] {} <Information> KeeperTCPHandler: Receiving request for session 36 took 4421 ms', 'info', 'header'],
  ['clickhouse error', '2026.09.29 22:30:12.093808 [ 31 ] {} <Error> TCPHandler: Code: 210. DB::NetException', 'error', 'header'],
  ['cloudwatch agent', '2026-09-29T20:38:42Z I! {"caller":"k8sapiserver/k8sapiserver.go:128","msg":"collect data from K8s API Server..."}', 'info', 'header'],
  ['cloudwatch agent error', '2026-09-29T20:38:42Z E! [outputs.cloudwatchlogs] Aws error received when sending logs', 'error', 'header'],
  ['supervisord', "2026-09-29 22:33:00,972 DEBG 'nginx' stdout output:", 'debug', 'header'],
  ['envoy bracketed', '[2026-09-29 22:43:46.517][1][info][main] [source/server/server.cc:493]   request header map: 664 bytes', 'info', 'header'],

  // Unstructured text — keyword fallback
  ['exception head', 'MongoServerError: Authentication failed.', 'error', 'keyword'],
  ['node error code', 'TypeError [ERR_INVALID_ARG_TYPE]: The "path" argument must be of type string', 'error', 'keyword'],
  ['logfmt empty level falls through', 'level= severity=error msg="failed"', 'error', 'structured'],
  ['java exception head', 'java.lang.IllegalStateException: Connection pool shut down', 'error', 'keyword'],
  ['python traceback', 'Traceback (most recent call last):', 'error', 'keyword'],
  ['go panic', 'panic: runtime error: invalid memory address or nil pointer dereference', 'error', 'keyword'],
  ['plain warning', 'deprecated flag used, warning: will be removed', 'warn', 'keyword'],
  ['key names are not severities', 'GET /healthz 200 errors=0 debug=false', 'unknown', 'none'],
  ['stack trace word is not debug', 'printing stack trace for request 42', 'unknown', 'none'],
  ['access log', '10.0.0.1 - - [30/Sep/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 612', 'unknown', 'none'],
]

describe('detectLevel', () => {
  it.each(CASES)('%s', (_name, line, level, source) => {
    expect(detectLevel(line)).toEqual({ level, source })
  })
})

describe('normalizeLevel', () => {
  it('treats absent values as no level and unrecognized names as unknown', () => {
    expect(normalizeLevel(undefined)).toBeNull()
    expect(normalizeLevel('')).toBeNull()
    expect(normalizeLevel({ name: 'error' })).toBeNull()
    expect(normalizeLevel('custom')).toBe('unknown')
  })

  it('maps pino numbers, including as strings', () => {
    expect(normalizeLevel(60)).toBe('error')
    expect(normalizeLevel('40')).toBe('warn')
    expect(normalizeLevel(30)).toBe('info')
    expect(normalizeLevel(10)).toBe('debug')
  })
})

describe('selectLevelField', () => {
  it('returns the raw value of the field the level came from', () => {
    expect(selectLevelField({ level: '', severity: 'ERROR' })).toEqual({ raw: 'ERROR', level: 'error' })
    expect(selectLevelField({ msg: 'x' })).toBeNull()
  })
})

interface TestEntry {
  id: number
  content: string
  level: LogLevel
  levelSource: LevelSource
  pod?: string
  container: string
}

function entry(id: number, content: string, pod = 'a'): TestEntry {
  const { level, source } = detectLevel(content)
  return { id, content, level, levelSource: source, pod, container: 'app' }
}

describe('isContinuationLine', () => {
  it.each([
    ['\tat com.example.Pool.get(Pool.java:10)', true],
    ['Caused by: java.io.IOException: closed', true],
    ['goroutine 1 [running]:', true],
    ['main.main()', true],
    ['net/http.(*conn).serve(0xc000112000, {0x1a2b3c, 0x4})', true],
    ['created by net/http.(*Server).Serve in goroutine 1', true],
    ['Starting server (version 1.2)', false],
    ['GET /api/v1/pods(list)', false],
    ['I0929 22:27:51.404929       1 reflector.go:376] Caches populated', false],
  ])('%s', (line, expected) => {
    expect(isContinuationLine(line)).toBe(expected)
  })

  it('keeps a Go panic together as one error record', () => {
    const lines = [
      entry(0, 'panic: runtime error: invalid memory address or nil pointer dereference'),
      entry(1, 'goroutine 1 [running]:'),
      entry(2, 'main.main()'),
      entry(3, '\t/app/main.go:12 +0x1d'),
    ]
    expect(associateContinuations(lines).effectiveLevel).toEqual(['error', 'error', 'error', 'error'])
  })
})

describe('associateContinuations', () => {
  const nodeTrace = [
    entry(0, '[Nest] 1  - 09/29/2026, 10:25:37 PM   ERROR [ExceptionHandler] MongoServerError: Authentication failed.'),
    entry(1, 'MongoServerError: Authentication failed.'),
    entry(2, '    at Connection.sendCommand (/app/node_modules/mongodb/lib/cmap/connection.js:306:27)'),
    entry(3, '    at process.processTicksAndRejections (node:internal/process/task_queues:104:5)'),
    entry(4, '[Nest] 1  - 09/29/2026, 10:25:38 PM     LOG [NestApplication] Nest application successfully started'),
  ]

  it('gives stack frames the level of the line that started their record', () => {
    const { headOf, effectiveLevel } = associateContinuations(nodeTrace)
    expect(headOf).toEqual([0, 1, 1, 1, 4])
    expect(effectiveLevel).toEqual(['error', 'error', 'error', 'error', 'info'])
  })

  it('keeps a frame whose text contains a level word inside its record', () => {
    const lines = [
      entry(0, '2026-09-30 10:00:00 ERROR [main] request failed'),
      entry(1, '    at example.debug.Handler.run(Handler.java:42)'),
    ]
    expect(lines[1].levelSource).toBe('keyword')
    expect(associateContinuations(lines).effectiveLevel).toEqual(['error', 'error'])
  })

  it('does not splice interleaved pods into each other\'s traces', () => {
    const lines = [
      entry(0, 'java.lang.IllegalStateException: pool closed', 'a'),
      entry(1, 'GET /healthz 200', 'b'),
      entry(2, '\tat com.example.Pool.get(Pool.java:10)', 'a'),
      entry(3, '\tat com.example.Pool.get(Pool.java:10)', 'c'),
    ]
    const { headOf, effectiveLevel } = associateContinuations(lines)
    expect(headOf).toEqual([0, 1, 0, 3])
    expect(effectiveLevel[2]).toBe('error')
    // A continuation with no earlier line from its own source starts its own record.
    expect(effectiveLevel[3]).toBe('unknown')
  })
})

describe('groupContinuations', () => {
  const lines = [
    entry(0, 'java.lang.IllegalStateException: pool closed', 'a'),
    entry(1, 'GET /healthz 200', 'b'),
    entry(2, '\tat com.example.Pool.get(Pool.java:10)', 'a'),
    entry(3, '\tat com.example.Main.run(Main.java:5)', 'a'),
  ]
  const { headOf } = associateContinuations(lines)
  const headIdById = new Map<number, number>()
  headOf.forEach((h, i) => { if (h !== i) headIdById.set(lines[i].id, lines[h].id) })

  it('groups frames under their own pod\'s record, not the previous row', () => {
    const groups = groupContinuations(lines, headIdById)
    expect(groups.map(g => [g.head.id, g.continuations.map(c => c.id)])).toEqual([[0, [2, 3]], [1, []]])
  })

  it('leaves frames on their own rows when their record start is filtered out', () => {
    const groups = groupContinuations(lines.slice(1), headIdById)
    expect(groups.map(g => g.head.id)).toEqual([1, 2, 3])
  })

  it('hides a whole record when its first line is hidden', () => {
    const visible = withoutRecordsOf(applySearchMode(lines, [0], 'hide'), new Set([0]), headIdById)
    expect(visible.map(e => e.id)).toEqual([1])
  })
})

describe('applySearchMode', () => {
  const lines = ['a', 'b', 'c', 'd']
  it('shows everything when highlighting', () => {
    expect(applySearchMode(lines, [1, 3], 'highlight')).toEqual(lines)
  })
  it('keeps only matches', () => {
    expect(applySearchMode(lines, [1, 3], 'only')).toEqual(['b', 'd'])
  })
  it('drops matches when hiding', () => {
    expect(applySearchMode(lines, [1, 3], 'hide')).toEqual(['a', 'c'])
  })
})
