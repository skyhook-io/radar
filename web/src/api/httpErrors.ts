// Error-body handling shared by every fetch site. Kept free of client.ts
// imports so the pure helpers can be tested and reused without the hooks.

const STATUS_TEXT: Record<number, string> = {
  400: 'Bad Request',
  401: 'Unauthorized',
  403: 'Forbidden',
  404: 'Not Found',
  405: 'Method Not Allowed',
  408: 'Request Timeout',
  409: 'Conflict',
  413: 'Payload Too Large',
  429: 'Too Many Requests',
  500: 'Internal Server Error',
  502: 'Bad Gateway',
  503: 'Service Unavailable',
  504: 'Gateway Timeout',
}

/**
 * "HTTP 404 (Not Found)". The table backs up `statusText`, which HTTP/2
 * responses leave empty.
 */
export function httpStatusMessage(status: number, statusText?: string): string {
  const text = statusText || STATUS_TEXT[status]
  return text ? `HTTP ${status} (${text})` : `HTTP ${status}`
}

/** What chi's default NotFound handler writes for a route the router doesn't have. */
export const CHI_UNKNOWN_ROUTE_BODY = '404 page not found\n'

/**
 * True only for chi's own unknown-route response. Radar's handlers answer a
 * missing resource with a JSON envelope, and Radar Hub's proxy answers a
 * cluster lookup failure with a plain-text "not found", so neither matches.
 * That exactness is what lets a caller read this as "the connected Radar
 * predates this endpoint" rather than "something is missing".
 */
export function isUnknownRouteResponse(status: number, contentType: string | null, body: string): boolean {
  return (
    status === 404 &&
    (contentType ?? '').toLowerCase().startsWith('text/plain') &&
    body === CHI_UNKNOWN_ROUTE_BODY
  )
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any -- error envelopes carry handler-specific fields
export type ErrorBody = { error?: string; [key: string]: any }

export interface ErrorResponse {
  body: ErrorBody
  unknownRoute: boolean
}

const MAX_PLAIN_TEXT_REASON = 200

/**
 * Reads a failed response once. A JSON object body is returned as-is; anything
 * else becomes `{ error: <message> }` (see nonJsonErrorMessage) so callers
 * never surface a contentless "Unknown error".
 */
export async function readErrorResponse(response: Response): Promise<ErrorResponse> {
  const text = await response.text().catch(() => '')
  const contentType = response.headers.get('content-type')
  const unknownRoute = isUnknownRouteResponse(response.status, contentType, text)
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    parsed = undefined
  }
  if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
    return { body: parsed as ErrorBody, unknownRoute }
  }
  return { body: { error: nonJsonErrorMessage(response, text) }, unknownRoute }
}

/**
 * The message for a failed response whose body is not a JSON object, for
 * callers that already read the body as text. A short plain-text reason, such
 * as Radar Hub's `cluster "x" not connected`, leads and the status trails it
 * ("cluster "x" not connected (HTTP 503)"), so it reads like Radar's own
 * errors. Without one (an HTML page, chi's unknown-route body, an empty body)
 * the status is the message: "HTTP 502 (Bad Gateway)".
 */
export function nonJsonErrorMessage(response: Response, text: string): string {
  const status = httpStatusMessage(response.status, response.statusText)
  const contentType = response.headers.get('content-type')
  const reason = text.trim()
  const showReason =
    !isUnknownRouteResponse(response.status, contentType, text) &&
    (contentType ?? '').toLowerCase().startsWith('text/plain') &&
    reason.length > 0 &&
    reason.length <= MAX_PLAIN_TEXT_REASON &&
    !status.toLowerCase().includes(reason.toLowerCase())
  return showReason ? `${reason} (HTTP ${response.status})` : status
}

export async function readErrorBody(response: Response): Promise<ErrorBody> {
  return (await readErrorResponse(response)).body
}
