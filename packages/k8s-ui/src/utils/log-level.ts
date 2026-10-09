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

// PostgreSQL's error_severity values (every DEBUGn is written as DEBUG);
// anything else under `record` isn't PostgreSQL's.
const POSTGRES_SEVERITIES: Record<string, LogLevel> = {
  DEBUG: 'debug', LOG: 'info', INFO: 'info', NOTICE: 'info', WARNING: 'warn', ERROR: 'error', FATAL: 'error', PANIC: 'error',
}

const LEVEL_FIELD_KEYS = ['level', 'lvl', 'severity', 'levelname', 'log.level'] as const

/**
 * Pick the level field of a structured record. Shared by detection and the
 * structured-line badge so the badge text and the line's resolved level always
 * come from the same field.
 */
export function selectLevelField(obj: Record<string, unknown>): { raw: unknown; level: LogLevel } | null {
  // CloudNativePG wraps each PostgreSQL line in an instance-manager record
  // whose own level is usually info; the database's severity is nested.
  const pgRecord = obj.record
  if (pgRecord && typeof pgRecord === 'object' && !Array.isArray(pgRecord)) {
    const raw = (pgRecord as Record<string, unknown>).error_severity
    const level = typeof raw === 'string' ? POSTGRES_SEVERITIES[raw.trim().toUpperCase()] : undefined
    if (level) return { raw, level }
  }
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
const LOGFMT_LEVEL_KEYS = new Set(['level', 'lvl', 'severity', 'log.level'])
const LOGFMT_LEVEL_HINT = /(?:^|\s)(?:level|lvl|severity|log\.level)=/

interface PlacedLevel {
  level: LogLevel
  /** Where in the line the level marker starts */
  at: number
}

function logfmtLevel(line: string): PlacedLevel | null {
  if (!LOGFMT_LEVEL_HINT.test(line)) return null
  const re = LOGFMT_PAIR_RE
  re.lastIndex = 0
  let match: RegExpExecArray | null
  while ((match = re.exec(line)) !== null) {
    if (!LOGFMT_LEVEL_KEYS.has(match[1])) continue
    let value = match[2]
    if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) value = value.slice(1, -1)
    const level = normalizeLevel(value)
    if (level) return { level, at: match.index }
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

function headerLevel(line: string): PlacedLevel | null {
  const klog = KLOG_RE.exec(line)
  if (klog) return { level: KLOG_LEVELS[klog[1]], at: 0 }
  const bang = BANG_PREFIX_RE.exec(line)
  if (bang) return { level: BANG_LEVELS[bang[1]], at: 0 }
  const head = line.slice(0, HEADER_WINDOW)
  const token = HEADER_TOKEN_RE.exec(head)
  const tokenLevel = token && normalizeLevel(token[1])
  if (token && tokenLevel) return { level: tokenLevel, at: token.index }
  const bracketed = BRACKETED_TOKEN_RE.exec(head)
  const bracketedLevel = bracketed && normalizeLevel(bracketed[1])
  if (bracketed && bracketedLevel) return { level: bracketedLevel, at: bracketed.index }
  return null
}

// A word followed by `=` is a key name (`errors=0`, `debug=false`), and a JSON
// key with an empty value says nothing happened.
const NOT_A_KEY = String.raw`(?!\s*=)(?!"\s*:\s*(?:null|false|0|""|\[\]|\{\})\s*[,}])`
const KEYWORD_ERROR_RE = new RegExp(String.raw`\b(?:error|fatal|panic|critical|crit|exception)\b` + NOT_A_KEY)
const KEYWORD_WARN_RE = new RegExp(String.raw`\b(?:warn|warning)\b` + NOT_A_KEY)
const KEYWORD_DEBUG_RE = new RegExp(String.raw`\bdebug\b` + NOT_A_KEY)
const KEYWORD_INFO_RE = new RegExp(String.raw`\binfo\b` + NOT_A_KEY)
const PYTHON_TRACEBACK_HEAD = 'Traceback (most recent call last):'
// The unindented line that ends a traceback: `ValueError: bad`, `Exception: boom`, `KeyboardInterrupt`
const PYTHON_EXCEPTION_LINE_RE = /^[A-Za-z_][\w.]*(?::|$)/
// The first line of an exception: `MongoServerError: ...`, `java.lang.IllegalStateException`,
// `Traceback (most recent call last):`.
const EXCEPTION_HEAD_RE = /^(?:(?:[\w$]+\.)*[A-Z][\w$]*(?:Error|Exception)(?: \[[\w-]+\])?(?::|\s*$)|Traceback \(most recent call last\):)/

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
    } catch {}
  }

  // A console line can carry key=value pairs in its message, so between a
  // header level word and a `level=` pair the one written first is the
  // line's own.
  const fromHeader = headerLevel(line.trimEnd())
  const fromLogfmt = trimmed[0] === '{' ? null : logfmtLevel(line.trimEnd())
  if (fromLogfmt && (!fromHeader || fromLogfmt.at <= fromHeader.at)) return { level: fromLogfmt.level, source: 'structured' }
  if (fromHeader) return { level: fromHeader.level, source: 'header' }

  const fromKeyword = keywordLevel(trimmed)
  if (fromKeyword) return { level: fromKeyword, source: 'keyword' }

  return { level: 'unknown', source: 'none' }
}

