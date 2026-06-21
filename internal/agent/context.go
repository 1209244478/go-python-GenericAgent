package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/genericagent/ga/internal/llm"
)

// 预编译正则
var (
	reWinPath   = regexp.MustCompile(`[a-zA-Z]:[\\\/][^\s"',<>|:*?]+`)
	reUnixPath  = regexp.MustCompile(`/[a-zA-Z][^\s"',<>|:*?]+`)
	reCodeFile  = regexp.MustCompile(`[\w\-./]+\.(go|py|js|ts|json|md)`)
)

// regexFindAll 简单封装正则匹配
func regexFindAll(text, pattern string) []string {
	var re *regexp.Regexp
	switch pattern {
	case `[a-zA-Z]:[\\\/][^\s"',<>|:*?]+`:
		re = reWinPath
	case `/[a-zA-Z][^\s"',<>|:*?]+`:
		re = reUnixPath
	case `[\w\-./]+\.(go|py|js|ts|json|md)`:
		re = reCodeFile
	default:
		re = regexp.MustCompile(pattern)
	}
	return re.FindAllString(text, -1)
}

// ContextManager 上下文管理器，负责 token 计数和自动压缩
//
// 设计参考 cc-haha 的 autoCompact + compactConversation:
//   - 多级降级: proactive(80%) -> reactive(API 413) -> hard truncate
//   - 递归守卫: 防止 compact/session_memory 源再次触发 compact 死循环
//   - 精确估算: 中英文混合 + 工具调用 JSON + 图片附件
//   - microcompact: 工具结果裁剪 (超过阈值时截断长 tool 结果)
//   - session memory: 优先尝试会话记忆压缩 (提取关键信息)
//   - 分级警告: warning(70%) / error(85%) / hard(95%)
//   - 连续失败保护: MAX_CONSECUTIVE_COMPACT_FAILURES=3 后直接硬截断
type ContextManager struct {
	Client     *llm.Client
	MaxTokens  int     // 上下文窗口上限 (来自 ContextWin)
	CompactAt  float64 // 主动触发压缩的阈值比例 (默认 0.8)
	HardLimit  float64 // 硬上限比例 (默认 0.95), 超过则强制硬截断
	KeepRecent int     // 压缩时保留最近几轮原文
	Compacted  bool    // 是否已压缩过

	// 分级警告阈值 (参考 cc-haha WARNING_THRESHOLD/ERROR_THRESHOLD)
	WarningThreshold float64 // 警告阈值 (默认 0.7)
	ErrorThreshold   float64 // 错误阈值 (默认 0.85)

	// microcompact 配置
	MicrocompactToolResult int // 单个工具结果超过此 token 数则裁剪 (默认 4000)

	// 递归守卫: 防止 compact 源的 LLM 调用再次触发 compact
	// 参考 cc-haha autoCompact.ts:165 对 session_memory/compact 源的守卫
	recursionGuard atomic.Bool

	// snipTokensFreed: 已通过 snip 释放的 token 数 (消息已删除但 usage 仍反映旧值)
	// 参考 cc-haha autoCompact.ts:166 snipTokensFreed 参数
	snipTokensFreed atomic.Int64

	// 压缩历史, 用于失败时降级
	mu              sync.Mutex
	lastCompactErr  error
	compactAttempts int
	// 连续失败计数 (参考 cc-haha MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES=3)
	consecutiveFailures int
}

// 连续失败上限
const maxConsecutiveCompactFailures = 3

// NewContextManager 创建上下文管理器
func NewContextManager(client *llm.Client) *ContextManager {
	maxTokens := client.ContextWin
	if maxTokens <= 0 {
		maxTokens = 128000 // 默认假设
	}
	return &ContextManager{
		Client:                 client,
		MaxTokens:              maxTokens,
		CompactAt:              0.8,
		HardLimit:              0.95,
		WarningThreshold:       0.7,
		ErrorThreshold:         0.85,
		MicrocompactToolResult: 4000,
		KeepRecent:             4, // 保留最近4条消息(约2轮)
	}
}

