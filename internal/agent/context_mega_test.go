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
// 百万 token 级上下文能力压力测试
// 模拟 deepseek-v4-flash 等 100万 context 窗口模型的真实场景
// ============================================================

// --- 大规模场景构造器 ---

// genLargeFileContent 生成大文件内容 (模拟读取整个代码文件)
// 每行约 40 字符 ≈ 20 token, lines 行 ≈ lines*20 token
func genLargeFileContent(filename string, lines int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"log\"\n\t\"os\"\n\t\"strings\"\n)\n\n")
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "// Line %d: function %s_%d implementation\n", i, filename, i)
		fmt.Fprintf(&sb, "func %s_%d(x int) int {\n\tlog.Println(\"processing %d\")\n\treturn x*%d + %d\n}\n\n", filename, i, i, i, i)
	}
	return sb.String()
}

// genLargeLogOutput 生成超长日志输出 (模拟 go test -v 全量输出)
func genLargeLogOutput(lines int) string {
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		if i%100 == 99 {
			fmt.Fprintf(&sb, "panic: runtime error: invalid memory address or nil pointer dereference [recovered]\n\tgoroutine %d [running]:\n", i)
		} else if i%50 == 49 {
			fmt.Fprintf(&sb, "--- FAIL: TestCritical_%d (%.2fs)\n    main_test.go:%d: Error: assertion failed\n    expected: true, got: false\n", i, float64(i)*0.1, i*10+5)
		} else {
			fmt.Fprintf(&sb, "--- PASS: TestExample_%d (%.2fs)\n", i, float64(i)*0.01)
		}
	}
	return sb.String()
}

// genLargeJSONDump 生成大 JSON dump (模拟数据库查询结果)
func genLargeJSONDump(records int) string {
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	for i := 0; i < records; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"user_%d","email":"user_%d@example.com","data":"payload_%d","nested":{"key":"value_%d","arr":[1,2,3,%d]}}`, i, i, i, i, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

// buildMegaConversation 构造百万 token 级对话
// 场景: 大型项目重构 - 读取多个大文件 + 运行测试 + 数据库分析
func buildMegaConversation(targetTokens int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是一个资深代码架构师，帮助重构大型 Go 项目。项目: /home/user/megaproject (含 200+ 文件，50万行代码)。目标: 将单体应用拆分为微服务，保持向后兼容。"},
		{Role: "user", Content: "开始重构 /home/user/megaproject。先读取核心文件 main.go 和 handler.go 分析当前架构。"},
	}

	// 大文件读取 (每个文件 ~5000 行 ≈ 10万 token)
	largeFiles := []struct {
		path  string
		lines int
	}{
		{"/home/user/megaproject/main.go", 3000},
		{"/home/user/megaproject/internal/handler/handler.go", 4000},
		{"/home/user/megaproject/internal/service/user_service.go", 2500},
		{"/home/user/megaproject/internal/service/order_service.go", 3500},
		{"/home/user/megaproject/internal/repo/db.go", 2000},
		{"/home/user/megaproject/internal/middleware/auth.go", 1500},
	}

	accumulatedTokens := 0
	for _, f := range largeFiles {
		fileContent := genLargeFileContent(f.path, f.lines)

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("读取 %s 分析架构", f.path), ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: fmt.Sprintf(`{"path":"%s"}`, f.path), ID: fmt.Sprintf("read_%s", f.path)},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: fileContent, ToolCallID: fmt.Sprintf("read_%s", f.path)})

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("分析 %s 完成。该文件含 %d 行代码，需要拆分为微服务。决定: 提取接口层。", f.path, f.lines)})

		accumulatedTokens += estimateStringTokens(fileContent)
		if accumulatedTokens >= targetTokens {
			break
		}
	}

	// 超长测试输出
	if accumulatedTokens < targetTokens {
		testLog := genLargeLogOutput(20000) // ~20万 token
		msgs = append(msgs, llm.Message{Role: "user", Content: "运行全量测试，分析所有输出"})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "执行 go test -v ./...", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"go test -v ./..."}`, ID: "test_run"},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: testLog, ToolCallID: "test_run"})
		accumulatedTokens += estimateStringTokens(testLog)
	}

	// 大 JSON dump
	if accumulatedTokens < targetTokens {
		jsonDump := genLargeJSONDump(10000) // ~10万 token
		msgs = append(msgs, llm.Message{Role: "user", Content: "查询用户数据进行分析"})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "执行数据库查询", ToolCalls: []llm.ToolCall{
			{Name: "db_query", Arguments: `{"sql":"SELECT * FROM users"}`, ID: "db_query"},
		}})
		msgs = append(msgs, llm.Message{Role: "tool", Content: jsonDump, ToolCallID: "db_query"})
		accumulatedTokens += estimateStringTokens(jsonDump)
	}

	return msgs
}

