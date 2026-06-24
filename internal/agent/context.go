package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
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
//   - 精确估算: 中英文混合 + 工具调用 JSON + 图片附件 + LLM usage 自校准
//   - microcompact: 工具结果裁剪 (超过阈值时截断长 tool 结果)
//   - session memory: 优先尝试会话记忆压缩 (提取关键信息)
//   - 分级警告: warning(70%) / error(85%) / hard(95%)
//   - 连续失败保护: MAX_CONSECUTIVE_COMPACT_FAILURES=3 后直接硬截断
//   - 压缩历史累积: 多次压缩后保留历史摘要链, 防止信息丢失
//   - cache-aware: 压缩时保留消息前缀稳定, 避免破坏 LLM 缓存
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
	MicrocompactAssistant  int // 单个 assistant 消息超过此 token 数则分段裁剪 (默认 6000)

	// thinking 块裁剪: 保留最近 N 轮 thinking, 清除更早的
	// 参考 cc-haha clear_thinking_20251015: 旧 thinking 块对后续生成无价值
	KeepThinkingRounds int // 保留最近几轮 thinking (默认 2, 0=不裁剪)

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

	// 压缩历史累积: 每次压缩的摘要链接保存, 新摘要引用旧摘要
	// 防止多次压缩后早期信息完全丢失
	compactionHistory []string

	// cache-aware 压缩: 记录稳定的缓存前缀边界
	// 压缩时只动中段, 保留前缀以复用 LLM 缓存
	cachePrefixStable int // 前缀消息数 (这些消息不被压缩)

	// Token 自校准: 基于 LLM 真实 usage 调整估算系数
	// 初始系数 1.0, 每次 LLM 返回真实 usage 后更新
	calibrationMu       sync.RWMutex
	calibrationFactor   float64 // 估算值 * factor ≈ 真实值
	calibrationSamples  int     // 校准样本数
	lastRealInputTokens int     // 最近一次 LLM 真实输入 token

	// F4: 持久化路径 (可选, 设置后 compactionHistory + calibration 会落盘)
	// 格式: context_meta.json, 存储压缩历史和校准系数
	metaPath string
}

// F4: contextMeta 持久化的上下文元数据
type contextMeta struct {
	CompactionHistory  []string `json:"compaction_history"`
	CalibrationFactor  float64  `json:"calibration_factor"`
	CalibrationSamples int      `json:"calibration_samples"`
}

// 连续失败上限
const maxConsecutiveCompactFailures = 3

// F4: SetMetaPath 设置持久化路径并立即加载已有元数据
// 设置后, compactionHistory 和 calibrationFactor 会自动落盘/加载
func (cm *ContextManager) SetMetaPath(path string) {
	cm.mu.Lock()
	cm.metaPath = path
	cm.mu.Unlock()
	cm.LoadMeta()
}

// F4: LoadMeta 从磁盘加载上下文元数据 (compactionHistory + calibration)
func (cm *ContextManager) LoadMeta() {
	cm.mu.Lock()
	path := cm.metaPath
	cm.mu.Unlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // 无文件, 首次使用
	}
	var meta contextMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return
	}

	cm.mu.Lock()
	if len(meta.CompactionHistory) > 0 {
		cm.compactionHistory = meta.CompactionHistory
	}
	cm.mu.Unlock()

	cm.calibrationMu.Lock()
	if meta.CalibrationFactor > 0 && meta.CalibrationFactor < 10 {
		cm.calibrationFactor = meta.CalibrationFactor
		cm.calibrationSamples = meta.CalibrationSamples
	}
	cm.calibrationMu.Unlock()
}

// F4: SaveMeta 将上下文元数据落盘 (compactionHistory + calibration)
// 原子写: tmp + rename
func (cm *ContextManager) SaveMeta() {
	cm.mu.Lock()
	path := cm.metaPath
	history := cm.compactionHistory
	cm.mu.Unlock()

	cm.calibrationMu.RLock()
	factor := cm.calibrationFactor
	samples := cm.calibrationSamples
	cm.calibrationMu.RUnlock()

	if path == "" {
		return
	}

	meta := contextMeta{
		CompactionHistory:  history,
		CalibrationFactor:  factor,
		CalibrationSamples: samples,
	}
	data, err := json.MarshalIndent(&meta, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path)
}

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
		MicrocompactAssistant:  6000,
		KeepRecent:             4, // 保留最近4条消息(约2轮)
		KeepThinkingRounds:     2, // 保留最近2轮 thinking, 清除更早的
		calibrationFactor:      1.0,
		cachePrefixStable:      2, // 默认保留 system + 首条 user 作为缓存前缀
	}
}

