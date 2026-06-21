package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/genericagent/ga/internal/llm"
)

// RecoveryManager 中断恢复管理器
// 参考 cc-haha:
//   - resumeAgent.ts: 恢复中断的 agent
//   - reconstructForSubagentResume: 子 agent 恢复重建
//   - filterOrphanedThinkingOnlyMessages: 过滤孤儿 thinking 消息
//   - ContentReplacementState: 工具结果内容替换
type RecoveryManager struct {
	store *Store
}

// NewRecoveryManager 创建恢复管理器
func NewRecoveryManager(store *Store) *RecoveryManager {
	return &RecoveryManager{store: store}
}

// FilterOrphanedMessages 过滤孤儿消息
// 参考 cc-haha filterOrphanedThinkingOnlyMessages
// 恢复时清理无效消息:
//  1. 只有 thinking 内容的 assistant 消息 (无 text/tool_calls)
//  2. 未解析的 tool_use (无对应 tool_result)
//  3. 空白 assistant 消息
func FilterOrphanedMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return messages
	}

	// 收集所有 tool_use_id
	toolUseIDs := make(map[string]bool)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			toolUseIDs[tc.ID] = true
		}
	}

	// 收集所有 tool_result 的 tool_call_id
	toolResultIDs := make(map[string]bool)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResultIDs[m.ToolCallID] = true
		}
	}

	result := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		// 过滤空白 assistant 消息
		if m.Role == "assistant" {
			content, _ := m.Content.(string)
			if content == "" && len(m.ToolCalls) == 0 {
				continue
			}
		}

		// 过滤未匹配的 tool_result (无对应 tool_use)
		if m.Role == "tool" && m.ToolCallID != "" {
			if !toolUseIDs[m.ToolCallID] {
				continue
			}
		}

		// 过滤未匹配的 tool_use (无对应 tool_result, 且非最后一条)
		// 注意: 最后一条 assistant 可能包含未执行的 tool_use, 保留
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			allUnmatched := true
			for _, tc := range m.ToolCalls {
				if toolResultIDs[tc.ID] {
					allUnmatched = false
					break
				}
			}
			if allUnmatched {
				// 检查是否是最后一条 assistant
				isLast := false
				for i := len(messages) - 1; i >= 0; i-- {
					if messages[i].Role == "assistant" {
						if &messages[i] == &m {
							isLast = true
						}
						break
					}
				}
				if !isLast {
					continue // 跳过中间未匹配的 tool_use
				}
			}
		}

		result = append(result, m)
	}

	return result
}

// ContentReplacement 工具结果内容替换
// 参考 cc-haha ContentReplacementState
// 用途: 将长工具结果替换为简短摘要, 减少 token 占用
type ContentReplacement struct {
	ToolCallID string `json:"tool_call_id"` // 原始 tool_call_id
	Original   string `json:"original"`     // 原始内容 (用于恢复)
	Summary    string `json:"summary"`      // 替换后的摘要
}

// ContentReplacementState 内容替换状态
type ContentReplacementState struct {
	replacements map[string]*ContentReplacement // toolCallID -> replacement
}

// NewContentReplacementState 创建内容替换状态
func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{
		replacements: make(map[string]*ContentReplacement),
	}
}

// Replace 替换工具结果内容
// 如果原始内容超过 maxTokens, 替换为摘要
func (crs *ContentReplacementState) Replace(messages []llm.Message, toolCallID, summary string) {
	crs.replacements[toolCallID] = &ContentReplacement{
		ToolCallID: toolCallID,
		Summary:    summary,
	}
}

// Apply 应用内容替换到消息列表
// 返回替换后的消息列表 (原始消息不变)
func (crs *ContentReplacementState) Apply(messages []llm.Message) []llm.Message {
	if len(crs.replacements) == 0 {
		return messages
	}

	result := make([]llm.Message, len(messages))
	copy(result, messages)

	for i, m := range result {
		if m.Role != "tool" || m.ToolCallID == "" {
			continue
		}
		if rep, ok := crs.replacements[m.ToolCallID]; ok {
			original, _ := m.Content.(string)
			rep.Original = original
			result[i].Content = fmt.Sprintf("[content_replaced] %s", rep.Summary)
		}
	}
	return result
}

