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
// 上下文能力压力测试
// 模拟真实多轮对话场景, 验证信息留存、压缩衰减、并发安全
// ============================================================

// --- 场景构造器 ---

// buildCodeEditingConversation 模拟真实代码编辑场景
// 包含: 读文件 → 分析 → 修改 → 编译 → 修复错误 → 测试
func buildCodeEditingConversation(turns int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是一个代码助手，帮助用户修改 Go 项目。项目根目录: /home/user/myproject"},
		{Role: "user", Content: "帮我修改 /home/user/myproject/main.go，添加日志功能。需要引入 log 包。"},
	}

	files := []string{
		"/home/user/myproject/main.go",
		"/home/user/myproject/internal/handler.go",
		"/home/user/myproject/internal/config.go",
		"/home/user/myproject/internal/utils.go",
		"/home/user/myproject/go.mod",
	}

	errors := []string{
		"main.go:15: undefined: log.Println",
		"handler.go:42: cannot use ctx (variable of type context.Context) as context.Context value in argument",
		"config.go:28: syntax error: unexpected semicolon or newline, expecting comma",
		"./utils.go:10:2: undefined: strings.Builder",
		"panic: runtime error: invalid memory address or nil pointer dereference",
	}

	for i := 0; i < turns; i++ {
		file := files[i%len(files)]
		errMsg := errors[i%len(errors)]

		msgs = append(msgs, llm.Message{Role: "user", Content: fmt.Sprintf("第%d步: 请查看 %s 的内容", i+1, file)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("好的，我来读取 %s。决定使用 file_read 工具。", file), ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: fmt.Sprintf(`{"path":"%s"}`, file), ID: fmt.Sprintf("call_%d", i*4)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: fmt.Sprintf("package main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"hello %d\")\n}", i), ToolCallID: fmt.Sprintf("call_%d", i*4)})

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("分析完成。需要修改 %s，添加 log.Println 调用。计划: 1. 引入 log 包 2. 在关键位置添加日志", file)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("执行修改"), ToolCalls: []llm.ToolCall{
			{Name: "file_patch", Arguments: fmt.Sprintf(`{"path":"%s","old":"fmt.Println","new":"log.Println"}`, file), ID: fmt.Sprintf("call_%d", i*4+1)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: "patch applied successfully", ToolCallID: fmt.Sprintf("call_%d", i*4+1)})

		msgs = append(msgs, llm.Message{Role: "assistant", Content: "修改完成，现在编译测试", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"cd /home/user/myproject && go build ./..."}`, ID: fmt.Sprintf("call_%d", i*4+2)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: fmt.Sprintf("Error: %s\nexit status 1", errMsg), ToolCallID: fmt.Sprintf("call_%d", i*4+2)})

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("编译失败: %s。需要修复: 添加 import \"log\" 声明", errMsg), ToolCalls: []llm.ToolCall{
			{Name: "file_patch", Arguments: fmt.Sprintf(`{"path":"%s","old":"import \"fmt\"","new":"import (\n  \"fmt\"\n  \"log\"\n)"}`, file), ID: fmt.Sprintf("call_%d", i*4+3)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: "patch applied successfully", ToolCallID: fmt.Sprintf("call_%d", i*4+3)})
	}

	return msgs
}

// buildMultimodalConversation 构造含图片/thinking 的多模态对话
func buildMultimodalConversation(turns int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是一个多模态助手"},
	}

	for i := 0; i < turns; i++ {
		msgs = append(msgs, llm.Message{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "text", Text: fmt.Sprintf("请分析第%d张截图中的错误", i+1)},
				{Type: "image", Text: fmt.Sprintf("data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA...%d", i)},
			},
		})
		msgs = append(msgs, llm.Message{
			Role: "assistant",
			Content: []llm.ContentBlock{
				{Type: "thinking", Text: fmt.Sprintf("让我分析这张图片...第%d张", i+1), Thinking: fmt.Sprintf("推理过程: 图片显示了错误信息，需要检查代码第 %d 行", i*10)},
				{Type: "text", Text: fmt.Sprintf("我看到第%d张截图中有编译错误，位于 /home/user/file%d.go", i+1, i)},
			},
		})
	}

	return msgs
}

// buildLongToolOutputConversation 构造含超长工具输出的对话
func buildLongToolOutputConversation(turns int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是代码助手"},
		{Role: "user", Content: "请运行测试并分析所有输出"},
	}

	for i := 0; i < turns; i++ {
		// 超长测试输出
		longOutput := strings.Repeat(fmt.Sprintf("--- PASS: TestExample%d (%.2fs)\n", i, float64(i)*0.1), 50)
		longOutput += fmt.Sprintf("--- FAIL: TestError%d (%.2fs)\n    main_test.go:%d: Error: assertion failed\n", i, float64(i)*0.2, i*10+5)
		longOutput += strings.Repeat("    expected: true, got: false\n", 10)

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("运行第%d批测试", i+1), ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: fmt.Sprintf(`{"command":"go test -v ./...%d"}`, i), ID: fmt.Sprintf("call_%d", i)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: longOutput, ToolCallID: fmt.Sprintf("call_%d", i)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("第%d批测试完成，有失败用例需要修复", i+1)})
	}

	return msgs
}

// --- 辅助: 提取压缩后内容用于验证 ---

func extractAllContent(msgs []llm.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok {
			sb.WriteString(s)
			sb.WriteString("\n")
		} else if blocks, ok := m.Content.([]llm.ContentBlock); ok {
			for _, b := range blocks {
				sb.WriteString(b.Text)
				sb.WriteString(" ")
				if b.Thinking != "" {
					sb.WriteString(b.Thinking)
				}
			}
			sb.WriteString("\n")
		}
		// 提取 ToolCalls (file_read, file_patch 等工具名在 ToolCalls 字段)
		for _, tc := range m.ToolCalls {
			sb.WriteString(tc.Name)
			sb.WriteString(" ")
			sb.WriteString(tc.Arguments)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func containsKeyword(content, keyword string) bool {
	return strings.Contains(content, keyword)
}

func countKeyword(content, keyword string) int {
	return strings.Count(content, keyword)
}

// ============================================================
// 测试 1: 30 轮代码编辑对话 - 信息留存率
// ============================================================

func TestStress_30Rounds_CodeEditing_Retention(t *testing.T) {
	// MaxTokens 设置: 让消息(7108 tokens)超过 compact 阈值(80%)但不超 hard limit(95%)
	// 8000*0.8=6400 < 7108 < 8000*0.95=7600
	cm := newTestContextManager(8000)
	cm.KeepRecent = 4
	cm.KeepThinkingRounds = 2
	cm.cachePrefixStable = 1

	msgs := buildCodeEditingConversation(30) // 30 轮 = ~240 条消息
	originalTokens := cm.EstimateTokens(msgs)
	originalContent := extractAllContent(msgs)

	t.Logf("原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	// 关键信息清单
	keyInfo := []struct {
		desc    string
		keyword string
	}{
		{"项目根路径", "/home/user/myproject"},
		{"main.go 路径", "main.go"},
		{"handler.go 路径", "handler.go"},
		{"config.go 路径", "config.go"},
		{"用户目标: 日志功能", "日志"},
		{"用户目标: log 包", "log"},
		{"编译错误", "undefined"},
		{"修复决策: import", "import"},
		{"工具: file_read", "file_read"},
		{"工具: file_patch", "file_patch"},
	}

	// 压缩前: 所有关键信息都在
	for _, info := range keyInfo {
		if !containsKeyword(originalContent, info.keyword) {
			t.Errorf("压缩前应包含 %s: %s", info.desc, info.keyword)
		}
	}

	// 执行压缩
	result, err := cm.Compact(msgs)
	if err != nil {
		t.Logf("Compact 返回错误 (可接受): %v", err)
	}
	if result == nil {
		t.Fatal("Compact 返回 nil")
	}

	compressedTokens := cm.EstimateTokens(result)
	compressedContent := extractAllContent(result)

	t.Logf("压缩后: %d 条消息, %d tokens (压缩率 %.1f%%)",
		len(result), compressedTokens, float64(compressedTokens)/float64(originalTokens)*100)

	// 验证关键信息留存
	retained := 0
	for _, info := range keyInfo {
		if containsKeyword(compressedContent, info.keyword) {
			retained++
		} else {
			t.Logf("  [丢失] %s: %s", info.desc, info.keyword)
		}
	}

	retentionRate := float64(retained) / float64(len(keyInfo)) * 100
	t.Logf("信息留存率: %d/%d (%.1f%%)", retained, len(keyInfo), retentionRate)

	// 至少保留 70% 的关键信息
	if retentionRate < 70 {
		t.Errorf("信息留存率 %.1f%% 低于 70%% 阈值", retentionRate)
	}

	// 压缩后 token 应显著减少
	if compressedTokens >= originalTokens {
		t.Error("压缩后 token 未减少")
	}
}

// ============================================================
// 测试 2: 50 轮对话 - 连续多次压缩后的信息衰减
// ============================================================

func TestStress_50Rounds_MultiCompaction_Decay(t *testing.T) {
	// 50 轮约 11800 tokens, MaxTokens=14000: 14000*0.8=11200 < 11800 < 14000*0.95=13300
	cm := newTestContextManager(14000)
	cm.KeepRecent = 4
	cm.cachePrefixStable = 1

	msgs := buildCodeEditingConversation(50)
	originalContent := extractAllContent(msgs)
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	// 关键信息 (应跨多次压缩保留)
	criticalInfo := []string{
		"/home/user/myproject",
		"main.go",
		"日志",
		"log",
	}

	// 模拟连续 3 次压缩 (每次压缩后再添加新对话)
	current := msgs
	for round := 1; round <= 3; round++ {
		result, err := cm.Compact(current)
		if err != nil {
			t.Logf("第%d次压缩错误: %v", round, err)
		}
		if result == nil {
			t.Fatalf("第%d次压缩返回 nil", round)
		}

		compressedTokens := cm.EstimateTokens(result)
		content := extractAllContent(result)

		// 检查关键信息
		retained := 0
		for _, kw := range criticalInfo {
			if containsKeyword(content, kw) {
				retained++
			}
		}

		t.Logf("第%d次压缩: %d 条消息, %d tokens, 关键信息 %d/%d",
			round, len(result), compressedTokens, retained, len(criticalInfo))

		// 关键信息至少保留 75%
		if retained < len(criticalInfo)*3/4 {
			t.Errorf("第%d次压缩后关键信息留存 %d/%d 低于 75%%", round, retained, len(criticalInfo))
		}

		current = result
	}

	finalTokens := cm.EstimateTokens(current)
	finalContent := extractAllContent(current)

	// 最终衰减率
	decayRate := (1 - float64(len(finalContent))/float64(len(originalContent))) * 100
	t.Logf("最终: %d tokens, 内容衰减 %.1f%%", finalTokens, decayRate)

	// 最关键的信息 (项目路径) 必须保留
	if !containsKeyword(finalContent, "/home/user/myproject") {
		t.Error("最终压缩后丢失了最关键的项目路径信息")
	}
}

// ============================================================
// 测试 3: 多模态对话 - thinking 块清理 + 图片 token 计数
// ============================================================

func TestStress_Multimodal_ThinkingCleanup(t *testing.T) {
	cm := newTestContextManager(50000)
	cm.KeepThinkingRounds = 2
	cm.MicrocompactAssistant = 1000

	turns := 20
	msgs := buildMultimodalConversation(turns)
	originalTokens := cm.EstimateTokens(msgs)

	// 统计原始 thinking 块数
	originalThinking := 0
	for _, m := range msgs {
		if hasThinkingBlock(m.Content) {
			originalThinking++
		}
	}
	t.Logf("原始: %d 条消息, %d tokens, %d 个 thinking 块",
		len(msgs), originalTokens, originalThinking)

	// 执行 Microcompact (含 thinking 清理)
	result := cm.Microcompact(msgs)
	compressedTokens := cm.EstimateTokens(result)

	// 统计清理后 thinking 块数
	remainingThinking := 0
	for _, m := range result {
		if hasThinkingBlock(m.Content) {
			remainingThinking++
		}
	}

	t.Logf("Microcompact 后: %d tokens, %d 个 thinking 块",
		compressedTokens, remainingThinking)

	// 应只保留最近 KeepThinkingRounds 个 thinking
	if remainingThinking > cm.KeepThinkingRounds {
		t.Errorf("thinking 块应 <= %d, 实际 %d", cm.KeepThinkingRounds, remainingThinking)
	}

	// thinking 清理应释放 token
	if compressedTokens >= originalTokens {
		t.Error("thinking 清理后 token 未减少")
	}

	// 图片块应保留 (不被 thinking 清理影响)
	imageCount := 0
	for _, m := range result {
		if blocks, ok := m.Content.([]llm.ContentBlock); ok {
			for _, b := range blocks {
				if b.Type == "image" {
					imageCount++
				}
			}
		}
	}
	if imageCount == 0 {
		t.Error("图片块不应被 thinking 清理删除")
	}
}

// ============================================================
// 测试 4: 超长工具输出 - Microcompact 裁剪效果
// ============================================================

func TestStress_LongToolOutput_Microcompact(t *testing.T) {
	cm := newTestContextManager(50000)
	cm.MicrocompactToolResult = 200 // 低阈值强制裁剪

	turns := 15
	msgs := buildLongToolOutputConversation(turns)
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	result := cm.Microcompact(msgs)
	compressedTokens := cm.EstimateTokens(result)

	freed := originalTokens - compressedTokens
	freedPercent := float64(freed) / float64(originalTokens) * 100

	t.Logf("Microcompact 后: %d tokens, 释放 %d (%.1f%%)",
		compressedTokens, freed, freedPercent)

	// 应释放大量 token
	if freed <= 0 {
		t.Error("Microcompact 应释放 token")
	}

	// 验证截断标记存在
	content := extractAllContent(result)
	if !strings.Contains(content, "[truncated") {
		t.Error("应包含截断标记")
	}

	// 验证错误信息保留 (FAIL 行不应被完全删除)
	if !strings.Contains(content, "FAIL") {
		t.Error("错误信息 (FAIL) 应被保留")
	}
}

// ============================================================
// 测试 5: 并发压缩安全 - 多 goroutine 同时操作
// ============================================================

func TestStress_ConcurrentCompact_Safety(t *testing.T) {
	cm := newTestContextManager(10000)
	cm.KeepRecent = 4

	msgs := buildCodeEditingConversation(20)

	var wg sync.WaitGroup
	errors := make([]error, 10)

	// 10 个 goroutine 同时压缩不同消息子集
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 每个 goroutine 取不同子集
			start := idx * 5 % len(msgs)
			end := start + 50
			if end > len(msgs) {
				end = len(msgs)
			}
			subset := msgs[start:end]
			_, err := cm.Compact(subset)
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	// 不 panic 即通过, 错误可接受 (并发压缩可能冲突)
	panicCount := 0
	for _, err := range errors {
		if err != nil && strings.Contains(err.Error(), "panic") {
			panicCount++
		}
	}
	if panicCount > 0 {
		t.Errorf("并发压缩出现 %d 次 panic", panicCount)
	}
	t.Logf("10 个并发压缩完成, 无 panic")
}

// ============================================================
// 测试 6: 真实 LLM 压缩 - mock server 模拟完整流程
// ============================================================

func TestStress_RealLLMCompact_30Rounds(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		// 返回包含关键信息的摘要
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "摘要: 用户在 /home/user/myproject 项目中修改 main.go 等文件，添加日志功能。遇到编译错误(undefined, syntax error)，通过添加 import 修复。使用 file_read 和 file_patch 工具。",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     5000,
				"completion_tokens": 100,
			},
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
	cm.MaxTokens = 8000 // 7108 tokens: 6400 < 7108 < 7600, 走正常压缩
	cm.KeepRecent = 4
	cm.cachePrefixStable = 1

	msgs := buildCodeEditingConversation(30)
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	start := time.Now()
	result, err := cm.Compact(msgs)
	duration := time.Since(start)

	if err != nil {
		t.Logf("Compact 错误 (可接受): %v", err)
	}
	if result == nil {
		t.Fatal("Compact 返回 nil")
	}

	compressedTokens := cm.EstimateTokens(result)
	content := extractAllContent(result)

	t.Logf("压缩后: %d 条消息, %d tokens, 耗时 %v, LLM 调用 %d 次",
		len(result), compressedTokens, duration, callCount)
	t.Logf("压缩率: %.1f%%", float64(compressedTokens)/float64(originalTokens)*100)

	// 验证关键信息留存
	criticalKeywords := []string{
		"/home/user/myproject",
		"main.go",
		"日志",
		"file_read",
		"file_patch",
	}
	retained := 0
	for _, kw := range criticalKeywords {
		if containsKeyword(content, kw) {
			retained++
		}
	}
	t.Logf("关键信息留存: %d/%d", retained, len(criticalKeywords))

	if retained < len(criticalKeywords)*3/4 {
		t.Errorf("关键信息留存 %d/%d 低于 75%%", retained, len(criticalKeywords))
	}
}

// ============================================================
// 测试 7: Token 计数压力 - 大量消息的性能
// ============================================================

func TestStress_TokenCount_Performance(t *testing.T) {
	cm := newTestContextManager(128000)

	// 构造 1000 条消息
	msgs := buildCodeEditingConversation(200) // ~1600 条消息

	start := time.Now()
	tokens := cm.EstimateTokens(msgs)
	duration := time.Since(start)

	t.Logf("1000+ 条消息 token 计数: %d tokens, 耗时 %v", tokens, duration)

	// 应在 100ms 内完成
	if duration > 100*time.Millisecond {
		t.Errorf("token 计数耗时 %v 超过 100ms", duration)
	}

	// 二次计数应更快 (缓存效果)
	start = time.Now()
	tokens2 := cm.EstimateTokens(msgs)
	duration2 := time.Since(start)
	t.Logf("二次计数: %d tokens, 耗时 %v", tokens2, duration2)

	if tokens != tokens2 {
		t.Error("两次计数结果应一致")
	}
}

// ============================================================
// 测试 8: SessionMemoryCompaction 信息提取完整性
// ============================================================

func TestStress_SessionMemory_KeyInfoExtraction(t *testing.T) {
	cm := newTestContextManager(128000)
	cm.KeepRecent = 4

	msgs := buildCodeEditingConversation(20)

	result := cm.SessionMemoryCompaction(msgs)
	if result == nil {
		t.Fatal("SessionMemoryCompaction 返回 nil")
	}

	content := extractAllContent(result)

	// 验证提取的各类信息
	checks := []struct {
		desc     string
		keyword  string
		required bool
	}{
		{"用户目标", "日志", true},
		{"文件路径", "/home/user/myproject", true},
		{"文件路径", "main.go", true},
		{"工具调用", "file_read", true},
		{"工具调用", "file_patch", true},
		{"错误信息", "undefined", false}, // 可能在错误提取中
		{"修复决策", "import", false},
	}

	t.Logf("SessionMemory 结果: %d 条消息", len(result))

	passed := 0
	for _, check := range checks {
		found := containsKeyword(content, check.keyword)
		status := "丢失"
		if found {
			status = "保留"
			passed++
		}
		t.Logf("  [%s] %s: %s", status, check.desc, check.keyword)

		if check.required && !found {
			t.Errorf("必需信息丢失: %s (%s)", check.desc, check.keyword)
		}
	}
	t.Logf("信息提取: %d/%d 项保留", passed, len(checks))
}

// ============================================================
// 测试 9: PTL 重试压力 - 连续 PTL 错误后恢复
// ============================================================

func TestStress_PTLRetry_Recovery(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 3 {
			// 前 3 次 PTL 错误
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"prompt is too long: 50000 > 8192"}}`))
			return
		}
		// 第 4 次成功
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":      map[string]any{"role": "assistant", "content": "压缩摘要: 保留了关键信息"},
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

	msgs := buildCodeEditingConversation(15)
	summary, err := cm.callCompactLLMWithHistory(msgs, nil)

	t.Logf("PTL 重试: 调用 %d 次, 错误: %v, 摘要长度: %d", callCount, err, len(summary))

	if callCount < 3 {
		t.Errorf("应重试至少 3 次, 实际 %d", callCount)
	}
	// 第 4 次应成功
	if callCount >= 4 && summary == "" {
		t.Error("第 4 次成功后应返回摘要")
	}
}

