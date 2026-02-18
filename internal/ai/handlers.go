package ai

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handlers provides HTTP handlers for AI endpoints
type Handlers struct {
	registry *Registry
}

// NewHandlers creates new AI handlers
func NewHandlers(registry *Registry) *Handlers {
	return &Handlers{registry: registry}
}

// RegisterRoutes registers AI routes on a chi router
// Streaming routes should be registered outside the timeout group
func (h *Handlers) RegisterStreamingRoutes(r chi.Router) {
	r.Post("/ai/chat", h.handleChat)
}

// RegisterRoutes registers non-streaming AI routes (inside timeout group)
func (h *Handlers) RegisterRoutes(r chi.Router) {
	r.Get("/ai/providers", h.handleListProviders)
	r.Post("/ai/provider", h.handleSetProvider)
	r.Get("/ai/models", h.handleListModels)
}

// ChatRequest is the request body for /api/ai/chat
type ChatRequest struct {
	Messages []Message        `json:"messages"`
	Model    string           `json:"model,omitempty"`
	Context  *ResourceContext `json:"context,omitempty"`
}

// Maximum number of tool call rounds per chat request to prevent infinite loops
const maxToolRounds = 5

// handleChat streams an AI response via SSE, with tool execution support
func (h *Handlers) handleChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages are required")
		return
	}

	provider, activeModel := h.registry.GetActive()
	if provider == nil {
		writeError(w, http.StatusServiceUnavailable, "no AI provider configured. Start radar with --ai-provider and --ai-api-key flags")
		return
	}

	model := req.Model
	if model == "" {
		model = activeModel
	}

	// Build system prompt with K8s context
	systemPrompt := BuildSystemPrompt(r.Context(), req.Context)

	// Prepend system message
	messages := make([]Message, 0, len(req.Messages)+1)
	messages = append(messages, Message{Role: "system", Content: systemPrompt})
	messages = append(messages, req.Messages...)

	opts := ChatOptions{
		Model: model,
	}

	// Set SSE headers early
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Check if provider supports tools
	toolProvider, hasTools := provider.(ToolProvider)
	if !hasTools {
		// Fall back to non-tool streaming
		h.streamWithoutTools(r, w, flusher, provider, messages, opts)
		return
	}

	// Stream with tool support
	tools := GetToolDefinitions()

	// Update system prompt to mention tool capabilities
	messages[0].Content += "\n\n## Available Tools\n\nYou have access to tools that can query the Kubernetes cluster directly. " +
		"Use them to get accurate, real-time data instead of guessing. " +
		"When a user asks about cluster state, pods, logs, events, or metrics, call the appropriate tool to get the data first, then explain what you found."

	h.streamWithTools(r, w, flusher, toolProvider, messages, tools, opts)
}

// streamWithoutTools handles the simple case (no tool calling)
func (h *Handlers) streamWithoutTools(r *http.Request, w http.ResponseWriter, flusher http.Flusher, provider Provider, messages []Message, opts ChatOptions) {
	stream, err := provider.ChatStream(r.Context(), messages, opts)
	if err != nil {
		log.Printf("[ai] Chat stream error: %v", err)
		sendSSEError(w, flusher, err.Error())
		return
	}

	for chunk := range stream {
		if chunk.Error != "" {
			sendSSEError(w, flusher, chunk.Error)
			return
		}
		if chunk.Done {
			sendSSEDone(w, flusher)
			return
		}
		if chunk.Content != "" {
			sendSSEChunk(w, flusher, chunk.Content)
		}
	}
	sendSSEDone(w, flusher)
}