// EstimateTokens 估算消息列表的 token 数
// 规则: 中文1字≈1.5 token, 英文1词≈1.3 token, 工具调用按 JSON 长度估算
// 每条消息额外 4 token 开销 (角色标记等)
// 应用自校准系数: 基于 LLM 真实 usage 反馈调整
func (cm *ContextManager) EstimateTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += cm.estimateMessageTokens(m)
	}
	// 每条消息额外开销(角色标记等)
	total += len(messages) * 4

	// 应用校准系数
	cm.calibrationMu.RLock()
	factor := cm.calibrationFactor
	cm.calibrationMu.RUnlock()
	if factor != 1.0 && factor > 0 {
		total = int(float64(total) * factor)
	}
	return total
}

// RecordRealUsage 记录 LLM 真实 usage, 自校准估算系数
// 在每次 LLM 调用返回后调用, 传入真实 input_tokens 和对应的消息列表
func (cm *ContextManager) RecordRealUsage(realInputTokens int, messages []llm.Message) {
	if realInputTokens <= 0 || len(messages) == 0 {
		return
	}
	estimated := cm.estimateRawTokens(messages) // 不含校准系数的原始估算

	cm.calibrationMu.Lock()

	cm.lastRealInputTokens = realInputTokens

	// 计算新系数 = 真实值 / 估算值
	newFactor := float64(realInputTokens) / float64(estimated)
	if newFactor <= 0 || newFactor > 10 {
		cm.calibrationMu.Unlock()
		return // 异常值, 忽略
	}

	// 指数移动平均 (EMA) 平滑更新
	if cm.calibrationSamples == 0 {
		cm.calibrationFactor = newFactor
	} else {
		// EMA: factor = 0.7*old + 0.3*new (偏重历史, 避免单次抖动)
		cm.calibrationFactor = 0.7*cm.calibrationFactor + 0.3*newFactor
	}
	cm.calibrationSamples++

	// F4: 每 10 次校准落盘一次 (避免频繁 IO)
	// 注意: 此处仍持有 calibrationMu, SaveMeta 内部会获取 mu 锁 (无嵌套, 因锁不同)
	shouldSave := cm.calibrationSamples%10 == 0
	cm.calibrationMu.Unlock()
	if shouldSave {
		cm.SaveMeta()
	}
}

// estimateRawTokens 原始估算 (不含校准系数)
func (cm *ContextManager) estimateRawTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += cm.estimateMessageTokens(m)
	}
	total += len(messages) * 4
	return total
}

// CountTokensPrecise 调用 LLM provider 的 count_tokens API 获取精确 token 数
// 当 API 不可用或调用失败时, 降级到本地估算 (EstimateTokens)
// 参考 cc-haha tokenEstimation.ts: countTokensViaAPI + 本地估算降级
// 参数:
//   - messages: 待计数的消息列表
// 返回:
//   - precise: 是否使用了精确计数 (true=API, false=本地估算)
//   - tokens: token 数
func (cm *ContextManager) CountTokensPrecise(messages []llm.Message) (precise bool, tokens int) {
	if cm.Client == nil {
		return false, cm.EstimateTokens(messages)
	}
	count, err := cm.Client.CountTokens(messages)
	if err != nil || count <= 0 {
		// 降级到本地估算
		return false, cm.EstimateTokens(messages)
	}
	return true, count
}

// GetCalibrationInfo 获取校准信息 (调试用)
func (cm *ContextManager) GetCalibrationInfo() (factor float64, samples int, lastReal int) {
	cm.calibrationMu.RLock()
	defer cm.calibrationMu.RUnlock()
	return cm.calibrationFactor, cm.calibrationSamples, cm.lastRealInputTokens
}

