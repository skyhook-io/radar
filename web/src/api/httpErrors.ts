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

/**
 * Reads a failed response once. A JSON object body is returned as-is; anything
 * else becomes `{ error: "HTTP <status> (<text>)" }` so callers never surface a
 * contentless "Unknown error".
 */
export async function readErrorResponse(response: Response): Promise<ErrorResponse> {
  const text = await response.text().catch(() => '')
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    parsed = undefined
  }
  const body: ErrorBody =
    typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? (parsed as ErrorBody)
      : { error: httpStatusMessage(response.status, response.statusText) }
  return {
    body,
    unknownRoute: isUnknownRouteResponse(response.status, response.headers.get('content-type'), text),
  }
}

export async function readErrorBody(response: Response): Promise<ErrorBody> {
  return (await readErrorResponse(response)).body
}
