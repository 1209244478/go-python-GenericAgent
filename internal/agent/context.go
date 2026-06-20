package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/genericagent/ga/internal/llm"
)

// ContextManager 上下文管理器，负责 token 计数和自动压缩
type ContextManager struct {
	Client     *llm.Client
	MaxTokens  int     // 上下文窗口上限 (来自 ContextWin)
	CompactAt  float64 // 触发压缩的阈值比例，如 0.8
	KeepRecent int     // 压缩时保留最近几轮原文
	Compacted  bool    // 是否已压缩过
}

// NewContextManager 创建上下文管理器
func NewContextManager(client *llm.Client) *ContextManager {
	maxTokens := client.ContextWin
	if maxTokens <= 0 {
		maxTokens = 128000 // 默认假设
	}
	return &ContextManager{
		Client:     client,
		MaxTokens:  maxTokens,
		CompactAt:  0.8,
		KeepRecent: 4, // 保留最近4条消息(约2轮)
	}
}

// EstimateTokens 估算消息列表的 token 数
// 规则: 中文1字≈1.5 token, 英文1词≈1.3 token, 工具调用按 JSON 长度估算
func (cm *ContextManager) EstimateTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += cm.estimateMessageTokens(m)
	}
	// 每条消息额外开销(角色标记等)
	total += len(messages) * 4
	return total
}

// estimateMessageTokens 估算单条消息的 token 数
func (cm *ContextManager) estimateMessageTokens(m llm.Message) int {
	tokens := 0

	// content 部分
	if content, ok := m.Content.(string); ok {
		tokens += estimateStringTokens(content)
	} else if m.Content != nil {
		if data, err := json.Marshal(m.Content); err == nil {
			tokens += estimateStringTokens(string(data))
		}
	}

	// tool_calls 部分
	for _, tc := range m.ToolCalls {
		tokens += estimateStringTokens(tc.Name)
		tokens += estimateStringTokens(tc.Arguments)
		tokens += estimateStringTokens(tc.ID)
	}

	// tool_call_id
	tokens += estimateStringTokens(m.ToolCallID)

	return tokens
}

// estimateStringTokens 估算字符串 token 数
// 中文按字符数*1.5, 英文按词数*1.3, 取较大值
func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	charCount := len([]rune(s))
	// 检测中文字符比例
	chineseCount := 0
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			chineseCount++
		}
	}
	if chineseCount > charCount/3 {
		// 中文为主
		return int(float64(charCount) * 1.5)
	}
	// 英文为主，按空格分词
	wordCount := strings.Fields(s)
	return int(float64(len(wordCount)) * 1.3)
}

// ShouldCompact 判断是否需要压缩
func (cm *ContextManager) ShouldCompact(messages []llm.Message) bool {
	estimated := cm.EstimateTokens(messages)
	threshold := int(float64(cm.MaxTokens) * cm.CompactAt)
	return estimated > threshold
}

// Compact 压缩上下文
// 策略: 保留 system + 最近 KeepRecent 条 + 压缩摘要
func (cm *ContextManager) Compact(messages []llm.Message) ([]llm.Message, error) {
	if len(messages) <= cm.KeepRecent+1 {
		return messages, nil // 消息太少，不压缩
	}

	// 分离 system 消息
	var systemMsgs, otherMsgs []llm.Message
	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}

	// 保留最近 KeepRecent 条
	keepCount := cm.KeepRecent
	if keepCount > len(otherMsgs) {
		keepCount = len(otherMsgs)
	}
	toCompress := otherMsgs[:len(otherMsgs)-keepCount]
	toKeep := otherMsgs[len(otherMsgs)-keepCount:]

	if len(toCompress) == 0 {
		return messages, nil
	}

	// 调用 LLM 压缩
	summary, err := cm.callCompactLLM(toCompress)
	if err != nil {
		// 降级: 硬截断，只保留 toKeep
		return append(systemMsgs, toKeep...), nil
	}

	// 构造压缩后的消息
	summaryMsg := llm.Message{
		Role: "user",
		Content: fmt.Sprintf("<previous_context_summary>\n%s\n</previous_context_summary>\n以上是之前对话的摘要，请基于此继续。", summary),
	}

	result := make([]llm.Message, 0, len(systemMsgs)+1+keepCount)
	result = append(result, systemMsgs...)
	result = append(result, summaryMsg)
	result = append(result, toKeep...)

	cm.Compacted = true
	return result, nil
}

// callCompactLLM 调用 LLM 进行压缩
func (cm *ContextManager) callCompactLLM(messages []llm.Message) (string, error) {
	// 构造压缩请求
	var historyText strings.Builder
	for _, m := range messages {
		historyText.WriteString(fmt.Sprintf("[%s]: ", m.Role))
		if content, ok := m.Content.(string); ok {
			historyText.WriteString(content)
		}
		if len(m.ToolCalls) > 0 {
			historyText.WriteString(" [工具调用: ")
			for i, tc := range m.ToolCalls {
				if i > 0 {
					historyText.WriteString(", ")
				}
				historyText.WriteString(tc.Name)
			}
			historyText.WriteString("]")
		}
		historyText.WriteString("\n")
	}

	prompt := fmt.Sprintf(`请将以下对话历史压缩为简洁摘要，保留:
1. 用户的核心目标和需求
2. 已完成的关键操作和结果
3. 待解决的问题和下一步
4. 关键文件路径、变量名、状态
删除冗余的工具输出细节和重复内容。摘要应控制在500字以内。

对话历史:
%s`, historyText.String())

	// 使用非流式调用
	ch, err := cm.Client.Chat(llm.ChatParams{
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   1024,
		Temperature: 0,
	})
	if err != nil {
		return "", err
	}

	var result string
	for chunk := range ch {
		if chunk.Error != nil {
			return "", chunk.Error
		}
		if chunk.Text != "" {
			result += chunk.Text
		}
	}
	return result, nil
}