// EstimateTokens 估算消息列表的 token 数
// 规则: 中文1字≈1.5 token, 英文1词≈1.3 token, 工具调用按 JSON 长度估算
// 每条消息额外 4 token 开销 (角色标记等)
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

	// tool_calls 部分 (含完整 arguments JSON)
	for _, tc := range m.ToolCalls {
		tokens += estimateStringTokens(tc.Name)
		tokens += estimateStringTokens(tc.Arguments)
		tokens += estimateStringTokens(tc.ID)
		// 工具调用结构开销
		tokens += 8
	}

	// tool_call_id
	tokens += estimateStringTokens(m.ToolCallID)

	// role 标记开销
	tokens += 4

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

// ShouldCompact 判断是否需要主动压缩 (proactive)
// 参考 cc-haha autoCompact.ts:160 shouldAutoCompact
func (cm *ContextManager) ShouldCompact(messages []llm.Message) bool {
	// 递归守卫: compact 自身的 LLM 调用不应再触发 compact
	if cm.recursionGuard.Load() {
		return false
	}

	estimated := cm.EstimateTokens(messages)
	// 减去已通过 snip 释放的 token (usage 仍反映旧值)
	estimated -= int(cm.snipTokensFreed.Load())
	if estimated < 0 {
		estimated = 0
	}
	threshold := int(float64(cm.MaxTokens) * cm.CompactAt)
	return estimated > threshold
}

// IsOverHardLimit 判断是否超过硬上限, 需要强制硬截断
func (cm *ContextManager) IsOverHardLimit(messages []llm.Message) bool {
	estimated := cm.EstimateTokens(messages)
	hardLimit := int(float64(cm.MaxTokens) * cm.HardLimit)
	return estimated > hardLimit
}

// GetWarningLevel 获取当前警告级别
// 返回: "ok" / "warning" / "error" / "hard"
// 参考 cc-haha contextBudget.ts: WARNING_THRESHOLD / ERROR_THRESHOLD
func (cm *ContextManager) GetWarningLevel(messages []llm.Message) string {
	estimated := cm.EstimateTokens(messages)
	ratio := float64(estimated) / float64(cm.MaxTokens)
	if ratio >= cm.HardLimit {
		return "hard"
	}
	if ratio >= cm.ErrorThreshold {
		return "error"
	}
	if ratio >= cm.WarningThreshold {
		return "warning"
	}
	return "ok"
}

// Microcompact 微压缩: 裁剪过长的工具结果
// 参考 cc-haha microcompactMessages: 在完整 compact 前先尝试裁剪 tool 结果
// 不调用 LLM, 仅本地截断超长 tool 结果, 保留首尾
func (cm *ContextManager) Microcompact(messages []llm.Message) []llm.Message {
	if cm.MicrocompactToolResult <= 0 {
		return messages
	}

	changed := false
	result := make([]llm.Message, len(messages))
	copy(result, messages)

	for i, m := range result {
		if m.Role != "tool" {
			continue
		}
		content, ok := m.Content.(string)
		if !ok || content == "" {
			continue
		}
		tokens := estimateStringTokens(content)
		if tokens <= cm.MicrocompactToolResult {
			continue
		}
		// 截断: 保留前 40% + 后 40% + 中间省略
		runes := []rune(content)
		keep := cm.MicrocompactToolResult * 2 / 3 // 估算字符数
		head := keep * 2 / 5
		tail := keep * 3 / 5
		if head+tail >= len(runes) {
			continue
		}
		truncated := string(runes[:head]) +
			fmt.Sprintf("\n...[truncated %d tokens]...\n", tokens-cm.MicrocompactToolResult) +
			string(runes[len(runes)-tail:])
		result[i].Content = truncated
		changed = true
	}

	if changed {
		// 记录 snip 释放的 token
		freed := cm.EstimateTokens(messages) - cm.EstimateTokens(result)
		if freed > 0 {
			cm.AddSnipTokensFreed(freed)
		}
	}
	return result
}