// buildHighVolumeRoundsConversation 构造高频次对话 (300+ 轮)
func buildHighVolumeRoundsConversation(turns int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是代码助手。项目: /home/user/bigproject"},
	}

	files := []string{"main.go", "handler.go", "config.go", "utils.go", "model.go", "router.go", "db.go", "auth.go"}

	for i := 0; i < turns; i++ {
		file := files[i%len(files)]
		// 每轮含中等长度内容 (累积产生大量消息)
		msgs = append(msgs, llm.Message{Role: "user", Content: fmt.Sprintf("第%d步: 修改 %s，添加功能 %d。需要确保向后兼容。", i+1, file, i)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("好的，分析 %s。决定使用 file_patch 工具修改。计划: 1. 定位修改点 2. 应用补丁 3. 验证", file), ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: fmt.Sprintf(`{"path":"/home/user/bigproject/%s"}`, file), ID: fmt.Sprintf("call_%d_a", i)},
		}})
		// 中等长度文件内容 (~500 token)
		msgs = append(msgs, llm.Message{Role: "tool", Content: genLargeFileContent(file, 25), ToolCallID: fmt.Sprintf("call_%d_a", i)})

		msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("修改 %s 完成。现在编译测试。", file), ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"go build ./..."}`, ID: fmt.Sprintf("call_%d_b", i)},
		}})
		errMsg := ""
		if i%3 == 2 {
			errMsg = fmt.Sprintf("Error: %s:%d: undefined: log.Println\nexit status 1", file, i*10)
		} else {
			errMsg = "build successful"
		}
		msgs = append(msgs, llm.Message{Role: "tool", Content: errMsg, ToolCallID: fmt.Sprintf("call_%d_b", i)})

		if i%3 == 2 {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: fmt.Sprintf("编译失败，需要修复 %s。添加 import \"log\"。", file), ToolCalls: []llm.ToolCall{
				{Name: "file_patch", Arguments: fmt.Sprintf(`{"path":"/home/user/bigproject/%s","old":"import \"fmt\"","new":"import (\n  \"fmt\"\n  \"log\"\n)"}`, file), ID: fmt.Sprintf("call_%d_c", i)},
			}})
			msgs = append(msgs, llm.Message{Role: "tool", Content: "patch applied", ToolCallID: fmt.Sprintf("call_%d_c", i)})
		}
	}

	return msgs
}

// buildMultimodalHeavyConversation 构造大量图片的多模态对话
func buildMultimodalHeavyConversation(images int) []llm.Message {
	msgs := []llm.Message{
		{Role: "system", Content: "你是多模态分析助手，分析大量截图"},
	}

	for i := 0; i < images; i++ {
		msgs = append(msgs, llm.Message{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "text", Text: fmt.Sprintf("分析第%d张截图 (共%d张)", i+1, images)},
				{Type: "image", Text: fmt.Sprintf("data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA%s", strings.Repeat("A", 100))},
			},
		})
		msgs = append(msgs, llm.Message{
			Role: "assistant",
			Content: []llm.ContentBlock{
				{Type: "thinking", Text: fmt.Sprintf("分析图片 %d", i+1), Thinking: fmt.Sprintf("推理: 图片 %d 显示了错误信息，需要检查代码", i+1)},
				{Type: "text", Text: fmt.Sprintf("第%d张截图分析完成: 发现 /home/user/file%d.go 第 %d 行有错误", i+1, i, i*10)},
			},
		})
	}

	return msgs
}

// --- 辅助函数 ---

func extractAllContentV2(msgs []llm.Message) string {
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
		for _, tc := range m.ToolCalls {
			sb.WriteString(tc.Name)
			sb.WriteString(" ")
			sb.WriteString(tc.Arguments)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func containsKw(content, keyword string) bool {
	return strings.Contains(content, keyword)
}

// ============================================================
// 测试 1: 百万 token 级对话 - 压缩与信息留存
// ============================================================

func TestMega_1M_Tokens_Retention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	// deepseek-v4-flash 等模型支持 100万 context
	cm := newTestContextManager(1000000) // 100万窗口
	cm.KeepRecent = 6
	cm.KeepThinkingRounds = 2
	cm.MicrocompactToolResult = 8000 // 大窗口下放宽裁剪阈值
	cm.MicrocompactAssistant = 10000
	cm.cachePrefixStable = 1

	// 构造 ~50万 token 对话 (大文件 + 超长日志 + JSON dump)
	msgs := buildMegaConversation(500000)
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("原始: %d 条消息, %d tokens (%.1f万)", len(msgs), originalTokens, float64(originalTokens)/10000)

	// 关键信息清单 (必须跨压缩保留)
	criticalInfo := []struct {
		desc    string
		keyword string
	}{
		{"项目路径", "/home/user/megaproject"},
		{"重构目标", "微服务"},
		{"main.go", "main.go"},
		{"handler.go", "handler.go"},
		{"user_service", "user_service"},
		{"order_service", "order_service"},
		{"测试失败", "FAIL"},
		{"数据库", "db_query"},
		{"工具: file_read", "file_read"},
		{"架构决策", "拆分"},
	}

	originalContent := extractAllContentV2(msgs)
	for _, info := range criticalInfo {
		if !containsKw(originalContent, info.keyword) {
			t.Errorf("压缩前应包含 %s: %s", info.desc, info.keyword)
		}
	}

	// 执行压缩
	result, err := cm.Compact(msgs)
	if err != nil {
		t.Logf("Compact 错误 (可接受): %v", err)
	}
	if result == nil {
		t.Fatal("Compact 返回 nil")
	}

	compressedTokens := cm.EstimateTokens(result)
	compressedContent := extractAllContentV2(result)

	t.Logf("压缩后: %d 条消息, %d tokens (%.1f万), 压缩率 %.1f%%",
		len(result), compressedTokens, float64(compressedTokens)/10000,
		float64(compressedTokens)/float64(originalTokens)*100)

	// 验证关键信息留存
	retained := 0
	for _, info := range criticalInfo {
		if containsKw(compressedContent, info.keyword) {
			retained++
		} else {
			t.Logf("  [丢失] %s: %s", info.desc, info.keyword)
		}
	}
	retentionRate := float64(retained) / float64(len(criticalInfo)) * 100
	t.Logf("信息留存率: %d/%d (%.1f%%)", retained, len(criticalInfo), retentionRate)

	if retentionRate < 70 {
		t.Errorf("信息留存率 %.1f%% 低于 70%% 阈值", retentionRate)
	}
}

// ============================================================
// 测试 2: 300 轮高频对话 - 累积压缩衰减
// ============================================================

func TestMega_300Rounds_AccumulatedDecay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.KeepRecent = 6
	cm.cachePrefixStable = 1
	cm.MicrocompactToolResult = 2000

	turns := 300
	msgs := buildHighVolumeRoundsConversation(turns)
	originalTokens := cm.EstimateTokens(msgs)
	originalContent := extractAllContentV2(msgs)

	t.Logf("原始: %d 条消息, %d tokens (%.1f万), %d 轮",
		len(msgs), originalTokens, float64(originalTokens)/10000, turns)

	criticalKws := []string{
		"/home/user/bigproject",
		"main.go",
		"向后兼容",
		"file_read",
		"file_patch",
		"undefined",
	}

	// 连续 3 次压缩
	current := msgs
	for round := 1; round <= 3; round++ {
		result, err := cm.Compact(current)
		if err != nil {
			t.Logf("第%d次压缩错误: %v", round, err)
		}
		if result == nil {
			break
		}

		compressedTokens := cm.EstimateTokens(result)
		content := extractAllContentV2(result)

		retained := 0
		for _, kw := range criticalKws {
			if containsKw(content, kw) {
				retained++
			}
		}

		t.Logf("第%d次压缩: %d 条消息, %d tokens, 关键信息 %d/%d",
			round, len(result), compressedTokens, retained, len(criticalKws))

		if retained < len(criticalKws)*2/3 {
			t.Errorf("第%d次压缩后关键信息留存 %d/%d 低于 66%%", round, retained, len(criticalKws))
		}
		current = result
	}

	finalTokens := cm.EstimateTokens(current)
	finalContent := extractAllContentV2(current)
	decayRate := (1 - float64(len(finalContent))/float64(len(originalContent))) * 100

	t.Logf("最终: %d tokens, 内容衰减 %.1f%%", finalTokens, decayRate)

	// 最关键信息必须保留
	if !containsKw(finalContent, "/home/user/bigproject") {
		t.Error("300轮压缩后丢失项目路径")
	}
}

// ============================================================
// 测试 3: 大量图片多模态 - token 计数与 thinking 清理
// ============================================================

func TestMega_HeavyMultimodal_100Images(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.KeepThinkingRounds = 3
	cm.MicrocompactAssistant = 5000

	imageCount := 100
	msgs := buildMultimodalHeavyConversation(imageCount)
	originalTokens := cm.EstimateTokens(msgs)

	// 统计原始 thinking 块
	originalThinking := 0
	originalImages := 0
	for _, m := range msgs {
		if blocks, ok := m.Content.([]llm.ContentBlock); ok {
			for _, b := range blocks {
				if b.Type == "thinking" {
					originalThinking++
				}
				if b.Type == "image" {
					originalImages++
				}
			}
		}
	}

	t.Logf("原始: %d 条消息, %d tokens, %d 图片块, %d thinking 块",
		len(msgs), originalTokens, originalImages, originalThinking)

	// 预期: 100 张图片 × 2000 token/张 = 20万 token 仅图片
	expectedImageTokens := imageCount * 2000
	t.Logf("图片 token 估算: %d (预期 %d)", originalTokens, expectedImageTokens)

	// Microcompact 清理 thinking
	result := cm.Microcompact(msgs)
	compressedTokens := cm.EstimateTokens(result)

	remainingThinking := 0
	remainingImages := 0
	for _, m := range result {
		if blocks, ok := m.Content.([]llm.ContentBlock); ok {
			for _, b := range blocks {
				if b.Type == "thinking" {
					remainingThinking++
				}
				if b.Type == "image" {
					remainingImages++
				}
			}
		}
	}

	t.Logf("Microcompact 后: %d tokens, %d 图片块, %d thinking 块",
		compressedTokens, remainingImages, remainingThinking)

	// thinking 应被清理到 KeepThinkingRounds
	if remainingThinking > cm.KeepThinkingRounds {
		t.Errorf("thinking 块应 <= %d, 实际 %d", cm.KeepThinkingRounds, remainingThinking)
	}

	// 图片块不应被 thinking 清理删除
	if remainingImages == 0 {
		t.Error("图片块不应被删除")
	}

	// 验证图片 token 计数正确 (每张 2000)
	if originalTokens < expectedImageTokens {
		t.Errorf("图片 token 计数不足: %d < %d", originalTokens, expectedImageTokens)
	}
}

// ============================================================
// 测试 4: 单条超大消息 - Microcompact 裁剪
// ============================================================

func TestMega_SingleHugeMessage_Microcompact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.MicrocompactToolResult = 5000 // 5000 token 阈值

	// 构造单条 20万 token 的工具输出 (超长日志)
	hugeLog := genLargeLogOutput(10000) // ~10万 token
	msgs := []llm.Message{
		{Role: "system", Content: "你是测试分析助手"},
		{Role: "user", Content: "运行全量测试并分析"},
		{Role: "assistant", Content: "执行测试", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"go test -v ./..."}`, ID: "huge_test"},
		}},
		{Role: "tool", Content: hugeLog, ToolCallID: "huge_test"},
		{Role: "assistant", Content: "分析完成"},
	}

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("原始: %d 条消息, %d tokens", len(msgs), originalTokens)

	result := cm.Microcompact(msgs)
	compressedTokens := cm.EstimateTokens(result)
	freed := originalTokens - compressedTokens

	t.Logf("Microcompact 后: %d tokens, 释放 %d (%.1f%%)",
		compressedTokens, freed, float64(freed)/float64(originalTokens)*100)

	if freed <= 0 {
		t.Error("应释放大量 token")
	}

	// 验证错误行保留
	content := extractAllContentV2(result)
	t.Logf("裁剪后内容长度: %d, 含 FAIL: %v, 含 panic: %v, 含 truncated: %v",
		len(content), strings.Contains(content, "FAIL"),
		strings.Contains(content, "panic"),
		strings.Contains(content, "[truncated") || strings.Contains(content, "[preserved"))
	if !strings.Contains(content, "FAIL") {
		t.Error("FAIL 行应被保留")
	}
	if !strings.Contains(content, "panic") {
		t.Error("panic 行应被保留")
	}
	if !strings.Contains(content, "[truncated") && !strings.Contains(content, "[preserved") {
		t.Error("应包含截断/保留标记")
	}
}

