package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicProvider implements Provider and ToolProvider for the Anthropic Messages API
type AnthropicProvider struct {
	apiKey string
	client *http.Client
}

// Verify interface compliance
var _ ToolProvider = (*AnthropicProvider)(nil)

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) Available() bool {
	return p.apiKey != ""
}

func (p *AnthropicProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4"},
		{ID: "claude-3-5-haiku-20241022", Name: "Claude 3.5 Haiku"},
	}
}

func (p *AnthropicProvider) ChatStream(ctx context.Context, messages []Message, opts ChatOptions) (<-chan StreamChunk, error) {
	return p.doStream(ctx, messages, nil, opts)
}

func (p *AnthropicProvider) ChatStreamWithTools(ctx context.Context, messages []Message, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	return p.doStream(ctx, messages, tools, opts)
}

func (p *AnthropicProvider) ChatStreamWithToolResults(ctx context.Context, messages []Message, toolCalls []ToolCall, results []ToolResult, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	// Build extended messages with the assistant tool_use block + user tool_result blocks
	extended := make([]Message, len(messages))
	copy(extended, messages)

	// Anthropic uses content blocks, but we'll encode this via special message format
	// We'll build the raw Anthropic message format in doStreamRaw
	return p.doStreamWithToolResults(ctx, extended, toolCalls, results, tools, opts)
}

func (p *AnthropicProvider) doStream(ctx context.Context, messages []Message, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	model := opts.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Split system message from conversation messages
	var systemContent string
	var anthropicMessages []any
	for _, m := range messages {
		if m.Role == "system" {
			systemContent = m.Content
			continue
		}
		anthropicMessages = append(anthropicMessages, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	body := map[string]any{
		"model":      model,
		"messages":   anthropicMessages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if systemContent != "" {
		body["system"] = systemContent
	}
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}
	if len(tools) > 0 {
		body["tools"] = anthropicToolDefs(tools)
	}

	return p.sendAndStream(ctx, body)
}

func (p *AnthropicProvider) doStreamWithToolResults(ctx context.Context, messages []Message, toolCalls []ToolCall, results []ToolResult, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	model := opts.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Build Anthropic message format with tool_use and tool_result blocks
	var systemContent string
	var anthropicMessages []any
	for _, m := range messages {
		if m.Role == "system" {
			systemContent = m.Content
			continue
		}
		anthropicMessages = append(anthropicMessages, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	// Add assistant message with tool_use content blocks
	var toolUseBlocks []any
	for _, tc := range toolCalls {
		var args any
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			args = map[string]any{}
		}
		toolUseBlocks = append(toolUseBlocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": args,
		})
	}
	anthropicMessages = append(anthropicMessages, map[string]any{
		"role":    "assistant",
		"content": toolUseBlocks,
	})

	// Add user message with tool_result content blocks
	var toolResultBlocks []any
	for _, r := range results {
		block := map[string]any{
			"type":       "tool_result",
			"tool_use_id": r.ToolCallID,
			"content":    r.Content,
		}
		if r.IsError {
			block["is_error"] = true
		}
		toolResultBlocks = append(toolResultBlocks, block)
	}
	anthropicMessages = append(anthropicMessages, map[string]any{
		"role":    "user",
		"content": toolResultBlocks,
	})

	body := map[string]any{
		"model":      model,
		"messages":   anthropicMessages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if systemContent != "" {
		body["system"] = systemContent
	}
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}
	if len(tools) > 0 {
		body["tools"] = anthropicToolDefs(tools)
	}

	return p.sendAndStream(ctx, body)
}

func (p *AnthropicProvider) sendAndStream(ctx context.Context, body map[string]any) (<-chan StreamChunk, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic error %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)

		// Track pending tool use blocks
		type pendingToolUse struct {
			ID   string
			Name string
			Args strings.Builder
		}
		var currentToolUse *pendingToolUse
		var completedCalls []ToolCall

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var event struct {
				Type         string `json:"type"`
				Index        int    `json:"index"`
				ContentBlock struct {
					Type  string `json:"type"`
					ID    string `json:"id"`
					Name  string `json:"name"`
					Text  string `json:"text"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "content_block_start":
				if event.ContentBlock.Type == "tool_use" {
					currentToolUse = &pendingToolUse{
						ID:   event.ContentBlock.ID,
						Name: event.ContentBlock.Name,
					}
				}

			case "content_block_delta":
				if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
					select {
					case ch <- StreamChunk{Content: event.Delta.Text}:
					case <-ctx.Done():
						return
					}
				} else if event.Delta.Type == "input_json_delta" && currentToolUse != nil {
					currentToolUse.Args.WriteString(event.Delta.PartialJSON)
				}

			case "content_block_stop":
				if currentToolUse != nil {
					args := currentToolUse.Args.String()
					if args == "" {
						args = "{}"
					}
					completedCalls = append(completedCalls, ToolCall{
						ID:        currentToolUse.ID,
						Name:      currentToolUse.Name,
						Arguments: args,
					})
					currentToolUse = nil
				}

			case "message_delta":
				if event.Delta.StopReason == "end_turn" || event.Delta.StopReason == "stop_sequence" {
					if len(completedCalls) > 0 {
						// Tool calls were made alongside text, send them
						select {
						case ch <- StreamChunk{ToolCalls: completedCalls}:
						case <-ctx.Done():
						}
						return
					}
					select {
					case ch <- StreamChunk{Done: true}:
					case <-ctx.Done():
					}
					return
				}
				if event.Delta.StopReason == "tool_use" {
					if len(completedCalls) > 0 {
						select {
						case ch <- StreamChunk{ToolCalls: completedCalls}:
						case <-ctx.Done():
						}
						return
					}
				}

			case "message_stop":
				if len(completedCalls) > 0 {
					select {
					case ch <- StreamChunk{ToolCalls: completedCalls}:
					case <-ctx.Done():
					}
					return
				}
				select {
				case ch <- StreamChunk{Done: true}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return ch, nil
}

func anthropicToolDefs(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, len(tools))
	for i, t := range tools {
		result[i] = map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		}
	}
	return result
}