// streamWithTools handles the tool execution loop
func (h *Handlers) streamWithTools(r *http.Request, w http.ResponseWriter, flusher http.Flusher, provider ToolProvider, messages []Message, tools []ToolDefinition, opts ChatOptions) {
	// First call with tools
	stream, err := provider.ChatStreamWithTools(r.Context(), messages, tools, opts)
	if err != nil {
		log.Printf("[ai] Chat stream error: %v", err)
		sendSSEError(w, flusher, err.Error())
		return
	}

	for round := 0; round < maxToolRounds; round++ {
		// Drain the stream, collecting content and tool calls
		var toolCalls []ToolCall
		for chunk := range stream {
			if chunk.Error != "" {
				sendSSEError(w, flusher, chunk.Error)
				return
			}

			// Stream content tokens directly to the client
			if chunk.Content != "" {
				sendSSEChunk(w, flusher, chunk.Content)
			}

			// Collect tool calls
			if len(chunk.ToolCalls) > 0 {
				toolCalls = append(toolCalls, chunk.ToolCalls...)
			}

			if chunk.Done {
				sendSSEDone(w, flusher)
				return
			}
		}

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			sendSSEDone(w, flusher)
			return
		}

		// Execute tool calls and notify the frontend
		var results []ToolResult
		for _, call := range toolCalls {
			// Send tool_call event to frontend
			sendSSEToolCall(w, flusher, call)

			// Execute the tool
			result := ExecuteTool(r.Context(), call)
			results = append(results, result)

			// Send tool_result event to frontend
			sendSSEToolResult(w, flusher, call, result)
		}

		// Continue the conversation with tool results
		stream, err = provider.ChatStreamWithToolResults(r.Context(), messages, toolCalls, results, tools, opts)
		if err != nil {
			log.Printf("[ai] Tool result stream error: %v", err)
			sendSSEError(w, flusher, err.Error())
			return
		}
	}

	// If we exhausted rounds, send what we have
	sendSSEDone(w, flusher)
}

// SSE helper functions

func sendSSEChunk(w http.ResponseWriter, flusher http.Flusher, content string) {
	data, _ := json.Marshal(map[string]any{
		"content": content,
		"done":    false,
	})
	fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", data)
	flusher.Flush()
}

func sendSSEDone(w http.ResponseWriter, flusher http.Flusher) {
	data, _ := json.Marshal(map[string]any{
		"content": "",
		"done":    true,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
	flusher.Flush()
}

func sendSSEError(w http.ResponseWriter, flusher http.Flusher, msg string) {
	data, _ := json.Marshal(map[string]any{
		"content": "",
		"done":    true,
		"error":   msg,
	})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	flusher.Flush()
}

func sendSSEToolCall(w http.ResponseWriter, flusher http.Flusher, call ToolCall) {
	data, _ := json.Marshal(map[string]any{
		"type":      "tool_call",
		"toolCallId": call.ID,
		"name":      call.Name,
		"arguments": call.Arguments,
	})
	fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", data)
	flusher.Flush()
}

func sendSSEToolResult(w http.ResponseWriter, flusher http.Flusher, call ToolCall, result ToolResult) {
	// Truncate result content for SSE (frontend will show a collapsible section)
	content := result.Content
	if len(content) > 2000 {
		content = content[:2000] + "...(truncated)"
	}
	data, _ := json.Marshal(map[string]any{
		"type":       "tool_result",
		"toolCallId": call.ID,
		"name":       call.Name,
		"content":    content,
		"isError":    result.IsError,
	})
	fmt.Fprintf(w, "event: tool_result\ndata: %s\n\n", data)
	flusher.Flush()
}

// handleListProviders returns available providers
func (h *Handlers) handleListProviders(w http.ResponseWriter, r *http.Request) {
	providers := h.registry.ListProviders()
	activeModel := h.registry.GetActiveModel()

	writeJSON(w, map[string]any{
		"providers":   providers,
		"activeModel": activeModel,
	})
}

// SetProviderRequest is the body for POST /api/ai/provider
type SetProviderRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleSetProvider switches the active provider
func (h *Handlers) handleSetProvider(w http.ResponseWriter, r *http.Request) {
	var req SetProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}

	if err := h.registry.SetActive(req.Provider, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// handleListModels returns models for the active provider
func (h *Handlers) handleListModels(w http.ResponseWriter, r *http.Request) {
	provider, _ := h.registry.GetActive()
	if provider == nil {
		writeJSON(w, map[string]any{"models": []ModelInfo{}})
		return
	}

	writeJSON(w, map[string]any{
		"models":   provider.Models(),
		"provider": provider.Name(),
	})
}

// Helper functions (matching server.go patterns)

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		log.Printf("Failed to encode error response: %v", err)
	}
}
