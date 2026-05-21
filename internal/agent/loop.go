package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/genericagent/ga/internal/llm"
)

type StepOutcome struct {
	Data        any
	NextPrompt  string
	ShouldExit  bool
}

type ToolHandler func(toolName string, args map[string]any, response *llm.Response, index int, toolNum int) *StepOutcome

type DisplayItem struct {
	Turn    int
	Content string
	Done    bool
	Source  string
	Outputs []string
}

type Agent struct {
	Client       *llm.Client
	Handler      ToolHandler
	MaxTurns     int
	Verbose      bool
	SystemPrompt string
	ToolsSchema  []llm.ToolSchema

	mu           sync.Mutex
	IsRunning    bool
	StopSignal   bool
	CurrentTurn  int
	Working      map[string]any
}

func New(client *llm.Client, systemPrompt string, toolsSchema []llm.ToolSchema) *Agent {
	return &Agent{
		Client:       client,
		SystemPrompt: systemPrompt,
		ToolsSchema:  toolsSchema,
		MaxTurns:     80,
		Verbose:      true,
		Working:      make(map[string]any),
	}
}

func (a *Agent) Run(userInput string, source string) <-chan DisplayItem {
	ch := make(chan DisplayItem, 128)
	go func() {
		defer close(ch)
		a.mu.Lock()
		a.IsRunning = true
		a.StopSignal = false
		a.mu.Unlock()

		defer func() {
			a.mu.Lock()
			a.IsRunning = false
			a.mu.Unlock()
		}()

		messages := []llm.Message{
			{Role: "system", Content: a.SystemPrompt},
			{Role: "user", Content: userInput},
		}

		var exitReason map[string]any
		turn := 0

		for turn < a.MaxTurns {
			a.mu.Lock()
			stopped := a.StopSignal
			a.mu.Unlock()
			if stopped {
				break
			}

			turn++
			a.CurrentTurn = turn

			var response *llm.Response
			streamCh, err := a.Client.ChatStream(llm.ChatParams{
				Messages:   messages,
				Tools:      a.ToolsSchema,
				MaxTokens:  a.Client.MaxTokens,
				Temperature: a.Client.Temperature,
			})
			if err != nil {
				ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("Error: %v", err), Source: "error"}
				break
			}

			var fullContent string
			var collectedToolCalls []llm.ToolCall
			for chunk := range streamCh {
				if chunk.Error != nil {
					ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("Error: %v", chunk.Error), Source: "error"}
					break
				}
				if chunk.Text != "" {
					fullContent += chunk.Text
				}
				if len(chunk.ToolCalls) > 0 {
					collectedToolCalls = append(collectedToolCalls, chunk.ToolCalls...)
				}
				if chunk.Done {
					break
				}
			}

			response = &llm.Response{
				Content:   fullContent,
				ToolCalls: collectedToolCalls,
			}

			var toolCalls []toolCallInfo
			if len(response.ToolCalls) == 0 {
				exitReason = map[string]any{"result": "CURRENT_TASK_DONE", "data": fullContent}
				ch <- DisplayItem{Turn: turn, Content: fullContent, Source: "final"}
				break
			} else {
				for _, tc := range response.ToolCalls {
					args := parseJSON(tc.Arguments)
					toolCalls = append(toolCalls, toolCallInfo{
					ToolName: tc.Name,
					Args:     args,
					ID:       tc.ID,
				})
				}
			}

			var toolResults []llm.ToolResult
			var nextPrompts []string

			for ii, tc := range toolCalls {
				argsJSON, _ := json.MarshalIndent(tc.Args, "", "  ")
				ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("🛠️ %s\n````text\n%s\n````", tc.ToolName, string(argsJSON)), Source: "tool"}

				outcome := a.Handler(tc.ToolName, tc.Args, response, ii, len(toolCalls))

				if outcome.ShouldExit {
					exitReason = map[string]any{"result": "EXITED", "data": outcome.Data}
					break
				}
				if outcome.NextPrompt == "" {
					exitReason = map[string]any{"result": "CURRENT_TASK_DONE", "data": outcome.Data}
					break
				}

				if outcome.Data != nil {
					dataStr := stringify(outcome.Data)
					toolResults = append(toolResults, llm.ToolResult{
						ToolUseID: tc.ID,
						Content:   dataStr,
					})
				}
				nextPrompts = append(nextPrompts, outcome.NextPrompt)
			}

			if len(nextPrompts) == 0 || exitReason != nil {
				break
			}

			assistantMsg := llm.Message{
				Role:      "assistant",
				Content:   fullContent,
				ToolCalls: response.ToolCalls,
			}
			messages = append(messages, assistantMsg)

			for _, tr := range toolResults {
				toolMsg := llm.Message{
					Role:       "tool",
					ToolCallID: tr.ToolUseID,
					Content:    tr.Content,
				}
				messages = append(messages, toolMsg)
			}

			nextPrompt := strings.Join(nextPrompts, "\n")
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: nextPrompt,
			})
		}

		if exitReason == nil {
			exitReason = map[string]any{"result": "MAX_TURNS_EXCEEDED"}
		}

		doneContent := fmt.Sprintf("\n[Done] %v", exitReason["result"])
		ch <- DisplayItem{Turn: turn, Content: doneContent, Done: true, Source: source}
	}()
	return ch
}

func (a *Agent) Abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.StopSignal = true
}

func (a *Agent) GetRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.IsRunning
}

type toolCallInfo struct {
	ToolName string
	Args     map[string]any
	ID       string
}

func parseJSON(s string) map[string]any {
	var result map[string]any
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return map[string]any{"_raw": s}
	}
	return result
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any, []any:
		data, _ := json.Marshal(val)
		return string(data)
	default:
		return fmt.Sprintf("%v", val)
	}
}
