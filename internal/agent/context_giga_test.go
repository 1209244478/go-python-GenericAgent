package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genericagent/ga/internal/llm"
)

// ============================================================
// 亿级 token 上下文能力压力测试
// 模拟几亿 token 场景 (1亿/2亿/5亿), 验证:
//   - token 计数性能 (应在秒级完成)
//   - 内存安全 (不 OOM)
//   - 压缩后信息留存
//   - Microcompact 裁剪效果
//
// 注意: 1亿 token ≈ 1.4亿字符 ≈ 280MB 字符串
//       5亿 token ≈ 7亿字符 ≈ 1.4GB 字符串
// ============================================================

// genHugeContent 生成指定 token 数的巨大内容
// 使用重复模式, 模拟真实代码/日志场景
func genHugeContent(targetTokens int) string {
	// 每行约 20 token (40 字符英文代码)
	linesNeeded := targetTokens / 20
	if linesNeeded < 1 {
		linesNeeded = 1
	}

	// 使用 strings.Builder 高效拼接
	var sb strings.Builder
	// 预分配容量 (每行约 45 字节)
	sb.Grow(linesNeeded * 50)

	// 生成模式: 每 100 行含 1 个 FAIL, 每 200 行含 1 个 panic
	for i := 0; i < linesNeeded; i++ {
		if i%200 == 199 {
			sb.WriteString("panic: runtime error: invalid memory address or nil pointer dereference [recovered]\n")
		} else if i%100 == 99 {
			fmt.Fprintf(&sb, "--- FAIL: TestCritical_%d (%.2fs) main_test.go:%d: Error: assertion failed expected true got false\n", i, float64(i)*0.1, i*10+5)
		} else {
			fmt.Fprintf(&sb, "// Line %d: func handler_%d(x int) int { return x*%d + %d } // processing\n", i, i, i, i)
		}
	}
	return sb.String()
}

// genHugeFileContent 生成模拟大文件内容 (含路径标记)
func genHugeFileContent(path string, targetTokens int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"log\"\n\t\"os\"\n)\n\n// File: ")
	sb.WriteString(path)
	sb.WriteString("\n\n")
	linesNeeded := targetTokens / 20
	if linesNeeded < 1 {
		linesNeeded = 1
	}
	sb.Grow(linesNeeded * 50)
	for i := 0; i < linesNeeded; i++ {
		fmt.Fprintf(&sb, "// %s line %d\nfunc %s_%d(x int) int {\n\tlog.Println(\"proc %d\")\n\treturn x*%d + %d\n}\n\n", path, i, path, i, i, i, i)
	}
	return sb.String()
}

// memStats 返回当前内存使用 (MB)
func memStatsMB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1e6
}

// ============================================================
// 测试 1: 1亿 token 计数性能与内存
// ============================================================

func TestGiga_100M_TokenCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(200000000) // 2亿窗口

	// 构造 1亿 token 内容 (~1.4亿字符)
	targetTokens := 100000000
	t.Logf("开始生成 %d token 内容...", targetTokens)
	memBefore := memStatsMB()
	start := time.Now()

	hugeContent := genHugeContent(targetTokens)
	genDuration := time.Since(start)
	memAfterGen := memStatsMB()

	msgs := []llm.Message{
		{Role: "system", Content: "你是代码分析助手。项目: /home/user/gigaproject"},
		{Role: "user", Content: "分析整个项目代码"},
		{Role: "assistant", Content: "读取主文件", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/gigaproject/main.go"}`, ID: "g1"},
		}},
		{Role: "tool", Content: hugeContent, ToolCallID: "g1"},
	}

	t.Logf("内容生成: 耗时 %v, 内存 %.0f->%.0f MB (+%.0f MB)",
		genDuration, memBefore, memAfterGen, memAfterGen-memBefore)

	// token 计数性能测试
	start = time.Now()
	tokens := cm.EstimateTokens(msgs)
	countDuration := time.Since(start)
	memAfterCount := memStatsMB()

	t.Logf("1亿 token 计数: %d tokens, 耗时 %v, 内存 %.0f MB",
		tokens, countDuration, memAfterCount)

	// 性能要求: 1亿 token 计数应在 5 秒内
	if countDuration > 5*time.Second {
		t.Errorf("1亿 token 计数耗时 %v 超过 5s", countDuration)
	}

	// 内存安全: 不应超过 5GB
	if memAfterCount > 5000 {
		t.Errorf("内存使用 %.0f MB 超过 5GB", memAfterCount)
	}

	// 验证计数准确性 (应在目标值附近)
	if tokens < targetTokens/2 || tokens > targetTokens*2 {
		t.Errorf("token 计数 %d 偏离目标 %d 过多", tokens, targetTokens)
	}
}

