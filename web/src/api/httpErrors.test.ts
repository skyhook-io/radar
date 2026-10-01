import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, fetchJSON } from './client'
import { CHI_UNKNOWN_ROUTE_BODY, httpStatusMessage, isUnknownRouteResponse, readErrorResponse } from './httpErrors'

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
