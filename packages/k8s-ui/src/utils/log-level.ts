import { stripAnsi } from './log-format'
import type { LogEntry } from '../components/logs/useLogBuffer'

export type LogLevel = 'error' | 'warn' | 'info' | 'debug' | 'unknown'

/**
 * Where a line's level came from. `structured` and `header` are the line
 * stating its own severity; `keyword` is a guess from words in the message, so
 * it must not override a line's membership in a stack trace or a structured
 * field that says otherwise.
 */
export type LevelSource = 'structured' | 'header' | 'keyword' | 'none'

export interface DetectedLevel {
  level: LogLevel
  source: LevelSource
}

const ERROR_NAMES = new Set(['error', 'err', 'eror', 'fatal', 'ftl', 'panic', 'pnc', 'dpanic', 'critical', 'crit', 'alert', 'emerg', 'emergency', 'severe'])
const WARN_NAMES = new Set(['warn', 'warning', 'wrn'])
const INFO_NAMES = new Set(['info', 'inf', 'information', 'notice', 'log'])
const DEBUG_NAMES = new Set(['debug', 'dbg', 'debg', 'trace', 'trc', 'verbose', 'fine', 'finer', 'finest'])

function levelFromNumber(n: number): LogLevel {
  // Pino/bunyan: 10 trace, 20 debug, 30 info, 40 warn, 50 error, 60 fatal
  if (n >= 50) return 'error'
  if (n >= 40) return 'warn'
  if (n >= 30) return 'info'
  return 'debug'
}

/**
 * Map a raw level value to a LogLevel. Returns null when the value carries no
 * level at all (missing, empty, non-scalar) so callers can fall through to the
 * next source; a present but unrecognized name is `unknown`.
 */
export function normalizeLevel(raw: unknown): LogLevel | null {
  if (typeof raw === 'number') return Number.isFinite(raw) ? levelFromNumber(raw) : null
  if (typeof raw !== 'string') return null
  const v = raw.trim().toLowerCase()
  if (!v) return null
  if (/^\d+$/.test(v)) return levelFromNumber(Number(v))
  if (ERROR_NAMES.has(v)) return 'error'
  if (WARN_NAMES.has(v)) return 'warn'
  if (INFO_NAMES.has(v)) return 'info'
  if (DEBUG_NAMES.has(v)) return 'debug'
  // zap/logr verbosity levels render as LEVEL(-2)
  if (/^level\(-\d+\)$/.test(v)) return 'debug'
  return 'unknown'
}

const LEVEL_FIELD_KEYS = ['level', 'lvl', 'severity', 'levelname', 'log.level'] as const

/**
 * Pick the level field of a structured record. Shared by detection and the
 * structured-line badge so the badge text and the line's resolved level always
 * come from the same field.
 */
export function selectLevelField(obj: Record<string, unknown>): { raw: unknown; level: LogLevel } | null {
  for (const key of LEVEL_FIELD_KEYS) {
    const level = normalizeLevel(obj[key])
    if (level) return { raw: obj[key], level }
  }
  const log = obj.log
  if (log && typeof log === 'object' && !Array.isArray(log)) {
    const raw = (log as Record<string, unknown>).level
    const level = normalizeLevel(raw)
    if (level) return { raw, level }
  }
  return null
}

// Same tokenization as parseLogfmt: a quoted value is consumed whole, so a
// `level=` inside a quoted message is never read as the line's own level.
const LOGFMT_PAIR_RE = /(?:^|\s)([a-zA-Z_][\w.]*)=((?:"(?:[^"\\]|\\.)*")|(?:[^\s]*))/g
const LOGFMT_LEVEL_KEYS = new Set(['level', 'lvl', 'severity'])
const LOGFMT_LEVEL_HINT = /(?:^|\s)(?:level|lvl|severity)=/

function logfmtLevel(line: string): LogLevel | null {
  if (!LOGFMT_LEVEL_HINT.test(line)) return null
  const re = LOGFMT_PAIR_RE
  re.lastIndex = 0
  let match: RegExpExecArray | null
  while ((match = re.exec(line)) !== null) {
    if (!LOGFMT_LEVEL_KEYS.has(match[1])) continue
    let value = match[2]
    if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) value = value.slice(1, -1)
    const level = normalizeLevel(value)
    if (level) return level
  }
  return null
}

// klog: Lmmdd hh:mm:ss.uuuuuu threadid file:line]
const KLOG_RE = /^([IWEF])\d{4} \d{2}:\d{2}:\d{2}\.\d+\s/
const KLOG_LEVELS: Record<string, LogLevel> = { I: 'info', W: 'warn', E: 'error', F: 'error' }