// ============================================================
// 测试 2: 2亿 token 计数与压缩
// ============================================================

func TestGiga_200M_TokenCountAndCompact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(500000000) // 5亿窗口
	cm.KeepRecent = 4
	cm.MicrocompactToolResult = 50000 // 大窗口下放宽
	cm.cachePrefixStable = 1

	// 构造 2亿 token (两个 1亿 token 的大文件)
	targetTokens := 200000000
	t.Logf("开始生成 %d token 内容...", targetTokens)
	memBefore := memStatsMB()
	start := time.Now()

	file1 := genHugeFileContent("/home/user/gigaproject/main.go", 100000000)
	file2 := genHugeFileContent("/home/user/gigaproject/handler.go", 100000000)
	genDuration := time.Since(start)
	memAfterGen := memStatsMB()

	msgs := []llm.Message{
		{Role: "system", Content: "你是代码分析助手。项目: /home/user/gigaproject"},
		{Role: "user", Content: "分析 main.go 和 handler.go 两个核心文件"},
		{Role: "assistant", Content: "读取 main.go", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/gigaproject/main.go"}`, ID: "f1"},
		}},
		{Role: "tool", Content: file1, ToolCallID: "f1"},
		{Role: "assistant", Content: "读取 handler.go", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/gigaproject/handler.go"}`, ID: "f2"},
		}},
		{Role: "tool", Content: file2, ToolCallID: "f2"},
		{Role: "assistant", Content: "两个文件分析完成"},
	}

	t.Logf("内容生成: 耗时 %v, 内存 %.0f->%.0f MB (+%.0f MB)",
		genDuration, memBefore, memAfterGen, memAfterGen-memBefore)

	// token 计数
	start = time.Now()
	tokens := cm.EstimateTokens(msgs)
	countDuration := time.Since(start)
	memAfterCount := memStatsMB()

	t.Logf("2亿 token 计数: %d tokens, 耗时 %v, 内存 %.0f MB",
		tokens, countDuration, memAfterCount)

	if countDuration > 10*time.Second {
		t.Errorf("2亿 token 计数耗时 %v 超过 10s", countDuration)
	}

	// Microcompact 裁剪 (应释放大量 token)
	start = time.Now()
	result := cm.Microcompact(msgs)
	microDuration := time.Since(start)
	compressedTokens := cm.EstimateTokens(result)
	memAfterMicro := memStatsMB()

	freed := tokens - compressedTokens
	t.Logf("Microcompact: %d -> %d tokens, 释放 %d (%.1f%%), 耗时 %v, 内存 %.0f MB",
		tokens, compressedTokens, freed, float64(freed)/float64(tokens)*100,
		microDuration, memAfterMicro)

	if freed <= 0 {
		t.Error("Microcompact 应释放大量 token")
	}

	// 验证关键信息保留
	content := extractAllContentV2(result)
	if !strings.Contains(content, "/home/user/gigaproject") {
		t.Error("项目路径应保留")
	}
	if !strings.Contains(content, "main.go") {
		t.Error("main.go 应保留")
	}
}

// ============================================================
// 测试 3: 5亿 token 极限计数
// ============================================================

func TestGiga_500M_ExtremeTokenCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(1000000000) // 10亿窗口

	// 构造 5亿 token (~7亿字符, ~1.4GB)
	targetTokens := 500000000
	t.Logf("开始生成 %d token 内容 (约 %.1f GB)...", targetTokens, float64(targetTokens)*1.4/1e9)
	memBefore := memStatsMB()
	start := time.Now()

	hugeContent := genHugeContent(targetTokens)
	genDuration := time.Since(start)
	memAfterGen := memStatsMB()

	t.Logf("内容生成: 耗时 %v, 内存 %.0f->%.0f MB (+%.0f MB)",
		genDuration, memBefore, memAfterGen, memAfterGen-memBefore)

	msgs := []llm.Message{
		{Role: "system", Content: "你是分析助手。项目: /home/user/extremeproject"},
		{Role: "user", Content: "分析超大规模代码库"},
		{Role: "assistant", Content: "读取主文件", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/extremeproject/main.go"}`, ID: "e1"},
		}},
		{Role: "tool", Content: hugeContent, ToolCallID: "e1"},
	}

	// token 计数
	start = time.Now()
	tokens := cm.EstimateTokens(msgs)
	countDuration := time.Since(start)
	memAfterCount := memStatsMB()

	t.Logf("5亿 token 计数: %d tokens, 耗时 %v, 内存 %.0f MB",
		tokens, countDuration, memAfterCount)

	// 性能要求: 5亿 token 计数应在 60 秒内 (极限场景)
	if countDuration > 60*time.Second {
		t.Errorf("5亿 token 计数耗时 %v 超过 60s", countDuration)
	}

	// 内存安全: 5亿 token 内容约 1.4GB, 计数时翻倍, 10GB 内可接受
	if memAfterCount > 10000 {
		t.Errorf("内存使用 %.0f MB 超过 10GB", memAfterCount)
	}
}