// SessionMemoryCompaction 会话记忆压缩: 本地提取关键信息构造摘要
// 参考 cc-haha trySessionMemoryCompaction: 在调用 LLM compact 前优先尝试
// 提取: 用户目标 + 关键决策 + 文件路径 + 错误教训
func (cm *ContextManager) SessionMemoryCompaction(messages []llm.Message) []llm.Message {
	var systemMsgs, otherMsgs []llm.Message
	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}

	if len(otherMsgs) <= cm.KeepRecent+1 {
		return nil
	}

	keepCount := cm.KeepRecent
	if keepCount > len(otherMsgs) {
		keepCount = len(otherMsgs)
	}
	toCompress := otherMsgs[:len(otherMsgs)-keepCount]
	toKeep := otherMsgs[len(otherMsgs)-keepCount:]

	// 提取关键信息
	var userGoals []string
	var assistantDecisions []string
	var filePaths []string
	var errors []string
	var toolCalls []string

	pathSet := make(map[string]bool)

	for _, m := range toCompress {
		switch m.Role {
		case "user":
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				truncated := content
				if len([]rune(truncated)) > 300 {
					truncated = string([]rune(truncated)[:300]) + "..."
				}
				userGoals = append(userGoals, truncated)
				// 提取文件路径
				extractPaths(content, pathSet)
			}
		case "assistant":
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				// 提取关键决策 (含"应该"/"需要"/"决定"的句子)
				for _, line := range strings.Split(content, "\n") {
					if strings.Contains(line, "应该") || strings.Contains(line, "需要") ||
						strings.Contains(line, "决定") || strings.Contains(line, "计划") {
						if len([]rune(line)) < 200 {
							assistantDecisions = append(assistantDecisions, strings.TrimSpace(line))
						}
					}
				}
			}
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, tc.Name)
			}
		case "tool":
			if content, ok := m.Content.(string); ok {
				// 提取错误信息
				if strings.Contains(content, "Error") || strings.Contains(content, "错误") ||
					strings.Contains(content, "失败") {
					for _, line := range strings.Split(content, "\n") {
						if strings.Contains(line, "Error") || strings.Contains(line, "错误") ||
							strings.Contains(line, "失败") {
							if len([]rune(line)) < 200 {
								errors = append(errors, strings.TrimSpace(line))
							}
						}
					}
				}
				extractPaths(content, pathSet)
			}
		}
	}

	for p := range pathSet {
		filePaths = append(filePaths, p)
	}

	// 限制各部分数量
	if len(userGoals) > 5 {
		userGoals = userGoals[len(userGoals)-5:]
	}
	if len(assistantDecisions) > 10 {
		assistantDecisions = assistantDecisions[len(assistantDecisions)-10:]
	}
	if len(errors) > 5 {
		errors = errors[len(errors)-5:]
	}
	if len(filePaths) > 15 {
		filePaths = filePaths[len(filePaths)-15:]
	}

	if len(userGoals) == 0 && len(assistantDecisions) == 0 && len(errors) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("<session_memory>\n")
	if len(userGoals) > 0 {
		sb.WriteString("## 用户目标\n")
		for i, g := range userGoals {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, g)
		}
	}
	if len(assistantDecisions) > 0 {
		sb.WriteString("\n## 关键决策\n")
		for i, d := range assistantDecisions {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, d)
		}
	}
	if len(filePaths) > 0 {
		sb.WriteString("\n## 涉及文件\n")
		for _, p := range filePaths {
			fmt.Fprintf(&sb, "- %s\n", p)
		}
	}
	if len(errors) > 0 {
		sb.WriteString("\n## 错误教训\n")
		for i, e := range errors {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, e)
		}
	}
	sb.WriteString("</session_memory>\n以上是之前对话的会话记忆，请基于此继续。")

	summaryMsg := llm.Message{Role: "user", Content: sb.String()}
	result := make([]llm.Message, 0, len(systemMsgs)+1+len(toKeep))
	result = append(result, systemMsgs...)
	result = append(result, summaryMsg)
	result = append(result, toKeep...)
	return result
}

