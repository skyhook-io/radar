import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'

const API_BASE = '/api'

// ============================================================================
// Types
// ============================================================================

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
}

export interface ResourceContext {
  kind?: string
  namespace?: string
  name?: string
  group?: string
}

export interface ChatRequest {
  messages: ChatMessage[]
  model?: string
  context?: ResourceContext
}

export interface StreamChunk {
  content: string
  done: boolean
  error?: string
}

export interface ToolCallEvent {
  type: 'tool_call'
  toolCallId: string
  name: string
  arguments: string
}

export interface ToolResultEvent {
  type: 'tool_result'
  toolCallId: string
  name: string
  content: string
  isError: boolean
}

export type StreamEvent = StreamChunk | ToolCallEvent | ToolResultEvent

export interface ModelInfo {
  id: string
  name: string
}

export interface ProviderInfo {
  name: string
  available: boolean
  active: boolean
  models: ModelInfo[]
}

export interface ProvidersResponse {
  providers: ProviderInfo[]
  activeModel: string
}

export interface ModelsResponse {
  models: ModelInfo[]
  provider: string
}

// ============================================================================
// Streaming Chat API
// ============================================================================

/**
 * streamChat sends a chat request and yields streaming events via SSE.
 * Events can be:
 * - StreamChunk: content text or done signal
 * - ToolCallEvent: the AI is calling a tool
 * - ToolResultEvent: tool execution result
 */
export async function* streamChat(
  request: ChatRequest,
  signal?: AbortSignal
): AsyncGenerator<StreamEvent> {
  const response = await fetch(`${API_BASE}/ai/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
    signal,
  })

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }))
    throw new Error(errorData.error || `HTTP ${response.status}`)
  }

  const reader = response.body?.getReader()
  if (!reader) throw new Error('No response body')

  const decoder = new TextDecoder()
  let buffer = ''
  let currentEventType = ''

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })

      // Parse SSE events from buffer
      const lines = buffer.split('\n')
      buffer = lines.pop() || '' // Keep incomplete line in buffer

      for (const line of lines) {
        if (line.startsWith('event: ')) {
          currentEventType = line.slice(7).trim()
          continue
        } else if (line.startsWith('data: ')) {
          const data = line.slice(6)
          try {
            const parsed = JSON.parse(data)

            if (currentEventType === 'tool_call') {
              yield parsed as ToolCallEvent
            } else if (currentEventType === 'tool_result') {
              yield parsed as ToolResultEvent
            } else {
              // chunk, done, or error
              const chunk = parsed as StreamChunk
              if (chunk.error) {
                throw new Error(chunk.error)
              }
              yield chunk
              if (chunk.done) return
            }
          } catch (e) {
            if (e instanceof SyntaxError) {
              // Skip malformed JSON
              continue
            }
            throw e
          }
          currentEventType = ''
        }
      }
    }
  } finally {
    reader.releaseLock()
  }
}

// ============================================================================
// React Query Hooks
// ============================================================================

/** Fetch available AI providers and their status */
export function useAIProviders() {
  return useQuery<ProvidersResponse>({
    queryKey: ['ai', 'providers'],
    queryFn: async () => {
      const res = await fetch(`${API_BASE}/ai/providers`)
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(err.error || `HTTP ${res.status}`)
      }
      return res.json()
    },
    staleTime: 30_000,
    retry: false,
  })
}

/** Fetch models for the active provider */
export function useAIModels() {
  return useQuery<ModelsResponse>({
    queryKey: ['ai', 'models'],
    queryFn: async () => {
      const res = await fetch(`${API_BASE}/ai/models`)
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(err.error || `HTTP ${res.status}`)
      }
      return res.json()
    },
    staleTime: 30_000,
    retry: false,
  })
}

/** Switch active AI provider */
export function useSetAIProvider() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ provider, model }: { provider: string; model?: string }) => {
      const res = await fetch(`${API_BASE}/ai/provider`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider, model: model || '' }),
      })
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Unknown error' }))
        throw new Error(err.error || `HTTP ${res.status}`)
      }
      return res.json()
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ai', 'providers'] })
      queryClient.invalidateQueries({ queryKey: ['ai', 'models'] })
    },
    meta: {
      errorMessage: 'Failed to switch AI provider',
      successMessage: 'AI provider switched',
    },
  })
}