// A standalone uppercase level word near the start of the line: zap and
// zerolog console output, logback/log4j, Python logging, NestJS. The window
// covers prefixes such as timestamps, thread names and agent tags.
const HEADER_WINDOW = 100
const HEADER_TOKEN_RE = /(?:^|[\s[<|(])(TRACE|TRC|DEBUG|DBG|DEBG|VERBOSE|INFO|INF|LOG|NOTICE|WARN|WARNING|WRN|ERROR|ERR|FATAL|FTL|PANIC|PNC|DPANIC|CRITICAL|CRIT|SEVERE|LEVEL\(-\d+\))(?=$|[\s\]>|:)])/
// `[error]` (nginx, envoy), `<Information>` (ClickHouse)
const BRACKETED_TOKEN_RE = /[[<](trace|debug|information|info|notice|warning|warn|error|critical|crit|alert|emerg|fatal)[\]>]/i
// telegraf and the CloudWatch agent: `2026-09-29T20:38:42Z E! message`
const BANG_PREFIX_RE = /^\S+\s([DIWE])!\s/
const BANG_LEVELS: Record<string, LogLevel> = { D: 'debug', I: 'info', W: 'warn', E: 'error' }

function headerLevel(line: string): LogLevel | null {
  const klog = KLOG_RE.exec(line)
  if (klog) return KLOG_LEVELS[klog[1]]
  const bang = BANG_PREFIX_RE.exec(line)
  if (bang) return BANG_LEVELS[bang[1]]
  const head = line.slice(0, HEADER_WINDOW)
  const token = HEADER_TOKEN_RE.exec(head)
  if (token) return normalizeLevel(token[1])
  const bracketed = BRACKETED_TOKEN_RE.exec(head)
  if (bracketed) return normalizeLevel(bracketed[1])
  return null
}

// A word followed by `=` is a key name (`errors=0`, `debug=false`), and a JSON
// key with an empty value says nothing happened.
const NOT_A_KEY = String.raw`(?!\s*=)(?!"\s*:\s*(?:null|false|0|""|\[\]|\{\})\s*[,}])`
const KEYWORD_ERROR_RE = new RegExp(String.raw`\b(?:error|fatal|panic|critical|crit|exception)\b` + NOT_A_KEY)
const KEYWORD_WARN_RE = new RegExp(String.raw`\b(?:warn|warning)\b` + NOT_A_KEY)
const KEYWORD_DEBUG_RE = new RegExp(String.raw`\bdebug\b` + NOT_A_KEY)
const KEYWORD_INFO_RE = new RegExp(String.raw`\binfo\b` + NOT_A_KEY)
// The first line of an exception: `MongoServerError: ...`, `java.lang.IllegalStateException`,
// `Traceback (most recent call last):`.
const EXCEPTION_HEAD_RE = /^(?:[\w$.]*[A-Z][\w$]*(?:Error|Exception)(?: \[[\w-]+\])?(?::|\s*$)|Traceback \(most recent call last\):)/

function keywordLevel(line: string): LogLevel | null {
  if (EXCEPTION_HEAD_RE.test(line)) return 'error'
  const lower = line.toLowerCase()
  if (KEYWORD_ERROR_RE.test(lower)) return 'error'
  if (KEYWORD_WARN_RE.test(lower)) return 'warn'
  if (KEYWORD_DEBUG_RE.test(lower)) return 'debug'
  if (KEYWORD_INFO_RE.test(lower)) return 'info'
  return null
}

/**
 * Resolve a log line's severity. Sources are tried from most to least
 * authoritative, and the first that yields a level wins: a structured field,
 * then a header the logging library wrote, then words in the message.
 */
export function detectLevel(content: string): DetectedLevel {
  const line = content.includes('\x1b') ? stripAnsi(content) : content
  const trimmed = line.trim()

  if (trimmed[0] === '{' && trimmed[trimmed.length - 1] === '}') {
    try {
      const obj = JSON.parse(trimmed)
      if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
        const selected = selectLevelField(obj as Record<string, unknown>)
        if (selected) return { level: selected.level, source: 'structured' }
      }
    } catch {
      // not JSON after all
    }
  } else {
    const fromLogfmt = logfmtLevel(trimmed)
    if (fromLogfmt) return { level: fromLogfmt, source: 'structured' }
  }

  const fromHeader = headerLevel(line.trimEnd())
  if (fromHeader) return { level: fromHeader, source: 'header' }

  const fromKeyword = keywordLevel(trimmed)
  if (fromKeyword) return { level: fromKeyword, source: 'keyword' }

  return { level: 'unknown', source: 'none' }
}