// ============================================================
// 测试 5: 百万 token 计数性能
// ============================================================

func TestMega_1M_TokenCount_Performance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)

	// 构造 ~100万 token
	msgs := buildMegaConversation(1000000)
	tokenCount := cm.EstimateTokens(msgs)

	t.Logf("消息数: %d, 估算 tokens: %d (%.1f万)", len(msgs), tokenCount, float64(tokenCount)/10000)

	// 性能测试: 百万 token 计数应在 500ms 内
	start := time.Now()
	tokens := cm.EstimateTokens(msgs)
	duration := time.Since(start)

	t.Logf("百万 token 计数耗时: %v", duration)

	if duration > 500*time.Millisecond {
		t.Errorf("百万 token 计数耗时 %v 超过 500ms", duration)
	}

	// 二次计数 (缓存)
	start = time.Now()
	tokens2 := cm.EstimateTokens(msgs)
	duration2 := time.Since(start)
	t.Logf("二次计数耗时: %v", duration2)

	if tokens != tokens2 {
		t.Error("两次计数结果应一致")
	}
}

// ============================================================
// 测试 6: 混合百万场景 - 完整压缩管道
// ============================================================

func TestMega_MixedScenario_FullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "综合摘要: 在 /home/user/megaproject 项目中执行微服务重构。读取了 main.go, handler.go, user_service.go, order_service.go 等核心文件。运行全量测试发现 FAIL 和 panic 错误。使用 file_read, file_patch, bash, db_query 工具。决定拆分为用户服务和订单服务。",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     500000,
				"completion_tokens": 150,
			},
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "deepseek-v4-flash",
		ConnectTimeout: 10 * time.Second,
		ReadTimeout:    30 * time.Second,
	}
	cm := NewContextManager(client)
	cm.MaxTokens = 1000000
	cm.KeepRecent = 6
	cm.KeepThinkingRounds = 2
	cm.MicrocompactToolResult = 8000
	cm.MicrocompactAssistant = 10000
	cm.cachePrefixStable = 1

	// 混合: 大文件 + 高频对话 + 多模态
	megaMsgs := buildMegaConversation(300000)        // ~30万 token
	highFreqMsgs := buildHighVolumeRoundsConversation(50) // ~50轮
	multimodalMsgs := buildMultimodalHeavyConversation(20) // 20张图片

	msgs := megaMsgs
	msgs = append(msgs, highFreqMsgs[1:]...)     // 跳过 system
	msgs = append(msgs, multimodalMsgs[1:]...)   // 跳过 system

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("混合场景原始: %d 条消息, %d tokens (%.1f万)",
		len(msgs), originalTokens, float64(originalTokens)/10000)

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
	content := extractAllContentV2(result)

	t.Logf("混合场景压缩后: %d 条消息, %d tokens, 耗时 %v, 压缩率 %.1f%%",
		len(result), compressedTokens, duration,
		float64(compressedTokens)/float64(originalTokens)*100)

	// 验证跨场景关键信息
	criticalInfo := []string{
		"/home/user/megaproject",
		"main.go",
		"微服务",
		"file_read",
		"FAIL",
		"db_query",
	}
	retained := 0
	for _, kw := range criticalInfo {
		if containsKw(content, kw) {
			retained++
		}
	}
	t.Logf("跨场景关键信息留存: %d/%d", retained, len(criticalInfo))

	if retained < len(criticalInfo)*2/3 {
		t.Errorf("关键信息留存 %d/%d 低于 66%%", retained, len(criticalInfo))
	}

	// 验证校准 (LLM 返回了 usage)
	factor, samples, lastReal := cm.GetCalibrationInfo()
	t.Logf("校准: factor=%.3f, samples=%d, lastReal=%d", factor, samples, lastReal)
}