// estimateMessageTokens 估算单条消息的 token 数
// 增强: 处理多模态 ContentBlock 数组 (图片/document/thinking/tool_use/tool_result)
// 参考 cc-haha tokenEstimation.ts: image=2000, document=2000, thinking 单独估算
func (cm *ContextManager) estimateMessageTokens(m llm.Message) int {
	tokens := 0

	// content 部分: 支持字符串和 ContentBlock 数组两种形态
	tokens += estimateContentTokens(m.Content)

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

// estimateContentTokens 估算 Content 字段的 token 数
// 支持: string / []ContentBlock / []map[string]any / []any (多模态)
// 参考 cc-haha tokenEstimation.ts:
//   - image: ~2000 tokens (含 base64 编码开销)
//   - document: ~2000 tokens
//   - thinking: 按 text 估算 (推理链内容)
//   - tool_use: name + input JSON
//   - tool_result: content 估算
func estimateContentTokens(content any) int {
	if content == nil {
		return 0
	}

	// 形态 1: 纯字符串
	if s, ok := content.(string); ok {
		return estimateStringTokens(s)
	}

	// 形态 2: []llm.ContentBlock (代码构造)
	if blocks, ok := content.([]llm.ContentBlock); ok {
		total := 0
		for _, b := range blocks {
			total += estimateContentBlockTokens(b.Type, b.Text, b.Name, b.Input, b.Thinking)
		}
		return total
	}

	// 形态 3: []map[string]any (JSON 解析)
	if blocks, ok := content.([]map[string]any); ok {
		total := 0
		for _, b := range blocks {
			total += estimateContentBlockFromMap(b)
		}
		return total
	}

	// 形态 4: []any (通用 JSON 解析)
	if blocks, ok := content.([]any); ok {
		total := 0
		for _, b := range blocks {
			if m, ok := b.(map[string]any); ok {
				total += estimateContentBlockFromMap(m)
			} else if s, ok := b.(string); ok {
				total += estimateStringTokens(s)
			}
		}
		return total
	}

	// 兜底: JSON 序列化后估算
	if data, err := json.Marshal(content); err == nil {
		return estimateStringTokens(string(data))
	}
	return 0
}

// estimateContentBlockTokens 估算单个 ContentBlock 的 token 数
func estimateContentBlockTokens(blockType, text, name string, input map[string]any, thinking string) int {
	switch blockType {
	case "image":
		// 图片: 固定估算 2000 token (参考 cc-haha, 含 base64 编码开销)
		return 2000
	case "document":
		// 文档 (PDF 等): 固定估算 2000 token
		return 2000
	case "thinking":
		// 推理链: 按 text + thinking 估算
		return estimateStringTokens(text) + estimateStringTokens(thinking)
	case "tool_use":
		// 工具调用: name + input JSON + 结构开销
		tokens := estimateStringTokens(name) + 8
		if input != nil {
			if data, err := json.Marshal(input); err == nil {
				tokens += estimateStringTokens(string(data))
			}
		}
		return tokens
	case "tool_result":
		// 工具结果: 按 text 估算
		return estimateStringTokens(text)
	default:
		// 默认: text 类型
		return estimateStringTokens(text)
	}
}

// estimateContentBlockFromMap 从 map 估算 ContentBlock token 数
func estimateContentBlockFromMap(b map[string]any) int {
	blockType, _ := b["type"].(string)
	text, _ := b["text"].(string)
	name, _ := b["name"].(string)
	thinking, _ := b["thinking"].(string)
	input := b["input"]
	var inputMap map[string]any
	if m, ok := input.(map[string]any); ok {
		inputMap = m
	}
	return estimateContentBlockTokens(blockType, text, name, inputMap, thinking)
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

// Microcompact 微压缩: 裁剪过长的工具结果和 assistant 消息
// 参考 cc-haha microcompactMessages: 在完整 compact 前先尝试裁剪
// 不调用 LLM, 仅本地截断超长内容, 保留首尾
// P8 增强: 扩展到 assistant 消息 (超长代码生成/分析)
func (cm *ContextManager) Microcompact(messages []llm.Message) []llm.Message {
	changed := false
	result := make([]llm.Message, len(messages))
	copy(result, messages)

	// thinking 块裁剪: 清除旧 thinking 块, 只保留最近 N 轮
	// 参考 cc-haha clear_thinking_20251015: 旧 thinking 对后续生成无价值
	if cm.KeepThinkingRounds > 0 {
		if cleared := cm.clearOldThinking(result, cm.KeepThinkingRounds); cleared {
			changed = true
		}
	}

	for i, m := range result {
		content, ok := m.Content.(string)
		if !ok || content == "" {
			continue
		}
		tokens := estimateStringTokens(content)

		switch m.Role {
		case "tool":
			// 工具结果裁剪
			if cm.MicrocompactToolResult <= 0 || tokens <= cm.MicrocompactToolResult {
				continue
			}
			// P-stress: 优先保留错误/失败行 (FAIL/Error/panic), 这些对后续修复至关重要
			truncated := cm.truncateToolResult(content, tokens, cm.MicrocompactToolResult)
			if truncated != content {
				result[i].Content = truncated
				changed = true
			}

		case "assistant":
			// assistant 消息裁剪 (超长代码生成/分析)
			// 保留 tool_calls 完整, 只裁剪 content
			if cm.MicrocompactAssistant <= 0 || tokens <= cm.MicrocompactAssistant {
				continue
			}
			// 如果含 tool_calls, 谨慎裁剪 (保留调用上下文)
			if len(m.ToolCalls) > 0 {
				// 有 tool_calls 的 assistant 消息只裁剪 content 的前半部分
				keep := cm.MicrocompactAssistant * 2 / 3
				truncated := cm.truncateContent(content, tokens, keep)
				if truncated != content {
					result[i].Content = truncated
					changed = true
				}
			} else {
				// 纯文本 assistant 消息, 保留首尾
				truncated := cm.truncateContent(content, tokens, cm.MicrocompactAssistant)
				if truncated != content {
					result[i].Content = truncated
					changed = true
				}
			}
		}
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

// clearOldThinking 清除旧的 thinking 块, 只保留最近 keepRounds 轮
// thinking 块存在于 ContentBlock 数组中 (type=thinking)
// 参考 cc-haha clear_thinking_20251015: 旧 thinking 块对后续生成无价值, 可安全删除
// 返回是否有修改
func (cm *ContextManager) clearOldThinking(messages []llm.Message, keepRounds int) bool {
	if keepRounds <= 0 || len(messages) == 0 {
		return false
	}

	// 统计含 thinking 块的 assistant 消息数, 标记需要清除的
	thinkingIndices := []int{}
	for i, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		if hasThinkingBlock(m.Content) {
			thinkingIndices = append(thinkingIndices, i)
		}
	}

	// 需要清除的: 除最近 keepRounds 轮外的所有
	if len(thinkingIndices) <= keepRounds {
		return false
	}
	toClear := thinkingIndices[:len(thinkingIndices)-keepRounds]

	changed := false
	for _, idx := range toClear {
		// hasThinkingBlock 已确认含 thinking 块, removeThinkingBlocks 一定会改变内容
		messages[idx].Content = removeThinkingBlocks(messages[idx].Content)
		changed = true
	}
	return changed
}

// hasThinkingBlock 检查 Content 是否含 thinking 类型的 ContentBlock
func hasThinkingBlock(content any) bool {
	if content == nil {
		return false
	}
	// []llm.ContentBlock
	if blocks, ok := content.([]llm.ContentBlock); ok {
		for _, b := range blocks {
			if b.Type == "thinking" {
				return true
			}
		}
		return false
	}
	// []map[string]any
	if blocks, ok := content.([]map[string]any); ok {
		for _, b := range blocks {
			if t, ok := b["type"].(string); ok && t == "thinking" {
				return true
			}
		}
		return false
	}
	// []any
	if blocks, ok := content.([]any); ok {
		for _, b := range blocks {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "thinking" {
					return true
				}
			}
		}
	}
	return false
}

// removeThinkingBlocks 从 Content 中移除 thinking 类型的 ContentBlock
func removeThinkingBlocks(content any) any {
	if content == nil {
		return content
	}
	// []llm.ContentBlock
	if blocks, ok := content.([]llm.ContentBlock); ok {
		filtered := make([]llm.ContentBlock, 0, len(blocks))
		for _, b := range blocks {
			if b.Type != "thinking" {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) == 0 {
			return "" // 全部是 thinking, 返回空字符串
		}
		return filtered
	}
	// []map[string]any
	if blocks, ok := content.([]map[string]any); ok {
		filtered := make([]map[string]any, 0, len(blocks))
		for _, b := range blocks {
			if t, ok := b["type"].(string); !ok || t != "thinking" {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) == 0 {
			return ""
		}
		return filtered
	}
	// []any
	if blocks, ok := content.([]any); ok {
		filtered := make([]any, 0, len(blocks))
		for _, b := range blocks {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "thinking" {
					continue
				}
			}
			filtered = append(filtered, b)
		}
		if len(filtered) == 0 {
			return ""
		}
		return filtered
	}
	return content
}

// truncateContent 截断内容: 保留前 head% + 后 tail% + 中间省略标记
func (cm *ContextManager) truncateContent(content string, currentTokens, targetTokens int) string {
	if currentTokens <= targetTokens {
		return content
	}
	runes := []rune(content)
	// 估算保留字符数 (targetTokens / 1.5 近似中文, / 1.3 近似英文, 取中间值)
	keep := int(float64(targetTokens) / 1.4)
	head := keep * 2 / 5
	tail := keep * 3 / 5
	if head+tail >= len(runes) {
		return content
	}
	return string(runes[:head]) +
		fmt.Sprintf("\n...[truncated %d tokens]...\n", currentTokens-targetTokens) +
		string(runes[len(runes)-tail:])
}

// truncateToolResult 裁剪工具输出, 优先保留错误/失败行
// 参考 cc-haha: 工具输出中的 FAIL/Error/panic 行对后续修复至关重要
func (cm *ContextManager) truncateToolResult(content string, currentTokens, targetTokens int) string {
	if currentTokens <= targetTokens {
		return content
	}

	// 提取错误行 (FAIL/Error/panic/exception/traceback 等)
	errorLines := []string{}
	// criticalLines: panic/traceback 等致命错误, 优先保留
	criticalLines := []string{}
	criticalKws := []string{"panic", "traceback", "fatal", "segfault"}
	errorKws := []string{"fail", "error", "panic", "exception", "traceback", "undefined", "cannot", "nil pointer"}
	for _, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(line)
		isCritical := false
		for _, kw := range criticalKws {
			if strings.Contains(lower, kw) {
				isCritical = true
				break
			}
		}
		for _, kw := range errorKws {
			if strings.Contains(lower, kw) {
				if isCritical {
					criticalLines = append(criticalLines, line)
				} else {
					errorLines = append(errorLines, line)
				}
				break
			}
		}
	}

	// 常规裁剪 (保留首尾)
	truncated := cm.truncateContent(content, currentTokens, targetTokens)

	// 检查裁剪后丢失的错误行
	missingCritical := []string{}
	for _, el := range criticalLines {
		if el == "" {
			continue
		}
		if !strings.Contains(truncated, el) {
			missingCritical = append(missingCritical, el)
		}
	}
	missingErrors := []string{}
	for _, el := range errorLines {
		if el == "" {
			continue
		}
		if !strings.Contains(truncated, el) {
			missingErrors = append(missingErrors, el)
		}
	}

	if len(missingCritical) == 0 && len(missingErrors) == 0 {
		return truncated
	}

	// critical 行全部保留 (不限制数量), 普通错误行限制最后 10 行
	if len(missingErrors) > 10 {
		missingErrors = missingErrors[len(missingErrors)-10:]
	}
	var sb strings.Builder
	sb.WriteString(truncated)
	sb.WriteString("\n...[preserved error lines]...\n")
	// 先写 critical (panic/traceback 等)
	for _, el := range missingCritical {
		sb.WriteString(el)
		sb.WriteString("\n")
	}
	for _, el := range missingErrors {
		sb.WriteString(el)
		sb.WriteString("\n")
	}
	return sb.String()
}

// SessionMemoryCompaction 会话记忆压缩: 本地提取关键信息构造摘要
// 参考 cc-haha trySessionMemoryCompaction: 在调用 LLM compact 前优先尝试
// P6 增强: 扩展关键词 (中英文) + 代码块提取 + URL + 函数名 + 用户确认
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
	var codeSnippets []string  // P6: 代码块提取
	var urls []string          // P6: URL 提取
	var userConfirmations []string // P6: 用户确认/反馈

	pathSet := make(map[string]bool)
	urlSet := make(map[string]bool)

	// P6: 扩展关键词 (中英文)
	decisionKeywords := []string{
		"应该", "需要", "决定", "计划", "将要", "必须", "建议", "方案",
		"should", "need", "decide", "plan", "will", "must", "suggest", "approach",
	}
	// P6: 用户确认关键词
	confirmKeywords := []string{
		"对", "是的", "确认", "同意", "好的", "可以", "没问题", "继续",
		"yes", "ok", "correct", "confirm", "agree", "continue", "proceed",
	}

	for _, m := range toCompress {
		switch m.Role {
		case "user":
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				truncated := content
				if len([]rune(truncated)) > 300 {
					truncated = string([]rune(truncated)[:300]) + "..."
				}
				// P7: 检查是否是确认/反馈 (短消息)
				if len([]rune(content)) < 50 {
					lower := strings.ToLower(content)
					for _, kw := range confirmKeywords {
						if strings.Contains(lower, kw) {
							userConfirmations = append(userConfirmations, strings.TrimSpace(content))
							break
						}
					}
				}
				userGoals = append(userGoals, truncated)
				extractPaths(content, pathSet)
				extractURLs(content, urlSet)
			}
		case "assistant":
			if content, ok := m.Content.(string); ok && len(content) > 0 {
				// P6: 提取代码块
				codeSnippets = append(codeSnippets, extractCodeBlocks(content)...)

				// P6: 扩展关键词匹配 (中英文)
				for _, line := range strings.Split(content, "\n") {
					trimmed := strings.TrimSpace(line)
					lower := strings.ToLower(trimmed)
					for _, kw := range decisionKeywords {
						if strings.Contains(trimmed, kw) || strings.Contains(lower, kw) {
							if len([]rune(trimmed)) < 200 {
								assistantDecisions = append(assistantDecisions, trimmed)
							}
							break
						}
					}
				}
			}
			for _, tc := range m.ToolCalls {
				toolCalls = append(toolCalls, tc.Name)
			}
		case "tool":
			if content, ok := m.Content.(string); ok {
				// P6: 改进错误提取 (含 stack trace 模式)
				if isErrorContent(content) {
					for _, line := range strings.Split(content, "\n") {
						if isErrorLine(line) && len([]rune(line)) < 200 {
							errors = append(errors, strings.TrimSpace(line))
						}
					}
				}
				extractPaths(content, pathSet)
				extractURLs(content, urlSet)
			}
		}
	}

	for p := range pathSet {
		filePaths = append(filePaths, p)
	}
	for u := range urlSet {
		urls = append(urls, u)
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
	if len(codeSnippets) > 3 {
		codeSnippets = codeSnippets[len(codeSnippets)-3:]
	}
	if len(urls) > 5 {
		urls = urls[len(urls)-5:]
	}
	if len(userConfirmations) > 3 {
		userConfirmations = userConfirmations[len(userConfirmations)-3:]
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
	if len(urls) > 0 {
		sb.WriteString("\n## 相关链接\n")
		for _, u := range urls {
			fmt.Fprintf(&sb, "- %s\n", u)
		}
	}
	if len(codeSnippets) > 0 {
		sb.WriteString("\n## 关键代码\n")
		for i, c := range codeSnippets {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, c)
		}
	}
	if len(userConfirmations) > 0 {
		sb.WriteString("\n## 用户确认\n")
		for i, c := range userConfirmations {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, c)
		}
	}
	// P-stress: 工具调用记录 (之前被提取但未输出, 导致 file_patch 等信息丢失)
	if len(toolCalls) > 0 {
		// 去重
		seen := make(map[string]bool)
		unique := toolCalls[:0]
		for _, tc := range toolCalls {
			if !seen[tc] {
				seen[tc] = true
				unique = append(unique, tc)
			}
		}
		if len(unique) > 10 {
			unique = unique[len(unique)-10:]
		}
		sb.WriteString("\n## 使用工具\n")
		for _, tc := range unique {
			fmt.Fprintf(&sb, "- %s\n", tc)
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

// P6: extractURLs 从文本中提取 URL
var reURL = regexp.MustCompile(`https?://[^\s"'<>]+`)

func extractURLs(text string, set map[string]bool) {
	matches := reURL.FindAllString(text, -1)
	for _, u := range matches {
		if len(u) > 10 && len(u) < 500 {
			set[u] = true
		}
	}
}

// P6: extractCodeBlocks 从文本中提取代码块 (```...```)
func extractCodeBlocks(text string) []string {
	var blocks []string
	// 匹配 ```lang\n...\n``` 代码块
	re := regexp.MustCompile("(?s)```(?:\\w+)?\\n(.*?)```")
	matches := re.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) > 1 {
			code := strings.TrimSpace(m[1])
			if len([]rune(code)) > 500 {
				code = string([]rune(code)[:500]) + "..."
			}
			blocks = append(blocks, code)
		}
	}
	return blocks
}

// P6: isErrorContent 判断内容是否包含错误
func isErrorContent(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "error") || strings.Contains(content, "错误") ||
		strings.Contains(content, "失败") || strings.Contains(lower, "exception") ||
		strings.Contains(lower, "traceback") || strings.Contains(lower, "panic")
}

