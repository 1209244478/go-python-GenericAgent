package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/genericagent/ga/internal/llm"
)

type StepOutcome struct {
	Data        any
	NextPrompt  string
	ShouldExit  bool
	PlanSubmit  string // 计划模式: 提交计划内容 (非空则触发审批流程)
}

type ToolHandler func(toolName string, args map[string]any, response *llm.Response, index int, toolNum int) *StepOutcome

type DisplayItem struct {
	Turn       int
	Content    string
	Done       bool
	Source     string
	Outputs    []string
	ToolCalls  []llm.ToolCall
	ToolCallID string
}

type Agent struct {
	Client       *llm.Client
	Handler      ToolHandler
	MaxTurns     int
	Verbose      bool
	SystemPrompt string
	ToolsSchema  []llm.ToolSchema

	// LLM 参数 (用于 cache safe params 对齐)
	Model        string
	Temperature  float64
	MaxTokens    int

	// 长任务能力
	ContextMgr   *ContextManager
	Goal         string // 目标追踪 (兼容旧字段)
	GoalTracker  *GoalTracker // 目标追踪状态机 (新)
	PlanMode     bool   // 计划模式
	TaskID       string // 关联的 Task ID
	Ctx          context.Context
	CwdOverride  string // 工作目录覆盖 (worktree 隔离)
	MaxDuration  time.Duration // 整体执行超时 (0=无限), 参考 cc-haha REMOTE_REVIEW_TIMEOUT_MS

	mu           sync.Mutex
	IsRunning    bool
	StopSignal   bool
	CurrentTurn  int
	Working      map[string]any

	// 计划审批通道(由 Runtime 注入)
	PlanApprovalCh chan bool

	// teammate 通信: 待注入的消息队列
	injectMu      sync.Mutex
	injectedMsgs  []string
	planApproved  chan struct{} // 计划审批通过信号

	// 最终消息历史 (loop 结束后供 Runtime 持久化)
	finalMessages []llm.Message
}

func New(client *llm.Client, systemPrompt string, toolsSchema []llm.ToolSchema) *Agent {
	return &Agent{
		Client:       client,
		SystemPrompt: systemPrompt,
		ToolsSchema:  toolsSchema,
		MaxTurns:     80,
		Verbose:      true,
		Working:      make(map[string]any),
		ContextMgr:   NewContextManager(client),
	}
}

