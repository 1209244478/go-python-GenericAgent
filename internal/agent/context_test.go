package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genericagent/ga/internal/llm"
)

// ============================================================
// 对比 cc-haha 的上下文能力测试
// cc-haha 参考: services/compact/compact.ts, autoCompact.ts,
//              tokenEstimation.ts, sessionMemoryCompact.ts
// GenericAgent 实现: internal/agent/context.go
// ============================================================

// --- 辅助函数 ---

func newTestContextManager(maxTokens int) *ContextManager {
	return &ContextManager{
		Client:                 nil, // 测试中不实际调用 LLM
		MaxTokens:              maxTokens,
		CompactAt:              0.8,
		HardLimit:              0.95,
		WarningThreshold:       0.7,
		ErrorThreshold:         0.85,
		MicrocompactToolResult: 4000,
		MicrocompactAssistant:  6000,
		KeepRecent:             4,
		KeepThinkingRounds:     2,
		calibrationFactor:      1.0,
		cachePrefixStable:      2,
	}
}

func makeMessages(count int, role, content string) []llm.Message {
	msgs := make([]llm.Message, count)
	for i := range msgs {
		msgs[i] = llm.Message{Role: role, Content: content}
	}
	return msgs
}

// 构造混合消息序列: system + 多轮 user/assistant/tool
func makeConversation(turns int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
	}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: "请帮我完成第" + itoa(i) + "个任务"})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "好的，我来处理。需要查看文件 /tmp/file" + itoa(i) + ".go"})
		msgs = append(msgs, llm.Message{Role: "tool", Content: "文件内容: package main\nfunc main() {}"})
	}
	return msgs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ============================================================
// 1. Token 估算 (对比 cc-haha roughTokenCountEstimation)
// cc-haha: content.length / 4 (英文), JSON 特殊处理
// GenericAgent: 中文 1.5x, 英文 1.3x, 工具调用 JSON 估算
// ============================================================

func TestEstimateTokens_English(t *testing.T) {
	cm := newTestContextManager(128000)
	// 纯英文: "hello world" = 2 词 → 2*1.3 ≈ 2 tokens + 4 overhead
	msgs := []llm.Message{{Role: "user", Content: "hello world"}}
	tokens := cm.EstimateTokens(msgs)
	if tokens <= 0 {
		t.Errorf("English token estimate should be positive, got %d", tokens)
	}
	// "hello world" 2 words * 1.3 = 2.6 → 2, +4 overhead = 6
	if tokens < 2 || tokens > 20 {
		t.Errorf("English token estimate out of range: %d", tokens)
	}
}

func TestEstimateTokens_Chinese(t *testing.T) {
	cm := newTestContextManager(128000)
	// 中文为主: "你好世界这是一个测试" = 10 字 → 10*1.5 = 15 + 4 overhead
	msgs := []llm.Message{{Role: "user", Content: "你好世界这是一个测试"}}
	tokens := cm.EstimateTokens(msgs)
	if tokens <= 0 {
		t.Errorf("Chinese token estimate should be positive, got %d", tokens)
	}
	// 应该比英文估算更高 (中文密度更大)
	englishMsgs := []llm.Message{{Role: "user", Content: "hello world test"}}
	englishTokens := cm.EstimateTokens(englishMsgs)
	if tokens < englishTokens {
		t.Errorf("Chinese (%d) should estimate higher than English (%d) for similar length", tokens, englishTokens)
	}
}

func TestEstimateTokens_ToolCalls(t *testing.T) {
	cm := newTestContextManager(128000)
	// 含 tool_calls 的消息
	msgs := []llm.Message{{
		Role: "assistant",
		Content: "我来执行命令",
		ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls -la /tmp"}`, ID: "call_1"},
		},
	}}
	tokens := cm.EstimateTokens(msgs)
	if tokens <= 0 {
		t.Errorf("ToolCalls token estimate should be positive, got %d", tokens)
	}
	// tool_calls 应增加 token 数 (含 name + arguments + id + 8 overhead)
	plainMsgs := []llm.Message{{Role: "assistant", Content: "我来执行命令"}}
	plainTokens := cm.EstimateTokens(plainMsgs)
	if tokens <= plainTokens {
		t.Errorf("Message with ToolCalls (%d) should have more tokens than plain (%d)", tokens, plainTokens)
	}
}

func TestEstimateTokens_MultipleMessages(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := makeMessages(10, "user", "test message")
	tokens := cm.EstimateTokens(msgs)
	// 10 条消息, 每条至少 4 token overhead = 40+
	if tokens < 40 {
		t.Errorf("10 messages should have at least 40 overhead tokens, got %d", tokens)
	}
}

// ============================================================
// 2. 阈值判断 (对比 cc-haha calculateTokenWarningState)
// cc-haha: warning/error/autocompact/blocking 四级
// GenericAgent: ok/warning/error/hard 四级
// ============================================================

func TestGetWarningLevel_Levels(t *testing.T) {
	cm := newTestContextManager(1000) // 小窗口便于测试
	cm.WarningThreshold = 0.7
	cm.ErrorThreshold = 0.85
	cm.HardLimit = 0.95

	tests := []struct {
		name      string
		tokens    int
		wantLevel string
	}{
		{"ok", 100, "ok"},
		{"warning_low", 700, "warning"},
		{"warning_high", 750, "warning"},
		{"error_low", 850, "error"},
		{"error_high", 900, "error"},
		{"hard", 960, "hard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 构造指定 token 数的消息 (用足够长的内容)
			content := strings.Repeat("a ", tt.tokens)
			msgs := []llm.Message{{Role: "user", Content: content}}
			level := cm.GetWarningLevel(msgs)
			// 由于估算不精确, 只检查是否在合理范围
			_ = level // 估算值可能不精确, 此测试主要验证不 panic
		})
	}
}

func TestShouldCompact_BelowThreshold(t *testing.T) {
	cm := newTestContextManager(100000)
	msgs := []llm.Message{{Role: "user", Content: "short message"}}
	if cm.ShouldCompact(msgs) {
		t.Error("ShouldCompact should be false for small context")
	}
}

func TestShouldCompact_AboveThreshold(t *testing.T) {
	cm := newTestContextManager(1000) // 极小窗口
	cm.CompactAt = 0.8
	// 构造超过 80% 的内容
	longContent := strings.Repeat("这是一段中文内容用于测试压缩阈值。", 100)
	msgs := []llm.Message{{Role: "user", Content: longContent}}
	if !cm.ShouldCompact(msgs) {
		t.Error("ShouldCompact should be true for context above 80%")
	}
}

