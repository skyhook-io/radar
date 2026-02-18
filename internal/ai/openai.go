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

// OpenAIProvider implements Provider and ToolProvider for the OpenAI API
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// Verify interface compliance
var _ ToolProvider = (*OpenAIProvider)(nil)

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) Available() bool {
	return p.apiKey != ""
}

func (p *OpenAIProvider) Models() []ModelInfo {
	return []ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o"},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini"},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo"},
		{ID: "gpt-3.5-turbo", Name: "GPT-3.5 Turbo"},
	}
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []Message, opts ChatOptions) (<-chan StreamChunk, error) {
	return p.doStream(ctx, messages, nil, opts)
}

func (p *OpenAIProvider) ChatStreamWithTools(ctx context.Context, messages []Message, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	return p.doStream(ctx, messages, tools, opts)
}

func (p *OpenAIProvider) ChatStreamWithToolResults(ctx context.Context, messages []Message, toolCalls []ToolCall, results []ToolResult, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	// Build the extended message list that includes the assistant's tool calls and tool results
	extended := make([]Message, len(messages))
	copy(extended, messages)

	// Add assistant message with tool calls (OpenAI requires this)
	assistantMsg := Message{Role: "assistant", Content: ""}
	extended = append(extended, assistantMsg)

	// Add tool result messages
	for _, r := range results {
		extended = append(extended, Message{
			Role:       "tool",
			Content:    r.Content,
			ToolCallID: r.ToolCallID,
		})
	}

	return p.doStreamWithToolCallHistory(ctx, extended, toolCalls, tools, opts)
}

func (p *OpenAIProvider) doStream(ctx context.Context, messages []Message, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	model := opts.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	oaiMessages := buildOpenAIMessages(messages)

	body := map[string]any{
		"model":    model,
		"messages": oaiMessages,
		"stream":   true,
	}
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		body["max_tokens"] = opts.MaxTokens
	}
	if len(tools) > 0 {
		body["tools"] = openAIToolDefs(tools)
	}

	return p.sendAndStream(ctx, body)
}

func (p *OpenAIProvider) doStreamWithToolCallHistory(ctx context.Context, messages []Message, toolCalls []ToolCall, tools []ToolDefinition, opts ChatOptions) (<-chan StreamChunk, error) {
	model := opts.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	// Build messages with the assistant tool_calls message properly formatted
	var oaiMessages []map[string]any
	for _, m := range messages {
		if m.Role == "assistant" && m.Content == "" {
			// This is the assistant message with tool calls
			oaiToolCalls := make([]map[string]any, len(toolCalls))
			for i, tc := range toolCalls {
				oaiToolCalls[i] = map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				}
			}
			oaiMessages = append(oaiMessages, map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": oaiToolCalls,
			})
		} else if m.Role == "tool" {
			oaiMessages = append(oaiMessages, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.Content,
			})
		} else {
			oaiMessages = append(oaiMessages, map[string]any{
				"role":    m.Role,
				"content": m.Content,
			})
		}
	}

	body := map[string]any{
		"model":    model,
		"messages": oaiMessages,
		"stream":   true,
	}
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		body["max_tokens"] = opts.MaxTokens
	}
	if len(tools) > 0 {
		body["tools"] = openAIToolDefs(tools)
	}

	return p.sendAndStream(ctx, body)
}

func (p *OpenAIProvider) sendAndStream(ctx context.Context, body map[string]any) (<-chan StreamChunk, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("openai error %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)

		// Accumulate tool calls across streaming chunks
		type pendingToolCall struct {
			ID        string
			Name      string
			Arguments strings.Builder
		}
		var pendingCalls []*pendingToolCall

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// If we have pending tool calls, send them
				if len(pendingCalls) > 0 {
					var calls []ToolCall
					for _, pc := range pendingCalls {
						calls = append(calls, ToolCall{
							ID:        pc.ID,
							Name:      pc.Name,
							Arguments: pc.Arguments.String(),
						})
					}
					select {
					case ch <- StreamChunk{ToolCalls: calls}:
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

			var chunk struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				choice := chunk.Choices[0]

				// Handle tool call deltas
				for _, tc := range choice.Delta.ToolCalls {
					// Grow the pending list if needed
					for tc.Index >= len(pendingCalls) {
						pendingCalls = append(pendingCalls, &pendingToolCall{})
					}
					pc := pendingCalls[tc.Index]
					if tc.ID != "" {
						pc.ID = tc.ID
					}
					if tc.Function.Name != "" {
						pc.Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						pc.Arguments.WriteString(tc.Function.Arguments)
					}
				}

				// Handle content
				content := choice.Delta.Content
				if content != "" {
					select {
					case ch <- StreamChunk{Content: content}:
					case <-ctx.Done():
						return
					}
				}

				// Handle finish_reason = tool_calls
				if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" && len(pendingCalls) > 0 {
					var calls []ToolCall
					for _, pc := range pendingCalls {
						calls = append(calls, ToolCall{
							ID:        pc.ID,
							Name:      pc.Name,
							Arguments: pc.Arguments.String(),
						})
					}
					select {
					case ch <- StreamChunk{ToolCalls: calls}:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()

	return ch, nil
}

func buildOpenAIMessages(messages []Message) []map[string]any {
	oaiMessages := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msg := map[string]any{
			"role":    m.Role,
			"content": m.Content,
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		oaiMessages = append(oaiMessages, msg)
	}
	return oaiMessages
}

func openAIToolDefs(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, len(tools))
	for i, t := range tools {
		result[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		}
	}
	return result
}