func (a *Agent) Run(userInput string, source string, history []llm.Message) <-chan DisplayItem {
	ch := make(chan DisplayItem, 128)
	go func() {
		defer close(ch)
		a.mu.Lock()
		a.IsRunning = true
		a.StopSignal = false
		a.mu.Unlock()

		// 整体执行超时计时
		startTime := time.Now()

		defer func() {
			a.mu.Lock()
			a.IsRunning = false
			a.mu.Unlock()
		}()

		messages := []llm.Message{
			{Role: "system", Content: a.SystemPrompt},
		}
		// Append history messages (skip system prompt from history)
		for _, m := range history {
			if m.Role != "system" {
				messages = append(messages, m)
			}
		}
		messages = append(messages, llm.Message{Role: "user", Content: userInput})

		var exitReason map[string]any
		turn := 0
		var fullResponseText string

		for turn < a.MaxTurns {
		a.mu.Lock()
		stopped := a.StopSignal
		a.mu.Unlock()
		if stopped {
			break
		}

		// 检查 context 取消
		if a.Ctx != nil {
			select {
			case <-a.Ctx.Done():
				exitReason = map[string]any{"result": "ABORTED", "data": "context cancelled"}
			default:
			}
			if exitReason != nil {
				break
			}
		}

		// 检查整体执行超时 (MaxDuration)
		// 参考 cc-haha REMOTE_REVIEW_TIMEOUT_MS = 30min
		if a.MaxDuration > 0 && time.Since(startTime) > a.MaxDuration {
			exitReason = map[string]any{"result": "TIMEOUT", "data": fmt.Sprintf("exceeded max duration %v", a.MaxDuration)}
			ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("⏰ 整体执行超时 (%v)，自动终止", a.MaxDuration), Source: "timeout"}
			break
		}

		turn++
		a.CurrentTurn = turn

		// 上下文压缩检查
		if a.ContextMgr != nil && a.ContextMgr.ShouldCompact(messages) {
			ch <- DisplayItem{Turn: turn, Content: "📦 上下文已超过阈值，正在压缩...", Source: "compact"}
			newMsgs, err := a.ContextMgr.Compact(messages)
			if err == nil {
				messages = newMsgs
				ch <- DisplayItem{Turn: turn, Content: "✅ 上下文压缩完成", Source: "compact"}
			} else {
				ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("⚠️ 压缩失败: %v，降级为硬截断", err), Source: "compact"}
			}
		}

		// 目标追踪: 使用 GoalTracker 状态机
		// 参考 cc-haha goalState.ts: active/paused/done/failed 状态机
		if a.GoalTracker != nil {
			if shouldRemind, msg := a.GoalTracker.ShouldRemind(turn); shouldRemind {
				messages = append(messages, llm.Message{Role: "user", Content: msg})
				a.GoalTracker.MarkReminded(turn)
			}
		} else if a.Goal != "" && turn > 1 && turn%5 == 0 {
			// 兼容旧字段
			reminder := fmt.Sprintf("<goal_reminder>当前目标: %s。请确保所有操作都服务于该目标。</goal_reminder>", a.Goal)
			messages = append(messages, llm.Message{Role: "user", Content: reminder})
		}

		// 消费 teammate 注入的消息
		if injected := a.drainInjectedMessages(); len(injected) > 0 {
			for _, msg := range injected {
				messages = append(messages, llm.Message{Role: "user", Content: msg})
			}
			ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("📨 收到 %d 条来自其他 agent 的消息", len(injected)), Source: "teammate"}
		}

		var response *llm.Response
		var streamCh <-chan llm.StreamChunk
		var streamErr error
		for retry := 0; retry < 3; retry++ {
			streamCh, streamErr = a.Client.ChatStream(llm.ChatParams{
				Messages:    messages,
				Tools:       a.ToolsSchema,
				MaxTokens:   a.Client.MaxTokens,
				Temperature: a.Client.Temperature,
			})
			if streamErr == nil {
				break
			}
			if retry < 2 {
				ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("⚠️ LLM 连接失败 (%v)，%d秒后重试...", streamErr, retry+1), Source: "retry"}
				time.Sleep(time.Duration(retry+1) * time.Second)
			}
		}
		if streamErr != nil {
			ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("Error: %v", streamErr), Source: "error"}
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
				if len(chunk.ToolCalls) == 0 {
					ch <- DisplayItem{Turn: turn, Content: chunk.Text, Source: "final"}
				}
			}
			if len(chunk.ToolCalls) > 0 {
				collectedToolCalls = append(collectedToolCalls, chunk.ToolCalls...)
			}
			// 记录真实 usage, 自校准 token 估算
			if chunk.Usage != nil && chunk.Usage.InputTokens > 0 {
				if a.ContextMgr != nil {
					a.ContextMgr.RecordRealUsage(chunk.Usage.InputTokens, messages)
				}
			}
			if chunk.Done {
				break
			}
		}

		fullResponseText += fullContent

		response = &llm.Response{
			Content:   fullContent,
			ToolCalls: collectedToolCalls,
		}

		var toolCalls []toolCallInfo
		if len(response.ToolCalls) == 0 {
			if fullContent == "" {
				ch <- DisplayItem{Turn: turn, Content: "", Source: "final"}
			}
			exitReason = map[string]any{"result": "CURRENT_TASK_DONE", "data": fullContent}
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

		// Send assistant message with tool_calls for history
		ch <- DisplayItem{Turn: turn, Content: fullContent, Source: "assistant", ToolCalls: response.ToolCalls}

		for ii, tc := range toolCalls {
			argsJSON, _ := json.MarshalIndent(tc.Args, "", "  ")
			ch <- DisplayItem{Turn: turn, Content: fmt.Sprintf("🛠️ %s\n````text\n%s\n````", tc.ToolName, string(argsJSON)), Source: "tool", ToolCallID: tc.ID}

			outcome := a.Handler(tc.ToolName, tc.Args, response, ii, len(toolCalls))

			// 计划模式: 提交计划等待审批
			// 参考 cc-haha ExitPlanModeTool: 暂停执行 -> 用户审批 -> 继续/终止
			if outcome.PlanSubmit != "" {
				ch <- DisplayItem{
					Turn:    turn,
					Content: outcome.PlanSubmit,
					Source:  "plan_submit",
				}

				// 初始化审批通道 (如果尚未初始化)
				if a.planApproved == nil {
					a.planApproved = make(chan struct{}, 1)
				}

				// 阻塞等待审批
				approved := a.waitForPlanApproval()
				if !approved {
					exitReason = map[string]any{"result": "PLAN_REJECTED", "data": outcome.PlanSubmit}
					break
				}

				// 审批通过, 注入批准消息并继续
				toolResults = append(toolResults, llm.ToolResult{
					ToolUseID: tc.ID,
					Content:   "[计划已批准，请继续执行]",
				})
				ch <- DisplayItem{Turn: turn, Content: "[计划已批准]", Source: "tool_result", ToolCallID: tc.ID}
				nextPrompts = append(nextPrompts, "计划已批准，请按照计划继续执行。")
				continue
			}

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
				// Send tool result for history
				ch <- DisplayItem{Turn: turn, Content: dataStr, Source: "tool_result", ToolCallID: tc.ID}
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

		// 保存最终消息历史 (供 Runtime 持久化和子 agent 恢复)
		a.mu.Lock()
		a.finalMessages = messages
		a.mu.Unlock()

		doneContent := fmt.Sprintf("\n[Done] %v", exitReason["result"])
		ch <- DisplayItem{Turn: turn, Content: fullResponseText, Done: true, Source: source, Outputs: []string{doneContent}}
	}()
	return ch
}