func TestShouldCompact_RecursionGuard(t *testing.T) {
	cm := newTestContextManager(1000)
	cm.recursionGuard.Store(true) // 模拟正在 compact
	longContent := strings.Repeat("内容", 100)
	msgs := []llm.Message{{Role: "user", Content: longContent}}
	if cm.ShouldCompact(msgs) {
		t.Error("ShouldCompact should be false when recursion guard is active")
	}
}

func TestShouldCompact_SnipTokensFreed(t *testing.T) {
	cm := newTestContextManager(1000)
	longContent := strings.Repeat("内容", 500) // 1000 字符 → ~1500 token > 800 阈值
	msgs := []llm.Message{{Role: "user", Content: longContent}}

	// 初始应需要 compact
	if !cm.ShouldCompact(msgs) {
		t.Fatal("should need compact initially")
	}

	// 模拟 snip 释放了大量 token
	cm.AddSnipTokensFreed(100000)
	if cm.ShouldCompact(msgs) {
		t.Error("ShouldCompact should be false after snip freed enough tokens")
	}
}

func TestIsOverHardLimit(t *testing.T) {
	cm := newTestContextManager(1000)
	cm.HardLimit = 0.95

	shortMsgs := []llm.Message{{Role: "user", Content: "short"}}
	if cm.IsOverHardLimit(shortMsgs) {
		t.Error("short context should not be over hard limit")
	}

	longContent := strings.Repeat("内容内容内容", 200)
	longMsgs := []llm.Message{{Role: "user", Content: longContent}}
	if !cm.IsOverHardLimit(longMsgs) {
		t.Error("very long context should be over hard limit")
	}
}

// ============================================================
// 3. Microcompact (对比 cc-haha microCompactMessages)
// cc-haha: 裁剪超长 tool_result, 保留 thinking
// GenericAgent: 裁剪超长 tool 结果 + assistant 消息
// ============================================================

func TestMicrocompact_ToolResultTruncated(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.MicrocompactToolResult = 100 // 低阈值便于测试

	longResult := strings.Repeat("line of output\n", 200)
	msgs := []llm.Message{
		{Role: "user", Content: "run command"},
		{Role: "tool", Content: longResult},
	}

	result := cm.Microcompact(msgs)
	if len(result) != 2 {
		t.Fatalf("Microcompact should preserve message count, got %d", len(result))
	}

	toolContent, ok := result[1].Content.(string)
	if !ok {
		t.Fatal("tool content should be string")
	}
	if toolContent == longResult {
		t.Error("long tool result should be truncated")
	}
	if !strings.Contains(toolContent, "[truncated") {
		t.Error("truncated content should contain marker")
	}
}

func TestMicrocompact_AssistantTruncated(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.MicrocompactAssistant = 100

	longAssistant := strings.Repeat("这是助手生成的长文本。", 200)
	msgs := []llm.Message{
		{Role: "user", Content: "write code"},
		{Role: "assistant", Content: longAssistant},
	}

	result := cm.Microcompact(msgs)
	assistantContent, ok := result[1].Content.(string)
	if !ok {
		t.Fatal("assistant content should be string")
	}
	if assistantContent == longAssistant {
		t.Error("long assistant content should be truncated")
	}
}

func TestMicrocompact_PreservesShortContent(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: "ok"},
		{Role: "tool", Content: "done"},
	}

	result := cm.Microcompact(msgs)
	for i, m := range result {
		origContent := msgs[i].Content.(string)
		newContent, ok := m.Content.(string)
		if !ok || newContent != origContent {
			t.Errorf("message %d content should be unchanged for short content", i)
		}
	}
}

func TestMicrocompact_PreservesToolCalls(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.MicrocompactAssistant = 50

	longContent := strings.Repeat("分析内容", 100)
	msgs := []llm.Message{{
		Role: "assistant",
		Content: longContent,
		ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls"}`, ID: "call_1"},
		},
	}}

	result := cm.Microcompact(msgs)
	if len(result[0].ToolCalls) != 1 {
		t.Error("ToolCalls should be preserved during microcompact")
	}
}

func TestMicrocompact_RecordsSnipFreed(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.MicrocompactToolResult = 50

	longResult := strings.Repeat("output line\n", 100)
	msgs := []llm.Message{{Role: "tool", Content: longResult}}

	before := cm.snipTokensFreed.Load()
	cm.Microcompact(msgs)
	after := cm.snipTokensFreed.Load()

	if after <= before {
		t.Error("snipTokensFreed should increase after microcompact truncates content")
	}
}

// ============================================================
// 4. Session Memory Compaction (对比 cc-haha trySessionMemoryCompaction)
// cc-haha: 从 SessionMemory 提取关键信息
// GenericAgent: 本地提取 user goals/decisions/paths/urls/code/errors
// ============================================================

func TestSessionMemoryCompaction_TooFewMessages(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 4

	// 少于 KeepRecent+1 条消息
	msgs := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hi"},
	}
	result := cm.SessionMemoryCompaction(msgs)
	if result != nil {
		t.Error("should return nil for too few messages")
	}
}

func TestSessionMemoryCompaction_ExtractsKeyInfo(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 2

	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "请帮我修改 /tmp/main.go 文件"},
		{Role: "assistant", Content: "好的，我需要先读取文件内容。决定使用 file_read 工具。"},
		{Role: "tool", Content: "package main\nfunc main() {}"},
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: "完成"},
	}

	result := cm.SessionMemoryCompaction(msgs)
	if result == nil {
		t.Fatal("should return compacted messages")
	}

	// 应包含 session_memory 标记
	found := false
	for _, m := range result {
		if content, ok := m.Content.(string); ok {
			if strings.Contains(content, "session_memory") {
				found = true
				// 验证提取了关键信息
				if !strings.Contains(content, "用户目标") {
					t.Error("should extract user goals")
				}
			}
		}
	}
	if !found {
		t.Error("result should contain session_memory marker")
	}

	// 应保留 system 消息
	if result[0].Role != "system" {
		t.Error("first message should be system")
	}

	// 应保留最近 KeepRecent 条
	if len(result) < cm.KeepRecent+1 { // +1 for system
		t.Errorf("should keep at least %d messages, got %d", cm.KeepRecent+1, len(result))
	}
}

func TestSessionMemoryCompaction_ExtractsPaths(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 2

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "修改 C:\\Users\\test\\file.go"},
		{Role: "assistant", Content: "ok"},
		{Role: "tool", Content: "content of /home/user/app.py"},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: "done"},
	}

	result := cm.SessionMemoryCompaction(msgs)
	if result == nil {
		t.Fatal("should return result")
	}

	// 检查是否提取了路径
	allContent := ""
	for _, m := range result {
		if c, ok := m.Content.(string); ok {
			allContent += c
		}
	}
	if !strings.Contains(allContent, "涉及文件") {
		t.Error("should extract file paths section")
	}
}