// extractPaths 从文本中提取文件路径
func extractPaths(text string, set map[string]bool) {
	// 匹配常见路径模式
	patterns := []string{
		`[a-zA-Z]:[\\\/][^\s"',<>|:*?]+`,  // Windows 绝对路径
		`/[a-zA-Z][^\s"',<>|:*?]+`,         // Unix 绝对路径
		`[\w\-./]+\.(go|py|js|ts|json|md)`, // 常见代码文件
	}
	for _, p := range patterns {
		matches := regexFindAll(text, p)
		for _, m := range matches {
			if len(m) > 3 && len(m) < 200 {
				set[m] = true
			}
		}
	}
}

// Compact 压缩上下文
// 策略 (多级降级, 参考 cc-haha):
//  0. microcompact: 先裁剪超长 tool 结果 (不调 LLM)
//  1. session memory: 本地提取关键信息 (不调 LLM)
//  2. 调用 LLM 生成摘要 (proactive compact)
//  3. 失败则尝试 reactive compact (简化摘要)
//  4. 仍失败则硬截断 (保留 system + 最近 KeepRecent)
//
// 连续失败保护: 连续 maxConsecutiveCompactFailures 次失败后直接硬截断
//
// 保留: system 消息 + 压缩摘要 + 最近 KeepRecent 条
func (cm *ContextManager) Compact(messages []llm.Message) ([]llm.Message, error) {
	// 递归守卫: 防止 compact LLM 调用再次触发 compact
	cm.recursionGuard.Store(true)
	defer cm.recursionGuard.Store(false)

	cm.mu.Lock()
	cm.compactAttempts++
	// 连续失败保护: 超过上限直接硬截断
	if cm.consecutiveFailures >= maxConsecutiveCompactFailures {
		cm.mu.Unlock()
		return cm.hardTruncate(messages), errors.New("context compact failed too many times, forced hard truncate")
	}
	cm.mu.Unlock()

	if len(messages) <= cm.KeepRecent+1 {
		return messages, nil // 消息太少，不压缩
	}

	// 硬上限检查: 超过 HardLimit 直接硬截断, 不再尝试 LLM 压缩
	if cm.IsOverHardLimit(messages) {
		cm.mu.Lock()
		cm.consecutiveFailures++
		cm.mu.Unlock()
		return cm.hardTruncate(messages), errors.New("context over hard limit, forced hard truncate")
	}

	// Level 0: microcompact - 先裁剪超长 tool 结果
	microcompacted := cm.Microcompact(messages)
	if cm.EstimateTokens(microcompacted) < cm.EstimateTokens(messages) {
		// microcompact 释放了足够空间, 可能不需要完整 compact
		if !cm.IsOverHardLimit(microcompacted) && !cm.ShouldCompact(microcompacted) {
			cm.mu.Lock()
			cm.consecutiveFailures = 0
			cm.mu.Unlock()
			return microcompacted, nil
		}
		messages = microcompacted
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

	// Level 1: session memory compaction - 本地提取关键信息 (不调 LLM)
	if smResult := cm.SessionMemoryCompaction(messages); smResult != nil {
		// 检查 session memory 是否足够 (降到 CompactAt 以下)
		if cm.EstimateTokens(smResult) < int(float64(cm.MaxTokens)*cm.CompactAt) {
			cm.Compacted = true
			cm.mu.Lock()
			cm.consecutiveFailures = 0
			cm.lastCompactErr = nil
			cm.mu.Unlock()
			return smResult, nil
		}
	}

	// Level 2: 调用 LLM 压缩
	summary, err := cm.callCompactLLM(toCompress)
	if err == nil && summary != "" {
		summaryMsg := llm.Message{
			Role: "user",
			Content: fmt.Sprintf("<previous_context_summary>\n%s\n</previous_context_summary>\n以上是之前对话的摘要，请基于此继续。", summary),
		}
		result := make([]llm.Message, 0, len(systemMsgs)+1+keepCount)
		result = append(result, systemMsgs...)
		result = append(result, summaryMsg)
		result = append(result, toKeep...)

		cm.Compacted = true
		cm.mu.Lock()
		cm.consecutiveFailures = 0
		cm.lastCompactErr = nil
		cm.mu.Unlock()
		return result, nil
	}

	// Level 3: reactive compact - 简化摘要 (本地提取, 不调 LLM)
	cm.mu.Lock()
	cm.lastCompactErr = err
	cm.consecutiveFailures++
	cm.mu.Unlock()
	if reactiveResult := cm.reactiveCompact(systemMsgs, toCompress, toKeep); reactiveResult != nil {
		cm.Compacted = true
		return reactiveResult, nil
	}

	// Level 4: 硬截断
	return cm.hardTruncate(messages), err
}

// reactiveCompact 反应式压缩: 不调用 LLM, 本地提取关键信息
// 参考 cc-haha reactiveCompact (API 413 触发时的降级路径)
func (cm *ContextManager) reactiveCompact(systemMsgs, toCompress, toKeep []llm.Message) []llm.Message {
	// 提取用户消息的核心目标和最近的工具调用结果
	var userGoals []string
	var lastToolResults []string

	for _, m := range toCompress {
		if m.Role == "user" {
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				// 只保留用户消息的前 200 字符
				truncated := content
				if len([]rune(truncated)) > 200 {
					truncated = string([]rune(truncated)[:200]) + "..."
				}
				userGoals = append(userGoals, truncated)
			}
		}
		if m.Role == "tool" && len(lastToolResults) < 3 {
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				truncated := content
				if len([]rune(truncated)) > 300 {
					truncated = string([]rune(truncated)[:300]) + "..."
				}
				lastToolResults = append(lastToolResults, truncated)
			}
		}
	}

	if len(userGoals) == 0 && len(lastToolResults) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("<previous_context_summary>\n")
	if len(userGoals) > 0 {
		sb.WriteString("用户目标:\n")
		for i, g := range userGoals {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, g)
		}
	}
	if len(lastToolResults) > 0 {
		sb.WriteString("\n最近工具结果:\n")
		for i, r := range lastToolResults {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, r)
		}
	}
	sb.WriteString("</previous_context_summary>\n以上是之前对话的摘要，请基于此继续。")

	summaryMsg := llm.Message{Role: "user", Content: sb.String()}
	result := make([]llm.Message, 0, len(systemMsgs)+1+len(toKeep))
	result = append(result, systemMsgs...)
	result = append(result, summaryMsg)
	result = append(result, toKeep...)
	return result
}

