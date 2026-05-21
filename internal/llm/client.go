package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
}

type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"`
}

type Response struct {
	Content     string
	ToolCalls   []ToolCall
	ContentBlocks []ContentBlock
	StopReason  string
	Usage       Usage
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read"`
	CacheCreate  int `json:"cache_create"`
}

type Message struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolResults []ToolResult  `json:"tool_results,omitempty"`
}

type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ChatParams struct {
	Messages   []Message
	Tools      []ToolSchema
	MaxTokens  int
	Temperature float64
}

type Client struct {
	APIBase      string
	APIKey       string
	Model        string
	APIMode      string
	Name         string
	Stream       bool
	MaxTokens    int
	Temperature  float64
	ContextWin   int
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	MaxRetries     int
	ExtraSysPrompt string

	History    []Message
	LastTools  string
}

type StreamChunk struct {
	Text      string
	ToolCalls []ToolCall
	Done      bool
	Error     error
}

func (c *Client) Chat(params ChatParams) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 64)
	go func() {
		defer close(ch)
		resp, err := c.doChat(params)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		if resp.Content != "" {
			ch <- StreamChunk{Text: resp.Content}
		}
		ch <- StreamChunk{Done: true}
	}()
	return ch, nil
}

func (c *Client) ChatStream(params ChatParams) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 256)
	go func() {
		defer close(ch)
		if err := c.doStreamChat(params, ch); err != nil {
			ch <- StreamChunk{Error: err}
		}
	}()
	return ch, nil
}

func (c *Client) doChat(params ChatParams) (*Response, error) {
	payload := c.buildPayload(params, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data[:min(len(data), 500)]))
	}

	return c.parseJSONResponse(data)
}

func (c *Client) doStreamChat(params ChatParams, ch chan<- StreamChunk) error {
	payload := c.buildPayload(params, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		resp, err := c.doRequest(body, true)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 400 {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if isRetryable(resp.StatusCode) && attempt < c.MaxRetries {
				delay := retryDelay(resp, attempt)
				time.Sleep(delay)
				continue
			}
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data[:min(len(data), 500)]))
		}

		err = c.parseSSEStream(resp.Body, ch)
		resp.Body.Close()
		return err
	}
	return lastErr
}

func (c *Client) buildPayload(params ChatParams, stream bool) map[string]any {
	msgs := make([]any, 0, len(params.Messages))
	for _, m := range params.Messages {
		if m.Role == "tool" {
			msg := map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.Content,
			}
			msgs = append(msgs, msg)
			continue
		}
		msg := map[string]any{"role": m.Role}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = buildAssistantToolCalls(m.ToolCalls)
		}
		if m.Content != nil {
			msg["content"] = m.Content
		}
		msgs = append(msgs, msg)
	}

	payload := map[string]any{
		"model":       c.Model,
		"messages":    msgs,
		"max_tokens":  c.MaxTokens,
		"stream":      stream,
	}
	if c.Temperature > 0 {
		payload["temperature"] = c.Temperature
	}
	if len(params.Tools) > 0 {
		payload["tools"] = buildToolsPayload(params.Tools)
	}
	return payload
}

func buildAssistantToolCalls(tcs []ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(tcs))
	for _, tc := range tcs {
		result = append(result, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			},
		})
	}
	return result
}

func buildToolsPayload(tools []ToolSchema) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		result = append(result, map[string]any{
			"type":        "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		})
	}
	return result
}

func (c *Client) doRequest(body []byte, stream bool) (*http.Response, error) {
	url := autoMakeURL(c.APIBase, "chat/completions")
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	client := &http.Client{
		Timeout: c.ConnectTimeout + c.ReadTimeout,
	}
	return client.Do(req)
}

func (c *Client) parseJSONResponse(data []byte) (*Response, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	resp := &Response{}
	if usage, ok := raw["usage"].(map[string]any); ok {
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			resp.Usage.InputTokens = int(pt)
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			resp.Usage.OutputTokens = int(ct)
		}
	}

	choices, _ := raw["choices"].([]any)
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		msg, _ := choice["message"].(map[string]any)
		if msg != nil {
			if content, ok := msg["content"].(string); ok {
				resp.Content = content
				resp.ContentBlocks = append(resp.ContentBlocks, ContentBlock{Type: "text", Text: content})
			}
			if tcs, ok := msg["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcMap, _ := tc.(map[string]any)
					fn, _ := tcMap["function"].(map[string]any)
					if fn != nil {
						tc := ToolCall{
							ID:        strVal(tcMap["id"]),
							Name:      strVal(fn["name"]),
							Arguments: strVal(fn["arguments"]),
						}
						resp.ToolCalls = append(resp.ToolCalls, tc)
						input := parseJSONArgs(tc.Arguments)
						resp.ContentBlocks = append(resp.ContentBlocks, ContentBlock{
							Type:  "tool_use",
							ID:    tc.ID,
							Name:  tc.Name,
							Input: input,
						})
					}
				}
			}
		}
	}
	return resp, nil
}