// GetFinalMessages 获取最终消息历史 (loop 结束后调用)
func (a *Agent) GetFinalMessages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finalMessages
}

func (a *Agent) Abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.StopSignal = true
}

// Stop 停止 agent (Abort 的语义化别名, 供 shutdown 协议调用)
func (a *Agent) Stop() {
	a.Abort()
}

// InjectMessage 注入外部消息 (teammate 通信)
// 消息会在下一轮循环开始前作为 user 消息加入上下文
func (a *Agent) InjectMessage(content string) {
	a.injectMu.Lock()
	defer a.injectMu.Unlock()
	a.injectedMsgs = append(a.injectedMsgs, content)
}

// drainInjectedMessages 取出并清空待注入消息
func (a *Agent) drainInjectedMessages() []string {
	a.injectMu.Lock()
	defer a.injectMu.Unlock()
	if len(a.injectedMsgs) == 0 {
		return nil
	}
	msgs := a.injectedMsgs
	a.injectedMsgs = nil
	return msgs
}

// ApprovePlan 通知 agent 计划已批准, 继续执行
func (a *Agent) ApprovePlan() {
	if a.planApproved != nil {
		select {
		case a.planApproved <- struct{}{}:
		default:
		}
	}
}

// waitForPlanApproval 等待计划审批 (阻塞)
// 在 plan mode 下调用 exit_plan_mode 后触发
func (a *Agent) waitForPlanApproval() bool {
	if a.planApproved == nil {
		return true // 无审批通道, 直接通过
	}
	_, ok := <-a.planApproved
	return ok
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