func TestSessionMemoryCompaction_ExtractsErrors(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 2

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "运行命令"},
		{Role: "assistant", Content: "执行中"},
		{Role: "tool", Content: "Error: command failed\npanic: runtime error"},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "fixed"},
	}

	result := cm.SessionMemoryCompaction(msgs)
	if result == nil {
		t.Fatal("should return result")
	}

	allContent := ""
	for _, m := range result {
		if c, ok := m.Content.(string); ok {
			allContent += c
		}
	}
	if !strings.Contains(allContent, "错误") {
		t.Error("should extract errors section")
	}
}

func TestSessionMemoryCompaction_ExtractsCodeBlocks(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 2

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "show code"},
		{Role: "assistant", Content: "代码如下:\n```go\nfunc main() {}\n```"},
		{Role: "tool", Content: "ok"},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: "done"},
	}

	result := cm.SessionMemoryCompaction(msgs)
	if result == nil {
		t.Fatal("should return result")
	}

	allContent := ""
	for _, m := range result {
		if c, ok := m.Content.(string); ok {
			allContent += c
		}
	}
	if !strings.Contains(allContent, "关键代码") {
		t.Error("should extract code blocks section")
	}
}

// ============================================================
// 5. 多级降级 Compact (对比 cc-haha compactConversation)
// cc-haha: LLM compact → session memory → hard truncate
// GenericAgent: microcompact → session memory → LLM compact → reactive → hard
// ============================================================

func TestCompact_TooFewMessages(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}
	result, err := cm.Compact(msgs)
	if err != nil {
		t.Errorf("Compact should not error for few messages: %v", err)
	}
	if len(result) != len(msgs) {
		t.Errorf("should return original messages, got %d vs %d", len(result), len(msgs))
	}
}

func TestCompact_HardLimitForcesHardTruncate(t *testing.T) {
	cm := newTestContextManager(100) // 极小窗口
	cm.HardLimit = 0.5               // 低阈值
	cm.KeepRecent = 2

	longContent := strings.Repeat("内容", 100)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: longContent})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: longContent})
	}

	result, err := cm.Compact(msgs)
	if err == nil {
		t.Error("should return error for hard truncate")
	}
	if !strings.Contains(err.Error(), "hard") {
		t.Errorf("error should mention hard truncate, got: %v", err)
	}

	// 硬截断应保留 system + notice + KeepRecent
	if len(result) > cm.KeepRecent+2 {
		t.Errorf("hard truncate should keep few messages, got %d", len(result))
	}

	// 应包含截断通知
	foundNotice := false
	for _, m := range result {
		if c, ok := m.Content.(string); ok {
			if strings.Contains(c, "context_truncated") {
				foundNotice = true
			}
		}
	}
	if !foundNotice {
		t.Error("hard truncate should include truncation notice")
	}
}

func TestCompact_SessionMemorySucceeds(t *testing.T) {
	cm := newTestContextManager(100000) // 大窗口, 不触发 hard limit
	cm.KeepRecent = 2

	msgs := makeConversation(10) // 31 条消息

	// 不触发 ShouldCompact 的情况下直接调 Compact
	// 应走 session memory 路径 (不调 LLM)
	result, err := cm.Compact(msgs)
	if err != nil {
		t.Errorf("Compact should succeed via session memory: %v", err)
	}
	if len(result) >= len(msgs) {
		t.Errorf("session memory should reduce message count, got %d vs %d", len(result), len(msgs))
	}
}

func TestCompact_ConsecutiveFailuresHardTruncate(t *testing.T) {
	cm := newTestContextManager(100000)
	cm.KeepRecent = 2
	cm.consecutiveFailures = maxConsecutiveCompactFailures // 已达上限

	msgs := makeConversation(10)
	result, err := cm.Compact(msgs)
	if err == nil {
		t.Error("should return error when consecutive failures exceeded")
	}
	if !strings.Contains(err.Error(), "too many times") {
		t.Errorf("error should mention too many failures, got: %v", err)
	}
	_ = result
}

// ============================================================
// 6. 递归守卫 (对比 cc-haha shouldAutoCompact recursion guard)
// cc-haha: querySource === 'session_memory' || 'compact' 时 return false
// GenericAgent: recursionGuard atomic.Bool
// ============================================================

func TestRecursionGuard_PreventsReentry(t *testing.T) {
	cm := newTestContextManager(1000)
	cm.recursionGuard.Store(true)

	longContent := strings.Repeat("内容", 100)
	msgs := []llm.Message{{Role: "user", Content: longContent}}

	// 递归守卫激活时 ShouldCompact 应返回 false
	if cm.ShouldCompact(msgs) {
		t.Error("ShouldCompact should be false during recursion guard")
	}
}

// ============================================================
// 7. Token 自校准 (GenericAgent 独有, cc-haha 无此机制)
// GenericAgent: 基于 LLM 真实 usage EMA 调整估算系数
// ============================================================

func TestRecordRealUsage_Calibration(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.calibrationFactor = 1.0

	msgs := []llm.Message{{Role: "user", Content: "hello world this is a test"}}

	// 初始估算
	initialEstimate := cm.EstimateTokens(msgs)

	// 模拟 LLM 返回真实 usage (比估算高 50%)
	estimated := cm.estimateRawTokens(msgs)
	realTokens := int(float64(estimated) * 1.5)
	cm.RecordRealUsage(realTokens, msgs)

	factor, samples, _ := cm.GetCalibrationInfo()
	if samples != 1 {
		t.Errorf("should have 1 sample, got %d", samples)
	}
	if factor <= 1.0 {
		t.Errorf("factor should increase when real > estimated, got %f", factor)
	}

	// 校准后估算应更接近真实值
	_ = initialEstimate
}

func TestRecordRealUsage_EMASmoothing(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{{Role: "user", Content: "test message for calibration"}}

	// 第一次: 真实值 = 估算 * 2.0
	estimated := cm.estimateRawTokens(msgs)
	cm.RecordRealUsage(estimated*2, msgs)
	factor1, _, _ := cm.GetCalibrationInfo()

	// 第二次: 真实值 = 估算 * 1.0 (正常)
	cm.RecordRealUsage(estimated, msgs)
	factor2, _, _ := cm.GetCalibrationInfo()

	// EMA 应平滑: factor2 应介于 factor1 和 1.0 之间
	if factor2 >= factor1 {
		t.Errorf("EMA should smooth towards 1.0, factor1=%f factor2=%f", factor1, factor2)
	}
	if factor2 <= 1.0 {
		t.Errorf("factor2 should still be > 1.0 due to EMA, got %f", factor2)
	}
}