// hardTruncate 硬截断: 只保留 system + 最近 KeepRecent 条
func (cm *ContextManager) hardTruncate(messages []llm.Message) []llm.Message {
	var systemMsgs, otherMsgs []llm.Message
	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}
	keepCount := cm.KeepRecent
	if keepCount > len(otherMsgs) {
		keepCount = len(otherMsgs)
	}
	toKeep := otherMsgs[len(otherMsgs)-keepCount:]

	notice := llm.Message{
		Role: "user",
		Content: "<context_truncated>上下文已超过硬上限，早期对话已被截断。请基于当前可见信息继续。</context_truncated>",
	}

	result := make([]llm.Message, 0, len(systemMsgs)+1+keepCount)
	result = append(result, systemMsgs...)
	result = append(result, notice)
	result = append(result, toKeep...)
	return result
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

// AddSnipTokensFreed 记录 snip 释放的 token 数
// 用于消息已删除但 usage 仍反映旧值的修正
func (cm *ContextManager) AddSnipTokensFreed(n int) {
	cm.snipTokensFreed.Add(int64(n))
}

// ResetSnipTokens 重置 snip 计数 (压缩成功后调用)
func (cm *ContextManager) ResetSnipTokens() {
	cm.snipTokensFreed.Store(0)
}

// GetCompactStats 获取压缩统计
func (cm *ContextManager) GetCompactStats() (attempts int, lastErr error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.compactAttempts, cm.lastCompactErr
}