export function detectLogLevel(content: string): LogLevel {
  return detectLevel(content).level
}

const LEADING_ANSI_RE = /^(?:\x1b\[[0-9;]*m)+/

// `goroutine 1 [running]:`, `main.main()`, `net/http.(*conn).serve(0xc000112000, {0x1a2b3c, 0x4})`,
// `created by net/http.(*Server).Serve in goroutine 1`
const GO_PANIC_LINE_RE = /^(?:goroutine \d+ \[[^\]]*\]:$|created by \S+|[\w./-]*[\w)\]]\.[\w*().[\]{}-]*\((?:[^()]|\([^()]*\))*\)$)/

/**
 * Lines that continue the previous line's record rather than starting a new
 * one: stack frames and wrapped detail, which almost always start indented.
 */
export function isContinuationLine(raw: string): boolean {
  return isIndented(raw) || looksLikeUnindentedFrame(raw)
}

// Java `\tat com.foo.Bar`, Go `\tpackage.func`, Node `    at func`, Python `  File "..."`.
function isIndented(raw: string): boolean {
  // Colored output (Node's inspector) can put an escape code ahead of the indentation.
  const content = raw.charCodeAt(0) === 0x1b ? raw.replace(LEADING_ANSI_RE, '') : raw
  return /^\s/.test(content)
}

function looksLikeUnindentedFrame(content: string): boolean {
  // Java's secondary chain markers that don't start with whitespace.
  if (/^(Caused by:|Suppressed:|\.\.\. \d+ more)/.test(content)) return true
  // The closing bracket of an object printed across lines (Node's util.inspect).
  if (/^[}\])]+[;,]?$/.test(content)) return true
  // A Go panic prints its goroutine header and function frames unindented;
  // only the file:line under each frame is indented.
  if (content.startsWith('goroutine ') || content.startsWith('created by ')) return GO_PANIC_LINE_RE.test(content)
  const paren = content.indexOf('(')
  if (paren <= 0 || !content.endsWith(')') || content.lastIndexOf(' ', paren) !== -1) return false
  return GO_PANIC_LINE_RE.test(content)
}

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
  // Sources whose current record is a Python traceback still waiting for its
  // closing `ValueError: ...` line, which Python prints unindented.
  const openTraceback = new Set<string>()
  for (let i = 0; i < entries.length; i++) {
    const entry = entries[i]
    const source = `${entry.pod ?? ''}\x00${entry.container}`
    const lastHead = lastHeadBySource.get(source)
    // Indentation always continues a record, even when a frame's source text
    // holds a level word; the unindented patterns yield to a line's own level.
    const explicit = entry.levelSource === 'structured' || entry.levelSource === 'header'
    const closesTraceback = openTraceback.has(source) && PYTHON_EXCEPTION_LINE_RE.test(entry.content)
    const continues = isIndented(entry.content) || (!explicit && (closesTraceback || looksLikeUnindentedFrame(entry.content)))
    if (lastHead !== undefined && continues) {
      headOf[i] = lastHead
      effectiveLevel[i] = effectiveLevel[lastHead]
      if (closesTraceback) openTraceback.delete(source)
    } else {
      headOf[i] = i
      effectiveLevel[i] = entry.level
      lastHeadBySource.set(source, i)
      if (entry.content.startsWith(PYTHON_TRACEBACK_HEAD)) openTraceback.add(source)
      else openTraceback.delete(source)
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