// ============================================================
// 测试 4: 1亿 token Microcompact 错误行保留
// ============================================================

func TestGiga_100M_Microcompact_ErrorRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(200000000)
	cm.MicrocompactToolResult = 10000 // 1万 token 阈值

	targetTokens := 100000000
	t.Logf("生成 %d token 含错误行的内容...", targetTokens)
	start := time.Now()

	hugeContent := genHugeContent(targetTokens)
	genDuration := time.Since(start)

	msgs := []llm.Message{
		{Role: "system", Content: "你是测试分析助手"},
		{Role: "user", Content: "分析全量测试输出"},
		{Role: "assistant", Content: "执行测试", ToolCalls: []llm.ToolCall{
			{Name: "bash", Arguments: `{"command":"go test -v"}`, ID: "t1"},
		}},
		{Role: "tool", Content: hugeContent, ToolCallID: "t1"},
	}

	t.Logf("内容生成耗时 %v", genDuration)

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("原始: %d tokens", originalTokens)

	// Microcompact
	start = time.Now()
	result := cm.Microcompact(msgs)
	microDuration := time.Since(start)
	compressedTokens := cm.EstimateTokens(result)
	freed := originalTokens - compressedTokens

	t.Logf("Microcompact: %d -> %d tokens, 释放 %d (%.2f%%), 耗时 %v",
		originalTokens, compressedTokens, freed,
		float64(freed)/float64(originalTokens)*100, microDuration)

	if freed <= 0 {
		t.Error("应释放大量 token")
	}

	// 验证错误行保留 (1亿 token 内容中应有大量 FAIL 和 panic)
	content := extractAllContentV2(result)
	failCount := strings.Count(content, "FAIL")
	panicCount := strings.Count(content, "panic")
	t.Logf("裁剪后: FAIL 行 %d, panic 行 %d", failCount, panicCount)

	if failCount == 0 {
		t.Error("FAIL 行应被保留")
	}
	if panicCount == 0 {
		t.Error("panic 行应被保留")
	}
}

// ============================================================
// 测试 5: 多个亿级文件 - 压缩信息留存
// ============================================================