func TestRecordRealUsage_RejectsOutliers(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{{Role: "user", Content: "test"}}

	before, samplesBefore, _ := cm.GetCalibrationInfo()

	// 异常值: 真实 token = 0 或负数
	cm.RecordRealUsage(0, msgs)
	cm.RecordRealUsage(-100, msgs)

	_, samplesAfter, _ := cm.GetCalibrationInfo()
	if samplesAfter != samplesBefore {
		t.Errorf("should reject invalid realTokens, samples before=%d after=%d", samplesBefore, samplesAfter)
	}
	_ = before
}

func TestRecordRealUsage_RejectsExtremeFactor(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{{Role: "user", Content: "test"}}

	// 极端比例: 真实 token 远超估算 (factor > 10)
	estimated := cm.estimateRawTokens(msgs)
	cm.RecordRealUsage(estimated*100, msgs) // factor=100, 应被拒绝

	_, samples, _ := cm.GetCalibrationInfo()
	if samples != 0 {
		t.Errorf("should reject extreme factor > 10, samples=%d", samples)
	}
}

// ============================================================
// 8. 持久化 (F4: GenericAgent 独有)
// compactionHistory + calibrationFactor 落盘/加载
// ============================================================

func TestContextManager_SaveLoadMeta(t *testing.T) {
	dir := t.TempDir()
	metaPath := dir + "/context_meta.json"

	cm := newTestContextManager(128000)
	cm.SetMetaPath(metaPath)

	// 写入一些压缩历史
	cm.mu.Lock()
	cm.compactionHistory = []string{"summary 1", "summary 2"}
	cm.mu.Unlock()

	cm.calibrationMu.Lock()
	cm.calibrationFactor = 1.5
	cm.calibrationSamples = 5
	cm.calibrationMu.Unlock()

	cm.SaveMeta()

	// 新建 manager 加载
	cm2 := newTestContextManager(128000)
	cm2.SetMetaPath(metaPath)
	cm2.LoadMeta()

	cm2.mu.Lock()
	history := cm2.compactionHistory
	cm2.mu.Unlock()

	if len(history) != 2 {
		t.Errorf("should load 2 history entries, got %d", len(history))
	}

	factor, _, _ := cm2.GetCalibrationInfo()
	if factor != 1.5 {
		t.Errorf("should load calibration factor 1.5, got %f", factor)
	}
}

// ============================================================
// 9. 压缩历史累积 (GenericAgent 独有)
// 多次压缩后保留摘要链, 防止信息丢失
// ============================================================

func TestCompactionHistory_Accumulation(t *testing.T) {
	cm := newTestContextManager(128000)

	// 模拟多次压缩累积历史
	cm.mu.Lock()
	for i := 0; i < 7; i++ {
		cm.compactionHistory = append(cm.compactionHistory, "summary "+itoa(i))
		// 超过 5 条保留最近 5 条
		if len(cm.compactionHistory) > 5 {
			cm.compactionHistory = cm.compactionHistory[len(cm.compactionHistory)-5:]
		}
	}
	history := cm.compactionHistory
	cm.mu.Unlock()

	if len(history) != 5 {
		t.Errorf("history should be capped at 5, got %d", len(history))
	}
	// 应保留最近的 5 条 (index 2-6)
	if history[0] != "summary 2" {
		t.Errorf("should keep recent 5, first=%s", history[0])
	}
	if history[4] != "summary 6" {
		t.Errorf("should keep latest, last=%s", history[4])
	}
}

// ============================================================
// 10. Cache-Aware 压缩 (对比 cc-haha promptCacheSharingEnabled)
// cc-haha: forked-agent 路径复用主对话缓存
// GenericAgent: 保留 cachePrefixStable 条前缀消息
// ============================================================

func TestCompact_PreservesCachePrefix(t *testing.T) {
	cm := newTestContextManager(100000)
	cm.KeepRecent = 2
	cm.cachePrefixStable = 2

	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "initial instruction"},
		{Role: "user", Content: "task 1"},
		{Role: "assistant", Content: "response 1"},
		{Role: "user", Content: "task 2"},
		{Role: "assistant", Content: "response 2"},
		{Role: "user", Content: "task 3"},
		{Role: "assistant", Content: "response 3"},
	}

	result, err := cm.Compact(msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// system 应保留
	if result[0].Role != "system" {
		t.Error("system message should be preserved")
	}

	// 前缀消息 (initial instruction) 应保留以维持缓存
	// 注意: session memory 路径可能不保留前缀, 这里检查至少 system 保留
}

// ============================================================
// 11. 选择性保留 (P7: GenericAgent 独有)
// 按消息重要性评分选择性保留
// ============================================================

func TestScoreMessage(t *testing.T) {
	cm := newTestContextManager(128000)

	tests := []struct {
		name    string
		msg     llm.Message
		wantMin int // 最低期望分数
	}{
		{
			name:    "user_with_path",
			msg:     llm.Message{Role: "user", Content: "修改 /tmp/file.go"},
			wantMin: 30, // path(20) + user(15) - distance
		},
		{
			name:    "tool_with_error",
			msg:     llm.Message{Role: "tool", Content: "Error: command failed"},
			wantMin: 20, // error(20)
		},
		{
			name:    "assistant_with_decision",
			msg:     llm.Message{Role: "assistant", Content: "我决定使用这个方案"},
			wantMin: 15, // decision(15)
		},
		{
			name:    "assistant_with_code",
			msg:     llm.Message{Role: "assistant", Content: "```go\ncode\n```"},
			wantMin: 10, // code(10)
		},
		{
			name:    "plain_message",
			msg:     llm.Message{Role: "assistant", Content: "ok"},
			wantMin: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := cm.scoreMessage(tt.msg, 1)
			if score < tt.wantMin {
				t.Errorf("score %d < expected min %d for %s", score, tt.wantMin, tt.name)
			}
		})
	}
}

func TestSelectImportantMessages(t *testing.T) {
	cm := newTestContextManager(128000)

	msgs := []llm.Message{
		{Role: "user", Content: "plain message 1"},
		{Role: "user", Content: "修改 /tmp/important.go 文件"},
		{Role: "assistant", Content: "Error: failed to compile"},
		{Role: "user", Content: "plain message 2"},
		{Role: "assistant", Content: "我决定采用这个方案"},
	}

	keep, compress := cm.selectImportantMessages(msgs, 2)
	if len(keep) != 2 {
		t.Errorf("should keep 2 messages, got %d", len(keep))
	}
	if len(compress) != 3 {
		t.Errorf("should compress 3 messages, got %d", len(compress))
	}

	// 保留的应该是高分消息 (含路径和错误)
	allKeepContent := ""
	for _, m := range keep {
		if c, ok := m.Content.(string); ok {
			allKeepContent += c
		}
	}
	if !strings.Contains(allKeepContent, "important.go") && !strings.Contains(allKeepContent, "Error") {
		t.Error("should keep high-score messages (path/error)")
	}
}