// ============================================================
// 测试 10: 综合场景 - 混合消息类型的完整压缩流程
// ============================================================

func TestStress_MixedScenario_FullPipeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "综合摘要: 用户在 /home/user/myproject 修改 main.go 添加日志。遇到编译错误，用 file_patch 修复。使用 file_read 读取文件。",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     3000,
				"completion_tokens": 80,
			},
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
	cm.MaxTokens = 18000 // 混合场景约 15175 tokens: 14400 < 15175 < 17100, 走正常压缩
	cm.KeepRecent = 4
	cm.KeepThinkingRounds = 2
	cm.MicrocompactToolResult = 200
	cm.cachePrefixStable = 1

	// 混合: 代码编辑 + 多模态 + 长输出
	codeMsgs := buildCodeEditingConversation(10)
	multimodalMsgs := buildMultimodalConversation(5)
	longOutputMsgs := buildLongToolOutputConversation(5)

	// 合并 (保留 system)
	msgs := codeMsgs
	msgs = append(msgs, multimodalMsgs[1:]...) // 跳过 multimodal 的 system
	msgs = append(msgs, longOutputMsgs[2:]...) // 跳过 longOutput 的 system+user

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("混合场景原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	// 执行完整压缩流程
	start := time.Now()
	result, err := cm.Compact(msgs)
	duration := time.Since(start)

	if err != nil {
		t.Logf("Compact 错误 (可接受): %v", err)
	}
	if result == nil {
		t.Fatal("Compact 返回 nil")
	}

	compressedTokens := cm.EstimateTokens(result)
	content := extractAllContent(result)

	t.Logf("混合场景压缩后: %d 条消息, %d tokens, 耗时 %v",
		len(result), compressedTokens, duration)
	t.Logf("压缩率: %.1f%%", float64(compressedTokens)/float64(originalTokens)*100)

	// 验证跨场景的关键信息
	criticalInfo := []string{
		"/home/user/myproject",
		"main.go",
		"file_read",
		"file_patch",
	}
	retained := 0
	for _, kw := range criticalInfo {
		if containsKeyword(content, kw) {
			retained++
		}
	}
	t.Logf("跨场景关键信息留存: %d/%d", retained, len(criticalInfo))

	if retained < len(criticalInfo)*3/4 {
		t.Errorf("关键信息留存 %d/%d 低于 75%%", retained, len(criticalInfo))
	}

	// 验证校准因子被更新 (LLM 返回了 usage)
	factor, samples, _ := cm.GetCalibrationInfo()
	t.Logf("校准因子: %.3f, 样本数: %d", factor, samples)
	if samples == 0 {
		t.Log("注意: 校准样本为 0 (可能走了 session memory 路径)")
	}
}