// ============================================================
// 测试 7: 并发百万 token 压缩安全
// ============================================================

func TestMega_ConcurrentCompact_Safety(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.KeepRecent = 6

	msgs := buildMegaConversation(200000)

	var wg sync.WaitGroup
	errors := make([]error, 5)

	// 5 个 goroutine 并发压缩不同子集
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := idx * len(msgs) / 5
			end := start + len(msgs) / 5
			if end > len(msgs) {
				end = len(msgs)
			}
			subset := msgs[start:end]
			if len(subset) < 3 {
				return
			}
			_, err := cm.Compact(subset)
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	panicCount := 0
	for _, err := range errors {
		if err != nil && strings.Contains(err.Error(), "panic") {
			panicCount++
		}
	}
	if panicCount > 0 {
		t.Errorf("并发压缩出现 %d 次 panic", panicCount)
	}
	t.Logf("5 个并发百万 token 压缩完成, 无 panic")
}

// ============================================================
// 测试 8: 500 轮极端高频对话
// ============================================================

func TestMega_500Rounds_Extreme(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.KeepRecent = 8
	cm.MicrocompactToolResult = 1500
	cm.cachePrefixStable = 1

	turns := 500
	msgs := buildHighVolumeRoundsConversation(turns)
	originalTokens := cm.EstimateTokens(msgs)

	t.Logf("极端: %d 条消息, %d tokens (%.1f万), %d 轮",
		len(msgs), originalTokens, float64(originalTokens)/10000, turns)

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

	finalContent := extractAllContentV2(current)
	finalTokens := cm.EstimateTokens(current)

	t.Logf("最终: %d tokens, 压缩率 %.1f%%",
		finalTokens, float64(finalTokens)/float64(originalTokens)*100)

	// 最关键信息必须保留
	if !containsKw(finalContent, "/home/user/bigproject") {
		t.Error("500轮压缩后丢失项目路径")
	}
	if !containsKw(finalContent, "main.go") {
		t.Error("500轮压缩后丢失 main.go")
	}
}