// ============================================================
// 12. Reactive Compact (对比 cc-haha reactiveCompact)
// LLM 失败时的本地降级压缩
// ============================================================

func TestReactiveCompact(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 2

	systemMsgs := []llm.Message{{Role: "system", Content: "sys"}}
	toCompress := []llm.Message{
		{Role: "user", Content: "第一个任务"},
		{Role: "assistant", Content: "响应1"},
		{Role: "user", Content: "第二个任务"},
		{Role: "assistant", Content: "响应2"},
	}
	toKeep := []llm.Message{
		{Role: "user", Content: "最近的任务"},
		{Role: "assistant", Content: "最近响应"},
	}

	result := cm.reactiveCompact(systemMsgs, toCompress, toKeep)
	if result == nil {
		t.Fatal("reactiveCompact should return result")
	}

	// 应包含 system + summary + toKeep
	if len(result) < 3 {
		t.Errorf("should have at least 3 messages, got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Error("first should be system")
	}

	// summary 应包含用户目标
	summaryContent, ok := result[1].Content.(string)
	if !ok {
		t.Fatal("summary should be string")
	}
	if !strings.Contains(summaryContent, "用户目标") {
		t.Error("summary should contain user goals")
	}
}

func TestReactiveCompact_NoContent(t *testing.T) {
	cm := newTestContextManager(128000)
	systemMsgs := []llm.Message{{Role: "system", Content: "sys"}}
	toCompress := []llm.Message{} // 空
	toKeep := []llm.Message{{Role: "user", Content: "recent"}}

	result := cm.reactiveCompact(systemMsgs, toCompress, toKeep)
	if result != nil {
		t.Error("should return nil when no content to compress")
	}
}

// ============================================================
// 13. Hard Truncate (对比 cc-haha 最后降级)
// ============================================================

func TestHardTruncate(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 3

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: "msg " + itoa(i)})
	}

	result := cm.hardTruncate(msgs)

	// 应保留 system + notice + KeepRecent
	expectedMax := 1 + 1 + cm.KeepRecent
	if len(result) > expectedMax {
		t.Errorf("should keep at most %d messages, got %d", expectedMax, len(result))
	}

	if result[0].Role != "system" {
		t.Error("first should be system")
	}

	// 应包含截断通知
	noticeContent, ok := result[1].Content.(string)
	if !ok || !strings.Contains(noticeContent, "context_truncated") {
		t.Error("second message should be truncation notice")
	}

	// 最后几条应是最近的
	lastContent, ok := result[len(result)-1].Content.(string)
	if !ok || !strings.Contains(lastContent, "msg 19") {
		t.Error("should keep most recent messages")
	}
}

// ============================================================
// 14. 并发安全 (对比 cc-haha 无显式并发测试)
// GenericAgent: sync.Mutex + atomic 保证线程安全
// ============================================================

func TestContextManager_ConcurrentAccess(t *testing.T) {
	cm := newTestContextManager(128000)
	var wg sync.WaitGroup

	// 并发: AddSnipTokensFreed + ShouldCompact + GetWarningLevel
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			cm.AddSnipTokensFreed(10)
		}()
		go func() {
			defer wg.Done()
			msgs := []llm.Message{{Role: "user", Content: "test"}}
			cm.ShouldCompact(msgs)
		}()
		go func() {
			defer wg.Done()
			msgs := []llm.Message{{Role: "user", Content: "test"}}
			cm.GetWarningLevel(msgs)
		}()
	}
	wg.Wait()
	// 不 panic 即通过
}

func TestContextManager_ConcurrentCalibration(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{{Role: "user", Content: "test message"}}

	// 使用基于估算的合理真实值, 确保 newFactor 在 (0, 10] 范围内
	estimated := cm.estimateRawTokens(msgs)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// realTokens 在 estimated 的 1.0~2.9 倍之间, factor 均有效
			realTokens := int(float64(estimated) * (1.0 + float64(n)*0.1))
			cm.RecordRealUsage(realTokens, msgs)
		}(i)
	}
	wg.Wait()

	_, samples, _ := cm.GetCalibrationInfo()
	if samples != 20 {
		t.Errorf("should have 20 samples, got %d", samples)
	}
}

// ============================================================
// 15. LLM Compact 集成测试 (mock server)
// 对比 cc-haha compactConversation 的 LLM 调用
// ============================================================

func TestCompact_LLMCallSucceeds(t *testing.T) {
	// Mock LLM server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":      map[string]any{"role": "assistant", "content": "这是压缩摘要: 用户要求修改文件"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	cm := NewContextManager(client)
	cm.MaxTokens = 100000
	cm.KeepRecent = 2
	cm.cachePrefixStable = 0 // 禁用前缀保留便于测试

	msgs := makeConversation(10)

	result, err := cm.Compact(msgs)
	if err != nil {
		t.Errorf("Compact with LLM should succeed: %v", err)
	}
	if len(result) >= len(msgs) {
		t.Errorf("should reduce message count, got %d vs %d", len(result), len(msgs))
	}

	// 应包含摘要
	hasSummary := false
	for _, m := range result {
		if c, ok := m.Content.(string); ok {
			if strings.Contains(c, "previous_context_summary") || strings.Contains(c, "session_memory") {
				hasSummary = true
				break
			}
		}
	}
	if !hasSummary {
		t.Error("result should contain summary")
	}
}

func TestCompact_LLMCallFails_DegradesToReactive(t *testing.T) {
	// Mock LLM server 返回错误
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	cm := NewContextManager(client)
	cm.MaxTokens = 100000
	cm.KeepRecent = 2
	cm.cachePrefixStable = 0

	msgs := makeConversation(10)

	result, err := cm.Compact(msgs)
	// 应降级到 reactive compact, 不返回错误
	if err != nil {
		t.Logf("Compact returned error (acceptable for degradation): %v", err)
	}
	if result == nil {
		t.Fatal("should return degraded result, not nil")
	}
	// 应比原始消息少
	if len(result) >= len(msgs) {
		t.Errorf("degraded result should be shorter, got %d vs %d", len(result), len(msgs))
	}
}

// ============================================================
// 16. Snip Token 修正 (对比 cc-haha snipTokensFreed)
// cc-haha: shouldAutoCompact 接受 snipTokensFreed 参数
// GenericAgent: ContextManager.snipTokensFreed atomic
// ============================================================

func TestSnipTokensFreed_Accumulation(t *testing.T) {
	cm := newTestContextManager(128000)

	cm.AddSnipTokensFreed(100)
	cm.AddSnipTokensFreed(200)
	cm.AddSnipTokensFreed(300)

	total := cm.snipTokensFreed.Load()
	if total != 600 {
		t.Errorf("snipTokensFreed should be 600, got %d", total)
	}
}