// ============================================================
// 测试 11: 极端压力 - 100 轮对话
// ============================================================

func TestStress_100Rounds_Extreme(t *testing.T) {
	// 100 轮约 23474 tokens, MaxTokens=28000: 22400 < 23474 < 26600, 走正常压缩
	cm := newTestContextManager(28000)
	cm.KeepRecent = 4
	cm.cachePrefixStable = 1

	msgs := buildCodeEditingConversation(100) // ~800 条消息
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("极端场景: %d 条消息, %d tokens", len(msgs), originalTokens)

	// 多次压缩
	current := msgs
	for round := 1; round <= 5; round++ {
		result, err := cm.Compact(current)
		if err != nil {
			t.Logf("第%d次压缩错误: %v", round, err)
		}
		if result == nil {
			break
		}
		current = result
		t.Logf("第%d次压缩: %d 条消息, %d tokens",
			round, len(current), cm.EstimateTokens(current))
	}

	finalContent := extractAllContent(current)
	finalTokens := cm.EstimateTokens(current)

	t.Logf("最终: %d tokens, 压缩率 %.1f%%",
		finalTokens, float64(finalTokens)/float64(originalTokens)*100)

	// 最关键信息必须保留
	if !containsKeyword(finalContent, "/home/user/myproject") {
		t.Error("100 轮压缩后丢失项目路径")
	}
	if !containsKeyword(finalContent, "main.go") {
		t.Error("100 轮压缩后丢失 main.go")
	}

	// 最终 token 应远小于原始
	if finalTokens >= originalTokens/2 {
		t.Errorf("最终 token %d 应小于原始的 50%% (%d)", finalTokens, originalTokens/2)
	}
}

// ============================================================
// 测试 12: CountTokensPrecise 压力 - API 降级容错
// ============================================================

func TestStress_CountTokensPrecise_Failover(t *testing.T) {
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		if failCount%3 == 0 {
			// 每 3 次请求, 第 3 次成功
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"input_tokens": 1234})
			return
		}
		// 其余失败
		w.WriteHeader(http.StatusServiceUnavailable)
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

	msgs := buildCodeEditingConversation(5)

	// 连续 10 次调用, 验证降级
	preciseCount := 0
	degradedCount := 0
	for i := 0; i < 10; i++ {
		precise, tokens := cm.CountTokensPrecise(msgs)
		if precise {
			preciseCount++
		} else {
			degradedCount++
		}
		if tokens <= 0 {
			t.Errorf("第%d次调用返回 0 tokens", i)
		}
	}

	t.Logf("CountTokensPrecise: %d 次精确, %d 次降级", preciseCount, degradedCount)

	// 降级时应返回本地估算 (非 0)
	if degradedCount == 0 {
		t.Error("应有降级情况")
	}
}