// P6: isErrorLine 判断单行是否是错误信息
func isErrorLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	// 常见错误行模式
	errorPatterns := []string{
		"error", "错误", "失败", "exception", "traceback", "panic",
		"failed", "fatal", "cannot", "unable", "invalid",
	}
	for _, p := range errorPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// P7: scoreMessage 评估消息重要性 (0-100)
// 用于选择性保留: 高分消息优先保留, 低分消息优先压缩
// 评分维度:
//   - 含文件路径 (+20)
//   - 含错误信息 (+20)
//   - 含用户目标/指令 (+15)
//   - 含关键决策 (+15)
//   - 含代码块 (+10)
//   - 含 URL (+5)
//   - 最近的消息 (+15, 按距离衰减)
func (cm *ContextManager) scoreMessage(m llm.Message, distanceFromEnd int) int {
	score := 0
	content, ok := m.Content.(string)
	if !ok || content == "" {
		return 0
	}

	// 含文件路径
	pathSet := make(map[string]bool)
	extractPaths(content, pathSet)
	if len(pathSet) > 0 {
		score += 20
	}

	// 含错误信息
	if isErrorContent(content) {
		score += 20
	}

	// 用户消息 (目标/指令)
	if m.Role == "user" {
		score += 15
	}

	// 含关键决策
	lower := strings.ToLower(content)
	decisionKws := []string{"应该", "需要", "决定", "计划", "should", "need", "decide", "plan"}
	for _, kw := range decisionKws {
		if strings.Contains(content, kw) || strings.Contains(lower, kw) {
			score += 15
			break
		}
	}

	// 含代码块
	if strings.Contains(content, "```") {
		score += 10
	}

	// 含 URL
	urlSet := make(map[string]bool)
	extractURLs(content, urlSet)
	if len(urlSet) > 0 {
		score += 5
	}

	// 距离衰减: 越近的消息分数越高
	if distanceFromEnd < 5 {
		score += 15 - distanceFromEnd*3
	}

	if score > 100 {
		score = 100
	}
	return score
}