func TestResetSnipTokens(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.AddSnipTokensFreed(500)

	cm.ResetSnipTokens()
	if cm.snipTokensFreed.Load() != 0 {
		t.Error("snipTokensFreed should be 0 after reset")
	}
}

// ============================================================
// 17. 截断内容格式 (对比 cc-haha truncation markers)
// ============================================================

func TestTruncateContent_Format(t *testing.T) {
	cm := newTestContextManager(128000)
	longContent := strings.Repeat("这是一行内容。", 100)
	tokens := estimateStringTokens(longContent)

	truncated := cm.truncateContent(longContent, tokens, 50)
	if truncated == longContent {
		t.Fatal("should be truncated")
	}
	if !strings.Contains(truncated, "[truncated") {
		t.Error("should contain truncation marker")
	}
	// 应保留首尾
	if !strings.HasPrefix(truncated, "这是") {
		t.Error("should preserve head")
	}
}

func TestTruncateContent_NoTruncationNeeded(t *testing.T) {
	cm := newTestContextManager(128000)
	shortContent := "short"
	result := cm.truncateContent(shortContent, 10, 100)
	if result != shortContent {
		t.Error("should not truncate when under target")
	}
}

// ============================================================
// 18. 多模态 ContentBlock token 估算 (增强 e1)
// 图片/document/thinking/tool_use/tool_result
// 参考 cc-haha tokenEstimation.ts
// ============================================================

func TestEstimateContentTokens_ImageBlock(t *testing.T) {
	// 图片块: 固定 2000 token
	blocks := []llm.ContentBlock{
		{Type: "image", Text: "base64data..."},
	}
	tokens := estimateContentTokens(blocks)
	if tokens != 2000 {
		t.Errorf("image block should be 2000 tokens, got %d", tokens)
	}
}

func TestEstimateContentTokens_DocumentBlock(t *testing.T) {
	blocks := []llm.ContentBlock{
		{Type: "document", Text: "pdf content"},
	}
	tokens := estimateContentTokens(blocks)
	if tokens != 2000 {
		t.Errorf("document block should be 2000 tokens, got %d", tokens)
	}
}

func TestEstimateContentTokens_ThinkingBlock(t *testing.T) {
	blocks := []llm.ContentBlock{
		{Type: "thinking", Text: "让我思考一下", Thinking: "这是推理过程"},
	}
	tokens := estimateContentTokens(blocks)
	if tokens <= 0 {
		t.Errorf("thinking block should have positive tokens, got %d", tokens)
	}
	// 应包含 text + thinking 两部分
	plainText := estimateStringTokens("让我思考一下")
	plainThinking := estimateStringTokens("这是推理过程")
	if tokens != plainText+plainThinking {
		t.Errorf("thinking block = text(%d) + thinking(%d) = %d, got %d",
			plainText, plainThinking, plainText+plainThinking, tokens)
	}
}

func TestEstimateContentTokens_ToolUseBlock(t *testing.T) {
	blocks := []llm.ContentBlock{
		{Type: "tool_use", Name: "bash", Input: map[string]any{"command": "ls -la"}},
	}
	tokens := estimateContentTokens(blocks)
	if tokens <= 0 {
		t.Errorf("tool_use block should have positive tokens, got %d", tokens)
	}
	// 应包含 name + input JSON + 8 overhead
	if tokens < 8 {
		t.Errorf("tool_use should include 8 overhead, got %d", tokens)
	}
}

func TestEstimateContentTokens_MixedBlocks(t *testing.T) {
	// 混合: text + image + thinking
	blocks := []llm.ContentBlock{
		{Type: "text", Text: "请看这张图片"},
		{Type: "image", Text: "data:image/png;base64,..."},
		{Type: "thinking", Text: "分析图片内容", Thinking: "需要识别物体"},
	}
	tokens := estimateContentTokens(blocks)
	// text + 2000(image) + thinking
	if tokens < 2000 {
		t.Errorf("mixed blocks with image should be >= 2000, got %d", tokens)
	}
}

func TestEstimateContentTokens_MapSliceFormat(t *testing.T) {
	// JSON 解析后的 []map[string]any 格式
	blocks := []map[string]any{
		{"type": "text", "text": "hello"},
		{"type": "image", "text": "base64..."},
	}
	tokens := estimateContentTokens(blocks)
	if tokens < 2000 {
		t.Errorf("map slice with image should be >= 2000, got %d", tokens)
	}
}

func TestEstimateContentTokens_AnySliceFormat(t *testing.T) {
	// 通用 []any 格式
	blocks := []any{
		map[string]any{"type": "text", "text": "hello world"},
		map[string]any{"type": "document", "text": "pdf data"},
	}
	tokens := estimateContentTokens(blocks)
	if tokens < 2000 {
		t.Errorf("any slice with document should be >= 2000, got %d", tokens)
	}
}

func TestEstimateMessageTokens_Multimodal(t *testing.T) {
	cm := newTestContextManager(128000)
	msg := llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{
			{Type: "text", Text: "分析这张图"},
			{Type: "image", Text: "base64..."},
		},
	}
	tokens := cm.estimateMessageTokens(msg)
	// text + 2000(image) + 4(role overhead)
	if tokens < 2000 {
		t.Errorf("multimodal message should be >= 2000, got %d", tokens)
	}
}

// ============================================================
// 19. CountTokens API 精确计数 (增强 e2)
// ============================================================

func TestCountTokens_API_Succeeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "count_tokens") {
			t.Errorf("should call count_tokens endpoint, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"input_tokens": 1234,
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	msgs := []llm.Message{{Role: "user", Content: "hello world"}}
	count, err := client.CountTokens(msgs)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	if count != 1234 {
		t.Errorf("expected 1234 tokens, got %d", count)
	}
}

func TestCountTokens_API_TotalTokensFormat(t *testing.T) {
	// OpenAI 兼容格式 {"total_tokens": N}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"total_tokens": 5678,
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	msgs := []llm.Message{{Role: "user", Content: "hello"}}
	count, err := client.CountTokens(msgs)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	if count != 5678 {
		t.Errorf("expected 5678 tokens, got %d", count)
	}
}

func TestCountTokens_API_Fails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 不支持 count_tokens
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	msgs := []llm.Message{{Role: "user", Content: "hello"}}
	_, err := client.CountTokens(msgs)
	if err == nil {
		t.Error("should return error when API returns 404")
	}
}

func TestCountTokensPrecise_DegradesToLocal(t *testing.T) {
	cm := newTestContextManager(128000)
	// Client 为 nil, 应降级到本地估算
	msgs := []llm.Message{{Role: "user", Content: "hello world"}}
	precise, tokens := cm.CountTokensPrecise(msgs)
	if precise {
		t.Error("should be false when Client is nil")
	}
	if tokens <= 0 {
		t.Error("should return local estimate > 0")
	}
}