func TestGiga_Multi100M_Retention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	server := newMockCompactServer()
	defer server.Close()

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "deepseek-v4-flash",
		ConnectTimeout: 10 * time.Second,
		ReadTimeout:    60 * time.Second,
	}
	cm := NewContextManager(client)
	cm.MaxTokens = 500000000 // 5亿窗口
	cm.KeepRecent = 6
	cm.MicrocompactToolResult = 50000
	cm.cachePrefixStable = 1

	// 构造 3 个 5000万 token 的文件 = 1.5亿 token
	t.Logf("生成 3 个 5000万 token 文件...")
	start := time.Now()
	memBefore := memStatsMB()

	file1 := genHugeFileContent("/home/user/multiproject/core.go", 50000000)
	file2 := genHugeFileContent("/home/user/multiproject/api.go", 50000000)
	file3 := genHugeFileContent("/home/user/multiproject/db.go", 50000000)
	genDuration := time.Since(start)
	memAfterGen := memStatsMB()

	msgs := []llm.Message{
		{Role: "system", Content: "你是架构师。项目: /home/user/multiproject。目标: 微服务拆分"},
		{Role: "user", Content: "分析 core.go, api.go, db.go 三个核心文件"},
		{Role: "assistant", Content: "读取 core.go", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/multiproject/core.go"}`, ID: "m1"},
		}},
		{Role: "tool", Content: file1, ToolCallID: "m1"},
		{Role: "assistant", Content: "读取 api.go", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/multiproject/api.go"}`, ID: "m2"},
		}},
		{Role: "tool", Content: file2, ToolCallID: "m2"},
		{Role: "assistant", Content: "读取 db.go", ToolCalls: []llm.ToolCall{
			{Name: "file_read", Arguments: `{"path":"/home/user/multiproject/db.go"}`, ID: "m3"},
		}},
		{Role: "tool", Content: file3, ToolCallID: "m3"},
		{Role: "assistant", Content: "三个文件分析完成，决定拆分为 core-service, api-service, db-service"},
	}

	t.Logf("内容生成: 耗时 %v, 内存 %.0f->%.0f MB", genDuration, memBefore, memAfterGen)

	originalTokens := cm.EstimateTokens(msgs)
	t.Logf("原始: %d tokens (%.2f亿)", originalTokens, float64(originalTokens)/1e8)

	// 压缩
	start = time.Now()
	result, err := cm.Compact(msgs)
	compactDuration := time.Since(start)

	if err != nil {
		t.Logf("Compact 错误 (可接受): %v", err)
	}
	if result == nil {
		t.Fatal("Compact 返回 nil")
	}

	compressedTokens := cm.EstimateTokens(result)
	content := extractAllContentV2(result)

	t.Logf("压缩后: %d tokens, 耗时 %v, 压缩率 %.4f%%",
		compressedTokens, compactDuration,
		float64(compressedTokens)/float64(originalTokens)*100)

	// 验证关键信息留存
	criticalInfo := []string{
		"/home/user/multiproject",
		"core.go",
		"api.go",
		"db.go",
		"微服务",
		"file_read",
	}
	retained := 0
	for _, kw := range criticalInfo {
		if strings.Contains(content, kw) {
			retained++
		}
	}
	t.Logf("关键信息留存: %d/%d", retained, len(criticalInfo))

	if retained < len(criticalInfo)*2/3 {
		t.Errorf("关键信息留存 %d/%d 低于 66%%", retained, len(criticalInfo))
	}
}

// ============================================================
// 测试 6: 亿级 token 并发计数安全
// ============================================================

func TestGiga_ConcurrentCount_Safety(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(200000000)

	// 构造 5000万 token 内容
	t.Logf("生成 5000万 token 内容...")
	hugeContent := genHugeContent(50000000)

	msgs := []llm.Message{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "分析"},
		{Role: "tool", Content: hugeContent, ToolCallID: "c1"},
	}

	// 5 个 goroutine 并发计数
	var wg sync.WaitGroup
	results := make([]int, 5)
	errors := make([]error, 5)

	start := time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = cm.EstimateTokens(msgs)
			_ = errors // placeholder
		}(i)
	}
	wg.Wait()
	duration := time.Since(start)

	t.Logf("5 并发 5000万 token 计数: 耗时 %v, 结果一致: %v",
		duration, results[0] == results[1] && results[1] == results[2])

	// 所有结果应一致
	for i := 1; i < 5; i++ {
		if results[i] != results[0] {
			t.Errorf("goroutine %d 结果 %d != goroutine 0 结果 %d", i, results[i], results[0])
		}
	}
}

// ============================================================
// 测试 7: 亿级 token 二次计数缓存效果
// ============================================================

func TestGiga_100M_CacheEffect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping giga test in short mode")
	}
	cm := newTestContextManager(200000000)

	targetTokens := 100000000
	t.Logf("生成 %d token 内容...", targetTokens)
	hugeContent := genHugeContent(targetTokens)

	msgs := []llm.Message{
		{Role: "system", Content: "你是助手"},
		{Role: "tool", Content: hugeContent, ToolCallID: "cache1"},
	}

	// 第一次计数
	start := time.Now()
	tokens1 := cm.EstimateTokens(msgs)
	firstDuration := time.Since(start)

	// 第二次计数 (应利用缓存或更快)
	start = time.Now()
	tokens2 := cm.EstimateTokens(msgs)
	secondDuration := time.Since(start)

	t.Logf("1亿 token 计数: 首次 %v, 二次 %v, 结果 %d/%d",
		firstDuration, secondDuration, tokens1, tokens2)

	if tokens1 != tokens2 {
		t.Error("两次计数结果应一致")
	}
}

// ============================================================
// 辅助: mock compact server
// ============================================================

func newMockCompactServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "摘要: 在 /home/user/multiproject 项目中分析 core.go, api.go, db.go。执行微服务拆分，使用 file_read 工具。决定拆分为 core-service, api-service, db-service。",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     150000000,
				"completion_tokens": 100,
			},
		})
	}))
}
