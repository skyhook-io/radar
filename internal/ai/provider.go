package ai

import (
	"context"
	"fmt"
	"sync"
)

// Message represents a chat message
type Message struct {
	Role       string `json:"role"`                 // "user", "assistant", "system", "tool"
	Content    string `json:"content"`              // Message text
	ToolCallID string `json:"toolCallId,omitempty"` // For tool result messages
}

// ChatOptions configures a chat request
type ChatOptions struct {
	Model       string            `json:"model,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"maxTokens,omitempty"`
	Context     *ResourceContext   `json:"context,omitempty"` // K8s context to include
}

// ResourceContext represents K8s resource context for AI
type ResourceContext struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Group     string `json:"group,omitempty"`
}

// StreamChunk is a piece of a streaming response
type StreamChunk struct {
	Content   string     `json:"content"`
	Done      bool       `json:"done"`
	Error     string     `json:"error,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"` // Tool calls requested by the LLM
}

// ToolDefinition describes a tool the LLM can call
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ToolCall represents a tool invocation requested by the LLM
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string of arguments
}

// ToolResult is the result of executing a tool call
type ToolResult struct {
	ToolCallID string `json:"toolCallId"`
	Content    string `json:"content"` // JSON result
	IsError    bool   `json:"isError,omitempty"`
}

// Usage tracks token usage
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
}

// ModelInfo describes an available model
type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Provider is the interface for LLM backends
type Provider interface {
	// ChatStream sends messages and returns a channel of streaming chunks
	ChatStream(ctx context.Context, messages []Message, opts ChatOptions) (<-chan StreamChunk, error)
	// Name returns the provider name
	Name() string
	// Available returns true if the provider is configured and ready
	Available() bool
	// Models returns available models for this provider
	Models() []ModelInfo
}

// ToolProvider is an optional interface for providers that support tool/function calling.
// Providers that implement this will have tools passed to the LLM and can handle
// multi-turn tool execution loops.
type ToolProvider interface {
	Provider
	// ChatStreamWithTools sends messages with tool definitions and returns streaming chunks.
	// The stream may include chunks with ToolCalls populated instead of Content.
	ChatStreamWithTools(ctx context.Context, messages []Message, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error)
	// ChatStreamWithToolResults continues a conversation after tool execution.
	// prevMessages includes the full history, toolCalls are the calls that were made,
	// and results are the executed results.
	ChatStreamWithToolResults(ctx context.Context, messages []Message, toolCalls []ToolCall, results []ToolResult, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error)
}

// Registry manages available AI providers
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	active    string
	model     string
}

// NewRegistry creates a new provider registry
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// SetActive sets the active provider and model
func (r *Registry) SetActive(providerName, model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.providers[providerName]
	if !ok {
		return fmt.Errorf("unknown provider: %s", providerName)
	}
	if !p.Available() {
		return fmt.Errorf("provider %s is not available (missing API key?)", providerName)
	}
	r.active = providerName
	r.model = model
	return nil
}

// GetActive returns the active provider and model
func (r *Registry) GetActive() (Provider, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.active == "" {
		return nil, ""
	}
	return r.providers[r.active], r.model
}

// GetActiveProviderName returns just the name
func (r *Registry) GetActiveProviderName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// GetActiveModel returns the active model name
func (r *Registry) GetActiveModel() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.model
}

// ListProviders returns info about all registered providers
func (r *Registry) ListProviders() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ProviderInfo, 0, len(r.providers))
	for name, p := range r.providers {
		result = append(result, ProviderInfo{
			Name:      name,
			Available: p.Available(),
			Active:    name == r.active,
			Models:    p.Models(),
		})
	}
	return result
}

// ProviderInfo describes a provider's status
type ProviderInfo struct {
	Name      string      `json:"name"`
	Available bool        `json:"available"`
	Active    bool        `json:"active"`
	Models    []ModelInfo `json:"models"`
}
