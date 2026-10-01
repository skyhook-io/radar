import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, fetchJSON } from './client'
import { CHI_UNKNOWN_ROUTE_BODY, httpStatusMessage, isUnknownRouteResponse, nonJsonErrorMessage, readErrorResponse } from './httpErrors'

const TEXT = 'text/plain; charset=utf-8'

function response(status: number, body: string, contentType: string | null, statusText = ''): Response {
  const headers = new Headers()
  if (contentType) headers.set('Content-Type', contentType)
  return new Response(body, { status, statusText, headers })
}

describe('isUnknownRouteResponse', () => {
  it("matches only chi's own unknown-route 404", () => {
    expect(isUnknownRouteResponse(404, TEXT, CHI_UNKNOWN_ROUTE_BODY)).toBe(true)
  })

  it("rejects a handler's JSON 404", () => {
    expect(isUnknownRouteResponse(404, 'application/json', '{"error":"pods \\"web\\" not found"}')).toBe(false)
  })

  it("rejects Radar Hub's plain-text cluster lookup 404", () => {
    expect(isUnknownRouteResponse(404, TEXT, 'not found\n')).toBe(false)
  })

  it('rejects the same body under another status or content type', () => {
    expect(isUnknownRouteResponse(405, TEXT, CHI_UNKNOWN_ROUTE_BODY)).toBe(false)
    expect(isUnknownRouteResponse(500, TEXT, CHI_UNKNOWN_ROUTE_BODY)).toBe(false)
    expect(isUnknownRouteResponse(404, 'text/html', CHI_UNKNOWN_ROUTE_BODY)).toBe(false)
    expect(isUnknownRouteResponse(404, null, CHI_UNKNOWN_ROUTE_BODY)).toBe(false)
    expect(isUnknownRouteResponse(404, TEXT, '<html>404 page not found</html>')).toBe(false)
  })
})

describe('readErrorResponse', () => {
  it('returns a JSON envelope as-is', async () => {
    const { body, unknownRoute } = await readErrorResponse(response(404, '{"error":"gone","code":7}', 'application/json'))
    expect(body).toEqual({ error: 'gone', code: 7 })
    expect(unknownRoute).toBe(false)
  })

  it('names the status instead of "Unknown error" for a non-JSON body', async () => {
    const chi = await readErrorResponse(response(404, CHI_UNKNOWN_ROUTE_BODY, TEXT))
    expect(chi).toEqual({ body: { error: 'HTTP 404 (Not Found)' }, unknownRoute: true })
    const gateway = await readErrorResponse(response(502, '<html>Bad gateway</html>', 'text/html'))
    expect(gateway).toEqual({ body: { error: 'HTTP 502 (Bad Gateway)' }, unknownRoute: false })
  })

  it("keeps a short plain-text reason such as Radar Hub's", async () => {
    const offline = await readErrorResponse(response(503, 'cluster "prod" not connected\n', TEXT))
    expect(offline.body.error).toBe('HTTP 503 (Service Unavailable): cluster "prod" not connected')
    // Hub's bare "not found" only repeats the status.
    const hub = await readErrorResponse(response(404, 'not found\n', TEXT))
    expect(hub.body.error).toBe('HTTP 404 (Not Found)')
    const long = await readErrorResponse(response(500, 'x'.repeat(500), TEXT))
    expect(long.body.error).toBe('HTTP 500 (Internal Server Error)')
  })
})

describe('nonJsonErrorMessage', () => {
  it('reads a body the caller already consumed', () => {
    expect(nonJsonErrorMessage(response(502, '', TEXT), 'upstream connect error\n')).toBe(
      'HTTP 502 (Bad Gateway): upstream connect error',
    )
    expect(nonJsonErrorMessage(response(404, '', TEXT), CHI_UNKNOWN_ROUTE_BODY)).toBe('HTTP 404 (Not Found)')
    expect(nonJsonErrorMessage(response(502, '', 'text/html'), '<html>bad</html>')).toBe('HTTP 502 (Bad Gateway)')
  })
})

describe('httpStatusMessage', () => {
  it('prefers the response status text and falls back to the table', () => {
    expect(httpStatusMessage(404, 'Nope')).toBe('HTTP 404 (Nope)')
    expect(httpStatusMessage(404, '')).toBe('HTTP 404 (Not Found)')
    expect(httpStatusMessage(418)).toBe('HTTP 418')
  })
})

describe('fetchJSON error fallback', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('reports the status and flags an unknown route', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(404, CHI_UNKNOWN_ROUTE_BODY, TEXT))))
    const error = await fetchJSON('/policy/resource/pods/default/web').catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).message).toBe('HTTP 404 (Not Found)')
    expect((error as ApiError).unknownRoute).toBe(true)
  })

  it("keeps a handler's own message and does not flag it", async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(404, '{"error":"pods \\"web\\" not found"}', 'application/json'))))
    const error = (await fetchJSON('/pods/default/web/environment').catch((e: unknown) => e)) as ApiError
    expect(error.message).toBe('pods "web" not found')
    expect(error.unknownRoute).toBe(false)
  })
})