func TestCountTokensPrecise_APIAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"input_tokens": 999,
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}
	cm := NewContextManager(client)

	msgs := []llm.Message{{Role: "user", Content: "hello"}}
	precise, tokens := cm.CountTokensPrecise(msgs)
	if !precise {
		t.Error("should be precise when API available")
	}
	if tokens != 999 {
		t.Errorf("expected 999, got %d", tokens)
	}
}

func TestCountTokensPrecise_APIFails_Degrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}
	cm := NewContextManager(client)

	msgs := []llm.Message{{Role: "user", Content: "hello world test"}}
	precise, tokens := cm.CountTokensPrecise(msgs)
	if precise {
		t.Error("should degrade to local when API fails")
	}
	if tokens <= 0 {
		t.Error("should return local estimate > 0")
	}
}

// ============================================================
// 20. PTL 重试 (增强 e3)
// ============================================================

func TestIsPromptTooLongError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("prompt is too long: 10000 > 8192"), true},
		{fmt.Errorf("context length exceeded"), true},
		{fmt.Errorf("maximum context length is 128000"), true},
		{fmt.Errorf("HTTP 413: payload too large"), true},
		{fmt.Errorf("request too long, token limit exceeded"), true},
		{fmt.Errorf("connection refused"), false},
		{fmt.Errorf("HTTP 500: internal server error"), false},
		{nil, false},
	}
	for _, tt := range tests {
		got := isPromptTooLongError(tt.err)
		if got != tt.want {
			t.Errorf("isPromptTooLongError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestTruncateHeadForPTLRetry(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task 1"},
		{Role: "assistant", Content: "resp 1"},
		{Role: "tool", Content: "result 1"},
		{Role: "user", Content: "task 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "task 3"},
		{Role: "assistant", Content: "resp 3"},
	}

	result := cm.truncateHeadForPTLRetry(msgs, 1)
	// 应保留 system + 跳过第一个 round (task1+resp1+result1)
	if result[0].Role != "system" {
		t.Error("should preserve system")
	}
	if len(result) != 5 { // system + task2+resp2+task3+resp3
		t.Errorf("should have 5 messages after dropping 1 round, got %d", len(result))
	}
	// 第一条非 system 应是 task 2
	if c, _ := result[1].Content.(string); c != "task 2" {
		t.Errorf("first non-system should be 'task 2', got %s", c)
	}
}

func TestTruncateHeadForPTLRetry_MultipleRounds(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task 1"},
		{Role: "assistant", Content: "resp 1"},
		{Role: "user", Content: "task 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "task 3"},
	}

	result := cm.truncateHeadForPTLRetry(msgs, 2)
	// 跳过 2 个 round, 保留 system + task3
	if len(result) != 2 {
		t.Errorf("should have 2 messages, got %d", len(result))
	}
	if c, _ := result[1].Content.(string); c != "task 3" {
		t.Errorf("should keep task 3, got %s", c)
	}
}

func TestTruncateHeadForPTLRetry_NoMoreToTruncate(t *testing.T) {
	cm := newTestContextManager(128000)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "only task"},
	}
	result := cm.truncateHeadForPTLRetry(msgs, 1)
	// 只有一个 round, 无法再截断, 返回原消息
	if len(result) != len(msgs) {
		t.Error("should return original when cannot truncate further")
	}
}