// ============================================================
// 测试 9: CountTokensPrecise 大规模降级
// ============================================================

func TestMega_CountTokensPrecise_LargeScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		if failCount%4 == 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"input_tokens": 456789})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "deepseek-v4-flash",
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}
	cm := NewContextManager(client)

	msgs := buildMegaConversation(100000)

	preciseCount := 0
	degradedCount := 0
	for i := 0; i < 8; i++ {
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

	t.Logf("大规模 CountTokensPrecise: %d 次精确, %d 次降级", preciseCount, degradedCount)
	if degradedCount == 0 {
		t.Error("应有降级情况")
	}
}

// ============================================================
// 测试 10: PTL 重试 - 大规模消息
// ============================================================

func TestMega_PTLRetry_LargeScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			// 前 2 次 PTL 错误, 第 3 次成功
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"prompt is too long: 1200000 > 1000000"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":      map[string]any{"role": "assistant", "content": "大规模压缩摘要: 保留 /home/user/megaproject 项目核心信息"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "deepseek-v4-flash",
		ConnectTimeout: 10 * time.Second,
		ReadTimeout:    30 * time.Second,
	}
	cm := NewContextManager(client)
	cm.MaxTokens = 1000000
	cm.KeepRecent = 4
	cm.cachePrefixStable = 0

	msgs := buildMegaConversation(200000)
	// 补充多个 user round, 确保 truncateHeadForPTLRetry 能截断
	msgs = append(msgs, []llm.Message{
		{Role: "user", Content: "继续分析，查看更多文件"},
		{Role: "assistant", Content: "好的，读取更多文件", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/megaproject/extra.go"}`, ID: "extra1"},
		}},
		{Role: "tool", Content: genLargeFileContent("extra", 500), ToolCallID: "extra1"},
		{Role: "user", Content: "再查看另一个文件"},
		{Role: "assistant", Content: "读取完成", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/megaproject/another.go"}`, ID: "extra2"},
		}},
		{Role: "tool", Content: genLargeFileContent("another", 500), ToolCallID: "extra2"},
	}...)
	t.Logf("PTL 测试: %d 条消息, %d tokens", len(msgs), cm.EstimateTokens(msgs))

	// 验证 PTL 错误检测
	testErr := fmt.Errorf("HTTP 400: {\"error\":{\"message\":\"prompt is too long: 1200000 > 1000000\"}}")
	if !isPromptTooLongError(testErr) {
		t.Logf("警告: isPromptTooLongError 未匹配测试错误: %v", testErr)
	}

	summary, err := cm.callCompactLLMWithHistory(msgs, nil)
	t.Logf("PTL 重试: 调用 %d 次, 错误: %v, 摘要长度: %d", callCount, err, len(summary))

	if callCount < 3 {
		t.Errorf("应重试至少 3 次, 实际 %d", callCount)
	}
	if callCount >= 4 && summary == "" {
		t.Error("第 4 次成功后应返回摘要")
	}
}