// Restore 恢复原始内容
func (crs *ContentReplacementState) Restore(messages []llm.Message) []llm.Message {
	if len(crs.replacements) == 0 {
		return messages
	}

	result := make([]llm.Message, len(messages))
	copy(result, messages)

	for i, m := range result {
		if m.Role != "tool" || m.ToolCallID == "" {
			continue
		}
		if rep, ok := crs.replacements[m.ToolCallID]; ok && rep.Original != "" {
			result[i].Content = rep.Original
		}
	}
	return result
}

// ReconstructForResume 为恢复重建消息历史
// 参考 cc-haha reconstructForSubagentResume
// 步骤:
//  1. 加载持久化的消息历史
//  2. 过滤孤儿消息
//  3. 应用 content replacement (如果启用)
//  4. 返回重建后的消息
func (rm *RecoveryManager) ReconstructForResume(userID int64, taskID string, applyReplacement bool) ([]llm.Message, error) {
	// 加载消息历史
	messages, err := rm.store.LoadMessages(userID, taskID)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}

	// 过滤孤儿消息
	messages = FilterOrphanedMessages(messages)

	// 应用 content replacement (从磁盘加载替换状态)
	if applyReplacement {
		repState := rm.loadReplacementState(userID, taskID)
		if repState != nil {
			messages = repState.Apply(messages)
		}
	}

	return messages, nil
}

// SaveReplacementState 保存内容替换状态到磁盘
func (rm *RecoveryManager) SaveReplacementState(userID int64, taskID string, state *ContentReplacementState) error {
	if state == nil || len(state.replacements) == 0 {
		return nil
	}

	dir := rm.store.taskDir(userID, taskID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "replacements.json")
	data, err := json.Marshal(state.replacements)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// loadReplacementState 从磁盘加载内容替换状态
func (rm *RecoveryManager) loadReplacementState(userID int64, taskID string) *ContentReplacementState {
	path := filepath.Join(rm.store.taskDir(userID, taskID), "replacements.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var replacements map[string]*ContentReplacement
	if err := json.Unmarshal(data, &replacements); err != nil {
		return nil
	}

	return &ContentReplacementState{replacements: replacements}
}

// ValidateRecovery 验证恢复的消息历史是否有效
// 检查: 消息顺序、角色配对、tool_use/tool_result 匹配
func ValidateRecovery(messages []llm.Message) error {
	if len(messages) == 0 {
		return nil
	}

	// 检查第一条必须是 system 或 user
	if messages[0].Role != "system" && messages[0].Role != "user" {
		return fmt.Errorf("first message must be system or user, got %s", messages[0].Role)
	}

	// 检查 tool_use/tool_result 配对
	toolUseIDs := make(map[string]bool)
	toolResultIDs := make(map[string]bool)

	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			toolUseIDs[tc.ID] = true
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResultIDs[m.ToolCallID] = true
		}
	}

	// 每个 tool_result 必须有对应 tool_use
	for id := range toolResultIDs {
		if !toolUseIDs[id] {
			return fmt.Errorf("tool_result %s has no matching tool_use", id)
		}
	}

	return nil
}

// FormatTranscriptSummary 格式化 transcript 摘要
// 供主 agent 重放子 agent 结果时使用
func FormatTranscriptSummary(state *TaskState, messages []llm.Message) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## 子任务摘要: %s\n", state.Description))
	sb.WriteString(fmt.Sprintf("- 状态: %s\n", state.Status))
	sb.WriteString(fmt.Sprintf("- 轮次: %d\n", state.TurnCount))
	sb.WriteString(fmt.Sprintf("- Token: %d\n", state.TokenUsage.TotalTokens))

	if state.Error != "" {
		sb.WriteString(fmt.Sprintf("- 错误: %s\n", state.Error))
	}

	// 提取最后几条 assistant 消息作为结果摘要
	var lastAssistant []string
	for i := len(messages) - 1; i >= 0 && len(lastAssistant) < 3; i-- {
		if messages[i].Role == "assistant" {
			if content, ok := messages[i].Content.(string); ok && content != "" {
				truncated := content
				if len([]rune(truncated)) > 500 {
					truncated = string([]rune(truncated)[:500]) + "..."
				}
				lastAssistant = append([]string{truncated}, lastAssistant...)
			}
		}
	}

	if len(lastAssistant) > 0 {
		sb.WriteString("\n### 最后输出\n")
		for i, s := range lastAssistant {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
		}
	}

	return sb.String()
}