// P7: selectImportantMessages 选择性保留重要消息
// 从 toCompress 中选出 top-K 重要消息, 压缩其余
// 返回: 保留的消息 + 需要压缩的消息
func (cm *ContextManager) selectImportantMessages(toCompress []llm.Message, keepTopK int) ([]llm.Message, []llm.Message) {
	if len(toCompress) <= keepTopK {
		return toCompress, nil
	}

	type scoredMsg struct {
		msg   llm.Message
		score int
		idx   int // 原始顺序
	}

	scored := make([]scoredMsg, len(toCompress))
	for i, m := range toCompress {
		dist := len(toCompress) - i
		scored[i] = scoredMsg{msg: m, score: cm.scoreMessage(m, dist), idx: i}
	}

	// 按分数排序, 取 top-K
	// 使用简单选择 (避免排序整个数组)
	threshold := 0
	if keepTopK < len(scored) {
		// 找第 keepTopK 大的分数
		scores := make([]int, len(scored))
		for i, s := range scored {
			scores[i] = s.score
		}
		sort.Ints(scores)
		threshold = scores[len(scores)-keepTopK]
	}

	var keep, compress []llm.Message
	kept := 0
	for _, s := range scored {
		if s.score >= threshold && kept < keepTopK {
			keep = append(keep, s.msg)
			kept++
		} else {
			compress = append(compress, s.msg)
		}
	}
	return keep, compress
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

	// cache-aware: 保留前缀消息 (这些消息不被压缩, 维持 LLM 缓存前缀稳定)
	// 前缀通常是 system + 首条 user 指令, 保持稳定可复用 LLM provider 的缓存
	prefixCount := cm.cachePrefixStable
	if prefixCount > len(otherMsgs)-cm.KeepRecent-1 {
		prefixCount = 0 // 前缀过大, 不启用
	}
	var prefixMsgs []llm.Message
	if prefixCount > 0 {
		prefixMsgs = otherMsgs[:prefixCount]
		otherMsgs = otherMsgs[prefixCount:]
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

	// Level 2: 调用 LLM 压缩 (注入历史摘要链, 防止多次压缩后信息丢失)
	// P7: 选择性保留 - 先选出重要消息, 只压缩次要消息
	importantKeep, toCompressFiltered := cm.selectImportantMessages(toCompress, 3)
	cm.mu.Lock()
	history := cm.compactionHistory
	cm.mu.Unlock()

	var summary string
	var err error
	if len(toCompressFiltered) > 0 {
		summary, err = cm.callCompactLLMWithHistory(toCompressFiltered, history)
	} else {
		// 全部重要, 无需压缩
		summary = ""
		err = nil
	}
	if err == nil && (summary != "" || len(importantKeep) > 0) {
		var summaryMsg llm.Message
		if summary != "" {
			summaryMsg = llm.Message{
				Role: "user",
				Content: fmt.Sprintf("<previous_context_summary>\n%s\n</previous_context_summary>\n以上是之前对话的摘要，请基于此继续。", summary),
			}
		}
		// cache-aware: system + prefix + important + summary + recent
		result := make([]llm.Message, 0, len(systemMsgs)+len(prefixMsgs)+len(importantKeep)+1+keepCount)
		result = append(result, systemMsgs...)
		result = append(result, prefixMsgs...) // 保留缓存前缀
		result = append(result, importantKeep...) // P7: 保留重要消息
		if summary != "" {
			result = append(result, summaryMsg)
		}
		result = append(result, toKeep...)

		cm.Compacted = true
		cm.mu.Lock()
		cm.consecutiveFailures = 0
		cm.lastCompactErr = nil
		// 累积压缩历史 (保留最近 5 条, 防止无限增长)
		if summary != "" {
			cm.compactionHistory = append(cm.compactionHistory, summary)
			if len(cm.compactionHistory) > 5 {
				cm.compactionHistory = cm.compactionHistory[len(cm.compactionHistory)-5:]
			}
		}
		cm.mu.Unlock()
		// F4: 持久化压缩历史
		cm.SaveMeta()
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
	return cm.callCompactLLMWithHistory(messages, nil)
}

// callCompactLLMWithHistory 调用 LLM 压缩, 注入历史摘要链
// history 为之前的压缩摘要, 新摘要应在此基础上累积而非覆盖
func (cm *ContextManager) callCompactLLMWithHistory(messages []llm.Message, history []string) (string, error) {
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

	// 构造历史摘要链提示
	var historyPrompt string
	if len(history) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n## 之前的压缩摘要 (请在此基础上累积, 不要丢失早期信息):\n")
		for i, h := range history {
			fmt.Fprintf(&sb, "### 摘要 #%d\n%s\n\n", i+1, h)
		}
		historyPrompt = sb.String()
	}

	prompt := fmt.Sprintf(`请将以下对话历史压缩为简洁摘要，保留:
1. 用户的核心目标和需求
2. 已完成的关键操作和结果
3. 待解决的问题和下一步
4. 关键文件路径、变量名、状态
删除冗余的工具输出细节和重复内容。摘要应控制在500字以内。
%s
对话历史:
%s`, historyPrompt, historyText.String())

	// PTL 重试: compact 请求本身可能 prompt-too-long
	// 参考 cc-haha compactConversation: 按 API-round 分组丢弃最旧消息, 最多 3 次
	const maxPTLRetries = 3
	currentMessages := messages
	for attempt := 0; attempt <= maxPTLRetries; attempt++ {
		// 使用非流式调用
		ch, err := cm.Client.Chat(llm.ChatParams{
			Messages: []llm.Message{
				{Role: "user", Content: prompt},
			},
			MaxTokens:   1024,
			Temperature: 0,
		})
		if err != nil {
			// 检测 PTL 错误
			if isPromptTooLongError(err) && attempt < maxPTLRetries {
				// 丢弃最旧的一组消息 (一个 API round: user+assistant+tool)
				truncated := cm.truncateHeadForPTLRetry(currentMessages, 1)
				if len(truncated) == len(currentMessages) {
					// 无法再截断, 返回错误
					return "", err
				}
				currentMessages = truncated
				// 重建 prompt (用截断后的消息)
				historyText.Reset()
				for _, m := range currentMessages {
					historyText.WriteString(fmt.Sprintf("[%s]: ", m.Role))
					if content, ok := m.Content.(string); ok {
						historyText.WriteString(content)
					}
					historyText.WriteString("\n")
				}
				prompt = fmt.Sprintf(`请将以下对话历史压缩为简洁摘要，保留:
1. 用户的核心目标和需求
2. 已完成的关键操作和结果
3. 待解决的问题和下一步
4. 关键文件路径、变量名、状态
删除冗余的工具输出细节和重复内容。摘要应控制在500字以内。
%s
对话历史:
%s`, historyPrompt, historyText.String())
				continue // 重试
			}
			return "", err
		}

		var result string
		for chunk := range ch {
			if chunk.Error != nil {
				// 流中错误也检测 PTL
				if isPromptTooLongError(chunk.Error) && attempt < maxPTLRetries {
					truncated := cm.truncateHeadForPTLRetry(currentMessages, 1)
					if len(truncated) == len(currentMessages) {
						return "", chunk.Error
					}
					currentMessages = truncated
					historyText.Reset()
					for _, m := range currentMessages {
						historyText.WriteString(fmt.Sprintf("[%s]: ", m.Role))
						if content, ok := m.Content.(string); ok {
							historyText.WriteString(content)
						}
						historyText.WriteString("\n")
					}
					prompt = fmt.Sprintf(`请将以下对话历史压缩为简洁摘要，保留:
1. 用户的核心目标和需求
2. 已完成的关键操作和结果
3. 待解决的问题和下一步
4. 关键文件路径、变量名、状态
删除冗余的工具输出细节和重复内容。摘要应控制在500字以内。
%s
对话历史:
%s`, historyPrompt, historyText.String())
					goto retry
				}
				return "", chunk.Error
			}
			if chunk.Text != "" {
				result += chunk.Text
			}
		}
		return result, nil

	retry:
	}
	return "", fmt.Errorf("compact failed after %d PTL retries", maxPTLRetries)
}

// truncateHeadForPTLRetry 丢弃最旧的 N 组消息 (一个 API round)
// 一个 round = user + assistant + (tool)*, 保留 system 消息
// 参考 cc-haha compactConversation: 按 API-round 分组丢弃
func (cm *ContextManager) truncateHeadForPTLRetry(messages []llm.Message, rounds int) []llm.Message {
	if rounds <= 0 || len(messages) == 0 {
		return messages
	}

	// 分离 system 和其他消息
	var systemMsgs, otherMsgs []llm.Message
	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}

	// 计算要跳过的消息数 (每个 round 从 user 开始到下一个 user 前)
	skip := 0
	roundsFound := 0
	for i, m := range otherMsgs {
		if m.Role == "user" {
			roundsFound++
			if roundsFound > rounds {
				break
			}
		}
		skip = i + 1
	}

	if skip >= len(otherMsgs) {
		// 全部被跳过, 无法再截断
		return messages
	}

	result := make([]llm.Message, 0, len(systemMsgs)+len(otherMsgs)-skip)
	result = append(result, systemMsgs...)
	result = append(result, otherMsgs[skip:]...)
	return result
}

// isPromptTooLongError 检测是否为 prompt-too-long 错误
// 参考 cc-haha: 检测 "prompt is too long" / "context length" / HTTP 413
func isPromptTooLongError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "prompt is too long") {
		return true
	}
	if strings.Contains(msg, "context length") {
		return true
	}
	if strings.Contains(msg, "maximum context") {
		return true
	}
	if strings.Contains(msg, "too long") && strings.Contains(msg, "token") {
		return true
	}
	if strings.Contains(msg, "http 413") {
		return true
	}
	return false
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