export function detectLogLevel(content: string): LogLevel {
  return detectLevel(content).level
}

/**
 * Lines that continue the previous line's record rather than starting a new
 * one: stack frames and wrapped detail, which almost always start indented.
 */
export function isContinuationLine(content: string): boolean {
  // Java `\tat com.foo.Bar`, Go `\tpackage.func`, Node `    at func`, Python `  File "..."`.
  if (/^\s/.test(content)) return true
  // Java's secondary chain markers that don't start with whitespace.
  if (/^(Caused by:|Suppressed:|\.\.\. \d+ more)/.test(content)) return true
  // A Go panic prints its goroutine header and function frames unindented;
  // only the file:line under each frame is indented.
  if (content.startsWith('goroutine ') || content.startsWith('created by ')) return GO_PANIC_LINE_RE.test(content)
  const paren = content.indexOf('(')
  if (paren <= 0 || !content.endsWith(')') || content.lastIndexOf(' ', paren) !== -1) return false
  return GO_PANIC_LINE_RE.test(content)
}

// `goroutine 1 [running]:`, `main.main()`, `net/http.(*conn).serve(0xc000112000, {0x1a2b3c, 0x4})`,
// `created by net/http.(*Server).Serve in goroutine 1`
const GO_PANIC_LINE_RE = /^(?:goroutine \d+ \[[^\]]*\]:$|created by \S+|[\w./-]*[\w)\]]\.[\w*().[\]{}-]*\((?:[^()]|\([^()]*\))*\)$)/

interface AssociableEntry {
  content: string
  level: LogLevel
  levelSource?: LevelSource
  pod?: string
  container: string
}

export interface ContinuationAssociation {
  /** Index of the record's first line for each entry; equals the entry's own index for a head. */
  headOf: number[]
  /** The level filters should use: a continuation carries its record's level. */
  effectiveLevel: LogLevel[]
}

/**
 * Link each continuation line to the record it belongs to, per pod and
 * container, so a filter keeps or drops a stack trace together with its
 * first line and interleaved pods never splice into each other's traces.
 */
export function associateContinuations(entries: readonly AssociableEntry[]): ContinuationAssociation {
  const headOf = new Array<number>(entries.length)
  const effectiveLevel = new Array<LogLevel>(entries.length)
  const lastHeadBySource = new Map<string, number>()
  for (let i = 0; i < entries.length; i++) {
    const entry = entries[i]
    const source = `${entry.pod ?? ''}\x00${entry.container}`
    const lastHead = lastHeadBySource.get(source)
    const explicit = entry.levelSource === 'structured' || entry.levelSource === 'header'
    if (lastHead !== undefined && !explicit && isContinuationLine(entry.content)) {
      headOf[i] = lastHead
      effectiveLevel[i] = effectiveLevel[lastHead]
    } else {
      headOf[i] = i
      effectiveLevel[i] = entry.level
      lastHeadBySource.set(source, i)
    }
  }
  return { headOf, effectiveLevel }
}

export interface LogGroup<T extends { id: number } = LogEntry> {
  head: T
  continuations: T[]
}

/**
 * Fold visible lines into records using `headIdById` (continuation id → id of
 * its record's first line). A line whose record start is not visible keeps
 * its own row rather than attaching to an unrelated neighbour.
 */
export function groupContinuations<T extends { id: number }>(visible: readonly T[], headIdById: ReadonlyMap<number, number>): LogGroup<T>[] {
  const groups: LogGroup<T>[] = []
  const groupByHeadId = new Map<number, LogGroup<T>>()
  for (const entry of visible) {
    const headId = headIdById.get(entry.id)
    const headGroup = headId !== undefined ? groupByHeadId.get(headId) : undefined
    if (headGroup) {
      headGroup.continuations.push(entry)
    } else {
      const group: LogGroup<T> = { head: entry, continuations: [] }
      groups.push(group)
      groupByHeadId.set(entry.id, group)
    }
  }
  return groups
}

/** Drop every line whose record starts at one of `headIds`. */
export function withoutRecordsOf<T extends { id: number }>(visible: readonly T[], headIds: ReadonlySet<number>, headIdById: ReadonlyMap<number, number>): T[] {
  return visible.filter(e => {
    const headId = headIdById.get(e.id)
    return headId === undefined || !headIds.has(headId)
  })
}