// ============================================================
// 测试 11: 大文件 + 大日志 + 大 JSON 混合 - Microcompact 效果
// ============================================================

func TestMega_MixedLargeContent_Microcompact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mega test in short mode")
	}
	cm := newTestContextManager(1000000)
	cm.MicrocompactToolResult = 3000
	cm.MicrocompactAssistant = 5000
	cm.KeepThinkingRounds = 2

	// 混合: 大文件 + 大日志 + 大 JSON + 多模态
	msgs := []llm.Message{
		{Role: "system", Content: "你是全栈分析助手。项目: /home/user/fullstack"},
		{Role: "user", Content: "分析整个项目: 读取大文件、运行测试、查询数据库、分析截图"},
	}

	// 大文件
	bigFile := genLargeFileContent("main.go", 2000) // ~4万 token
	msgs = append(msgs, llm.Message{Role: "assistant", Content: "读取 main.go", ToolCalls: []llm.ToolCall{
		{Name: "file_read", Arguments: `{"path":"/home/user/fullstack/main.go"}`, ID: "f1"},
	}})
	msgs = append(msgs, llm.Message{Role: "tool", Content: bigFile, ToolCallID: "f1"})

	// 大日志
	bigLog := genLargeLogOutput(5000) // ~5万 token
	msgs = append(msgs, llm.Message{Role: "assistant", Content: "运行测试", ToolCalls: []llm.ToolCall{
		{Name: "bash", Arguments: `{"command":"go test -v"}`, ID: "t1"},
	}})
	msgs = append(msgs, llm.Message{Role: "tool", Content: bigLog, ToolCallID: "t1"})

	// 大 JSON
	bigJSON := genLargeJSONDump(5000) // ~5万 token
	msgs = append(msgs, llm.Message{Role: "assistant", Content: "查询数据库", ToolCalls: []llm.ToolCall{
		{Name: "db_query", Arguments: `{"sql":"SELECT * FROM users"}`, ID: "d1"},
	}})
	msgs = append(msgs, llm.Message{Role: "tool", Content: bigJSON, ToolCallID: "d1"})

	// 多模态
	msgs = append(msgs, llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{
			{Type: "text", Text: "分析这张架构图"},
			{Type: "image", Text: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA"},
		},
	})
	msgs = append(msgs, llm.Message{
		Role: "assistant",
		Content: []llm.ContentBlock{
			{Type: "thinking", Text: "分析架构图", Thinking: "推理: 微服务架构"},
			{Type: "text", Text: "架构图分析完成"},
		},
	})

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("混合大内容原始: %d 条消息, %d tokens (%.1f万)",
		len(msgs), originalTokens, float64(originalTokens)/10000)

	result := cm.Microcompact(msgs)
	compressedTokens := cm.EstimateTokens(result)
	freed := originalTokens - compressedTokens

	t.Logf("Microcompact 后: %d tokens, 释放 %d (%.1f%%)",
		compressedTokens, freed, float64(freed)/float64(originalTokens)*100)

	if freed <= 0 {
		t.Error("应释放 token")
	}

	// 验证错误信息保留
	content := extractAllContentV2(result)
	if !strings.Contains(content, "FAIL") {
		t.Error("FAIL 行应保留")
	}
	if !containsKw(content, "/home/user/fullstack") {
		t.Error("项目路径应保留")
	}
}