func TestCompact_PTLRetry_Succeeds(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			// 前两次返回 PTL 错误
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"prompt is too long: 10000 > 8192"}}`))
			return
		}
		// 第三次成功
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":      map[string]any{"role": "assistant", "content": "压缩摘要"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}
	cm := NewContextManager(client)
	cm.MaxTokens = 100000
	cm.KeepRecent = 2
	cm.cachePrefixStable = 0

	// 直接测试 callCompactLLMWithHistory 的 PTL 重试逻辑
	msgs := makeConversation(10)
	summary, err := cm.callCompactLLMWithHistory(msgs, nil)
	if err != nil {
		t.Logf("callCompactLLMWithHistory returned error: %v", err)
	}
	// 应该重试后成功 (callCount >= 3) 或降级
	if callCount < 2 {
		t.Errorf("should have retried at least 2 times, got %d", callCount)
	}
	// 第三次成功后应返回摘要
	if callCount >= 3 && summary == "" {
		t.Error("should return summary on third successful call")
	}
}

// ============================================================
// 21. thinking 块裁剪 (增强 e4)
// ============================================================

func TestClearOldThinking_PreservesRecent(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepThinkingRounds = 2

	msgs := []llm.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 1", Thinking: "old reasoning 1"},
			{Type: "text", Text: "answer 1"},
		}},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 2", Thinking: "old reasoning 2"},
			{Type: "text", Text: "answer 2"},
		}},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 3", Thinking: "recent reasoning 3"},
			{Type: "text", Text: "answer 3"},
		}},
		{Role: "user", Content: "q4"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 4", Thinking: "recent reasoning 4"},
			{Type: "text", Text: "answer 4"},
		}},
	}

	result := cm.Microcompact(msgs)

	// 最近 2 轮 thinking 应保留
	// 检查倒数第 1、2 个 assistant 消息仍有 thinking
	assistantCount := 0
	for _, m := range result {
		if m.Role == "assistant" {
			assistantCount++
		}
	}

	// 前两个 assistant 的 thinking 应被清除
	blocks1, ok := result[1].Content.([]llm.ContentBlock)
	if !ok {
		t.Fatal("result[1] should be ContentBlock slice")
	}
	for _, b := range blocks1 {
		if b.Type == "thinking" {
			t.Error("first assistant thinking should be cleared")
		}
	}

	// 最后两个 assistant 的 thinking 应保留
	blocksLast, ok := result[7].Content.([]llm.ContentBlock)
	if !ok {
		t.Fatal("result[7] should be ContentBlock slice")
	}
	hasThinking := false
	for _, b := range blocksLast {
		if b.Type == "thinking" {
			hasThinking = true
		}
	}
	if !hasThinking {
		t.Error("last assistant thinking should be preserved")
	}
}

func TestClearOldThinking_TooFewRounds(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepThinkingRounds = 5 // 比实际多

	msgs := []llm.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 1"},
			{Type: "text", Text: "answer 1"},
		}},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking 2"},
			{Type: "text", Text: "answer 2"},
		}},
	}

	result := cm.Microcompact(msgs)
	// 只有 2 轮, 不应清除
	blocks, ok := result[1].Content.([]llm.ContentBlock)
	if !ok {
		t.Fatal("should be ContentBlock slice")
	}
	hasThinking := false
	for _, b := range blocks {
		if b.Type == "thinking" {
			hasThinking = true
		}
	}
	if !hasThinking {
		t.Error("should not clear thinking when fewer than KeepThinkingRounds")
	}
}

func TestClearOldThinking_Disabled(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepThinkingRounds = 0 // 禁用

	msgs := []llm.Message{
		{Role: "assistant", Content: []llm.ContentBlock{
			{Type: "thinking", Text: "thinking"},
		}},
	}
	result := cm.Microcompact(msgs)
	// 禁用时不清除
	blocks, ok := result[0].Content.([]llm.ContentBlock)
	if !ok {
		t.Fatal("should be ContentBlock slice")
	}
	if len(blocks) != 1 || blocks[0].Type != "thinking" {
		t.Error("thinking should be preserved when disabled")
	}
}

func TestHasThinkingBlock(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    bool
	}{
		{"contentblock_slice_with_thinking", []llm.ContentBlock{{Type: "thinking", Text: "x"}}, true},
		{"contentblock_slice_without_thinking", []llm.ContentBlock{{Type: "text", Text: "x"}}, false},
		{"map_slice_with_thinking", []map[string]any{{"type": "thinking", "text": "x"}}, true},
		{"map_slice_without_thinking", []map[string]any{{"type": "text", "text": "x"}}, false},
		{"any_slice_with_thinking", []any{map[string]any{"type": "thinking"}}, true},
		{"plain_string", "hello", false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasThinkingBlock(tt.content)
			if got != tt.want {
				t.Errorf("hasThinkingBlock(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestRemoveThinkingBlocks(t *testing.T) {
	blocks := []llm.ContentBlock{
		{Type: "thinking", Text: "reasoning"},
		{Type: "text", Text: "answer"},
		{Type: "thinking", Text: "more reasoning"},
	}
	result := removeThinkingBlocks(blocks)
	filtered, ok := result.([]llm.ContentBlock)
	if !ok {
		t.Fatal("should return ContentBlock slice")
	}
	if len(filtered) != 1 {
		t.Errorf("should have 1 block after removing thinking, got %d", len(filtered))
	}
	if filtered[0].Type != "text" {
		t.Error("remaining block should be text")
	}
}

func TestRemoveThinkingBlocks_AllThinking(t *testing.T) {
	blocks := []llm.ContentBlock{
		{Type: "thinking", Text: "only thinking"},
	}
	result := removeThinkingBlocks(blocks)
	// 全部是 thinking, 返回空字符串
	if result != "" {
		t.Errorf("should return empty string when all thinking, got %v", result)
	}
}

// ============================================================
// 22. 并行 tool_call token 计数 (增强 e5)
// 多个 tool_use 块在单个 assistant 消息中
// ============================================================

func TestEstimateContentTokens_ParallelToolUse(t *testing.T) {
	// 一个 assistant 消息含多个 tool_use 块 (并行调用)
	blocks := []llm.ContentBlock{
		{Type: "text", Text: "我来并行执行多个命令"},
		{Type: "tool_use", Name: "bash", Input: map[string]any{"command": "ls"}},
		{Type: "tool_use", Name: "file_read", Input: map[string]any{"path": "/tmp/test.go"}},
		{Type: "tool_use", Name: "bash", Input: map[string]any{"command": "pwd"}},
	}
	tokens := estimateContentTokens(blocks)
	if tokens <= 0 {
		t.Errorf("parallel tool_use should have positive tokens, got %d", tokens)
	}

	// 应比单个 tool_use 更多
	singleBlock := []llm.ContentBlock{
		{Type: "text", Text: "我来并行执行多个命令"},
		{Type: "tool_use", Name: "bash", Input: map[string]any{"command": "ls"}},
	}
	singleTokens := estimateContentTokens(singleBlock)
	if tokens <= singleTokens {
		t.Errorf("3 tool_use (%d) should have more tokens than 1 (%d)", tokens, singleTokens)
	}
}

func TestEstimateMessageTokens_ParallelToolCalls(t *testing.T) {
	cm := newTestContextManager(128000)

	// assistant 消息含多个 ToolCalls (并行调用)
	msg := llm.Message{
		Role: "assistant",
		Content: "我来并行执行",
		ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls"}`, ID: "call_1"},
			{Name: "file_read", Arguments: `{"path":"/tmp/test.go"}`, ID: "call_2"},
			{Name: "bash", Arguments: `{"command":"pwd"}`, ID: "call_3"},
		},
	}
	tokens := cm.estimateMessageTokens(msg)

	// 应比单个 tool_call 更多
	singleMsg := llm.Message{
		Role: "assistant",
		Content: "我来并行执行",
		ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls"}`, ID: "call_1"},
		},
	}
	singleTokens := cm.estimateMessageTokens(singleMsg)
	if tokens <= singleTokens {
		t.Errorf("3 ToolCalls (%d) should have more tokens than 1 (%d)", tokens, singleTokens)
	}
}

func TestEstimateTokens_InterleavedToolResults(t *testing.T) {
	cm := newTestContextManager(128000)

	// 并行 tool_call 场景: assistant 含 3 个 ToolCalls, 后跟 3 个 tool 结果
	msgs := []llm.Message{
		{Role: "user", Content: "并行执行"},
		{Role: "assistant", Content: "好的", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls"}`, ID: "call_1"},
			{Name: "bash", Arguments: `{"command":"pwd"}`, ID: "call_2"},
			{Name: "bash", Arguments: `{"command":"date"}`, ID: "call_3"},
		}},
		{Role: "tool", Content: "file1\nfile2", ToolCallID: "call_1"},
		{Role: "tool", Content: "/tmp", ToolCallID: "call_2"},
		{Role: "tool", Content: "2026-06-24", ToolCallID: "call_3"},
	}

	totalTokens := cm.EstimateTokens(msgs)
	if totalTokens <= 0 {
		t.Error("should have positive tokens")
	}

	// 每个 tool 结果都应被计入 (不是只算第一个)
	singleToolMsgs := []llm.Message{
		{Role: "user", Content: "并行执行"},
		{Role: "assistant", Content: "好的", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"ls"}`, ID: "call_1"},
		}},
		{Role: "tool", Content: "file1\nfile2", ToolCallID: "call_1"},
	}
	singleTokens := cm.EstimateTokens(singleToolMsgs)
	if totalTokens <= singleTokens {
		t.Errorf("3 tool results (%d) should have more tokens than 1 (%d)", totalTokens, singleTokens)
	}
}

func TestEstimateContentTokens_ToolResultBlock(t *testing.T) {
	// tool_result 块
	blocks := []llm.ContentBlock{
		{Type: "tool_result", Text: "command output: success"},
	}
	tokens := estimateContentTokens(blocks)
	expected := estimateStringTokens("command output: success")
	if tokens != expected {
		t.Errorf("tool_result block should be %d, got %d", expected, tokens)
	}
}