func (c *Client) parseSSEStream(body io.ReadCloser, ch chan<- StreamChunk) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	tcBuf := make(map[int]*toolCallBuf)
	var contentText string

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var evt map[string]any
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		choices, _ := evt["choices"].([]any)
		if len(choices) == 0 {
			if usage, ok := evt["usage"].(map[string]any); ok {
				_ = usage
			}
			continue
		}

		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}

		if content, ok := delta["content"].(string); ok && content != "" {
			contentText += content
			ch <- StreamChunk{Text: content}
		}

		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			_ = reasoning
		}

		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range toolCalls {
				tcMap, _ := tc.(map[string]any)
				idx := int(floatVal(tcMap["index"]))
				fn, _ := tcMap["function"].(map[string]any)

				if _, exists := tcBuf[idx]; !exists {
					tcBuf[idx] = &toolCallBuf{}
				}
				buf := tcBuf[idx]

				if fn != nil {
					if name, ok := fn["name"].(string); ok && name != "" {
						buf.name = name
					}
					if args, ok := fn["arguments"].(string); ok {
						buf.args += args
					}
				}
				if id, ok := tcMap["id"].(string); ok && id != "" {
					buf.id = id
				}
			}
		}

		if usage, ok := evt["usage"].(map[string]any); ok {
			_ = usage
		}
	}

	for i := 0; i < len(tcBuf); i++ {
		if buf, ok := tcBuf[i]; ok {
			input := parseJSONArgs(buf.args)
			ch <- StreamChunk{Text: fmt.Sprintf("\n🛠️ Tool: `%s` 📥 args:\n```\n%s\n```\n", buf.name, prettyJSON(input))}
		}
	}

	if len(tcBuf) > 0 {
		var toolCalls []ToolCall
		for i := 0; i < len(tcBuf); i++ {
			if buf, ok := tcBuf[i]; ok {
				toolCalls = append(toolCalls, ToolCall{
					ID:        buf.id,
					Name:      buf.name,
					Arguments: buf.args,
				})
			}
		}
		ch <- StreamChunk{ToolCalls: toolCalls}
	}

	return scanner.Err()
}

type toolCallBuf struct {
	id   string
	name string
	args string
}

func autoMakeURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	p := strings.Trim(path, "/")
	if strings.Contains(b, "/v") {
		return b + "/" + p
	}
	return b + "/v1/" + p
}

func isRetryable(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if d, err := time.ParseDuration(ra + "s"); err == nil {
			return d
		}
	}
	delay := time.Duration(1.5*float64(int(1)<<uint(attempt))) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	if delay < 500*time.Millisecond {
		delay = 500 * time.Millisecond
	}
	return delay
}

func parseJSONArgs(raw string) map[string]any {
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return map[string]any{"_raw": raw}
	}
	return result
}

func prettyJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func strVal(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func floatVal(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

var codeBlockRe = regexp.MustCompile("```\\w*\n[\\s\\S]*?```")

func CleanContent(text string) string {
	text = codeBlockRe.ReplaceAllStringFunc(text, func(m string) string {
		lines := strings.Split(m, "\n")
		body := make([]string, 0, len(lines))
		for _, l := range lines[1 : len(lines)-1] {
			if strings.TrimSpace(l) != "" {
				body = append(body, l)
			}
		}
		if len(body) <= 6 {
			return m
		}
		preview := strings.Join(body[:5], "\n")
		return fmt.Sprintf("```\n%s\n  ... (%d lines)\n```", preview, len(body))
	})
	text = regexp.MustCompile(`<file_content>[\s\S]*?</file_content>`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`<tool_(?:use|call)>[\s\S]*?</tool_(?:use|call)>`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(\r?\n){3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
