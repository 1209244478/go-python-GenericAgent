package tool

import (
	"context"
	"testing"

	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/task"
)

// ==================== 工具参数验证测试 ====================

func TestSpawnSubagent_MissingPrompt(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSpawnSubagent(map[string]any{})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	if outcome.Data == nil || outcome.Data == "" {
		t.Fatalf("expected error data, got %v", outcome.Data)
	}
	t.Logf("Missing prompt error: %v", outcome.Data)
}

func TestSpawnSubagent_NoRuntime(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSpawnSubagent(map[string]any{
		"prompt": "test task",
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	dataStr, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", outcome.Data)
	}
	if dataStr == "" {
		t.Error("expected error message for missing runtime")
	}
	t.Logf("No runtime error: %s", dataStr)
}

func TestSpawnTeammate_MissingPrompt(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSpawnTeammate(map[string]any{})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("Missing prompt error: %v", outcome.Data)
}

func TestSpawnTeammate_MissingName(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSpawnTeammate(map[string]any{
		"prompt": "test",
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("Missing name error: %v", outcome.Data)
}

func TestSendMessage_MissingArgs(t *testing.T) {
	router := setupTestRouter(t)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing_all", map[string]any{}},
		{"missing_content", map[string]any{"to": "agent1"}},
		{"missing_to", map[string]any{"content": "hello"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := router.doSendMessage(tc.args)
			if outcome == nil {
				t.Fatal("outcome is nil")
			}
			t.Logf("%s: %v", tc.name, outcome.Data)
		})
	}
}

func TestExitPlanMode_MissingPlan(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doExitPlanMode(map[string]any{})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("Missing plan error: %v", outcome.Data)
}

func TestExitPlanMode_Success(t *testing.T) {
	router := setupTestRouter(t)

	plan := "1. Step one\n2. Step two\n3. Step three"
	outcome := router.doExitPlanMode(map[string]any{
		"plan": plan,
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	if outcome.PlanSubmit != plan {
		t.Errorf("PlanSubmit mismatch: got %q, want %q", outcome.PlanSubmit, plan)
	}
	if outcome.Data != plan {
		t.Errorf("Data mismatch: got %v, want %q", outcome.Data, plan)
	}
	t.Logf("Plan submitted: %s", outcome.PlanSubmit)
}

func TestSetGoal_MissingGoal(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSetGoal(map[string]any{
		"action": "set",
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("Missing goal error: %v", outcome.Data)
}

func TestSetGoal_NoRuntime(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doSetGoal(map[string]any{
		"action": "set",
		"goal":   "test goal",
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("No runtime error: %v", outcome.Data)
}

func TestUpdateTodo_MissingTodos(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doUpdateTodo(map[string]any{})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	t.Logf("Missing todos error: %v", outcome.Data)
}

func TestUpdateTodo_Success(t *testing.T) {
	router := setupTestRouter(t)

	todos := []map[string]any{
		{"id": "1", "content": "Task 1", "status": "pending"},
		{"id": "2", "content": "Task 2", "status": "in_progress"},
		{"id": "3", "content": "Task 3", "status": "completed"},
	}

	outcome := router.doUpdateTodo(map[string]any{
		"todos": todos,
	})
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	dataStr, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", outcome.Data)
	}
	if dataStr == "" {
		t.Error("expected non-empty todos JSON")
	}
	t.Logf("Todos updated: %s", dataStr)
}

// ==================== Dispatch 路由测试 ====================

func TestDispatch_AllTools(t *testing.T) {
	router := setupTestRouter(t)

	cases := []struct {
		tool     string
		args     map[string]any
		hasError bool
	}{
		{"code_run", map[string]any{"code": "print('hello')", "type": "python", "timeout": 10}, false},
		{"file_read", map[string]any{"path": "nonexistent.txt"}, false},
		{"file_write", map[string]any{"path": "test_dispatch.txt", "content": "test"}, false},
		{"file_patch", map[string]any{"path": "nonexistent.txt", "old_content": "a", "new_content": "b"}, false},
		{"ask_user", map[string]any{"question": "test?"}, false},
		{"update_working_checkpoint", map[string]any{"key_info": "test"}, false},
		{"skill_run", map[string]any{"skill": "nonexistent"}, false},
		{"spawn_subagent", map[string]any{}, true},
		{"spawn_teammate", map[string]any{}, true},
		{"send_message", map[string]any{}, true},
		{"exit_plan_mode", map[string]any{"plan": "test plan"}, false},
		{"set_goal", map[string]any{"action": "set", "goal": "test"}, true},
		{"update_todo", map[string]any{"todos": []map[string]any{{"id": "1", "content": "test", "status": "pending"}}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			outcome := router.Dispatch(tc.tool, tc.args, nil, 0, 1)
			if outcome == nil {
				t.Fatalf("Dispatch(%s) returned nil", tc.tool)
			}
			t.Logf("Dispatch(%s) => Data=%v NextPrompt=%q", tc.tool, outcome.Data, outcome.NextPrompt)
		})
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.Dispatch("nonexistent_tool", map[string]any{}, nil, 0, 1)
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	if outcome.NextPrompt == "" {
		t.Error("expected non-empty NextPrompt for unknown tool")
	}
	t.Logf("Unknown tool: %s", outcome.NextPrompt)
}

// ==================== File 工具测试 ====================

func TestFileWrite_Read_Patch(t *testing.T) {
	router := setupTestRouter(t)

	// Write (使用相对路径, 会被解析到 router.Cwd 内)
	testPath := "test_file_ops.txt"
	testContent := "line1\nline2\nline3\n"
	outcome := router.doFileWrite(map[string]any{
		"path":    testPath,
		"content": testContent,
	})
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "success" {
		t.Fatalf("write failed: %v", data["msg"])
	}

	// Read
	outcome = router.doFileRead(map[string]any{
		"path":  testPath,
		"start": 1,
		"count": 10,
	})
	readData, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string, got %T", outcome.Data)
	}
	if !contains(readData, "line1") {
		t.Errorf("read content mismatch: %s", readData)
	}
	t.Logf("Read result: %s", readData)

	// Patch
	outcome = router.doFilePatch(map[string]any{
		"path":        testPath,
		"old_content": "line2",
		"new_content": "LINE_TWO",
	})
	data, ok = outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "success" {
		t.Errorf("patch failed: %v", data["msg"])
	}

	// Verify patch
	outcome = router.doFileRead(map[string]any{
		"path":  testPath,
		"start": 1,
		"count": 10,
	})
	readData, _ = outcome.Data.(string)
	if !contains(readData, "LINE_TWO") {
		t.Errorf("patch verification failed: %s", readData)
	}
	t.Logf("After patch: %s", readData)
}

func TestFilePatch_NotFound(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doFilePatch(map[string]any{
		"path":        "nonexistent_patch_test.txt",
		"old_content": "a",
		"new_content": "b",
	})
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "error" {
		t.Errorf("expected error for nonexistent file, got %v", data["status"])
	}
}

func TestFilePatch_MultipleMatches(t *testing.T) {
	router := setupTestRouter(t)

	// Write file with duplicate content
	testPath := "test_multi_match.txt"
	outcome := router.doFileWrite(map[string]any{
		"path":    testPath,
		"content": "dup\ndup\ndup\n",
	})
	if outcome.Data.(map[string]any)["status"] != "success" {
		t.Fatal("setup write failed")
	}

	// Patch should fail (multiple matches)
	outcome = router.doFilePatch(map[string]any{
		"path":        testPath,
		"old_content": "dup",
		"new_content": "unique",
	})
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "error" {
		t.Errorf("expected error for multiple matches, got %v", data["status"])
	}
	t.Logf("Multiple matches error: %v", data["msg"])
}

func TestFileRead_WithKeyword(t *testing.T) {
	router := setupTestRouter(t)

	// Write test file
	testPath := "test_keyword.txt"
	outcome := router.doFileWrite(map[string]any{
		"path":    testPath,
		"content": "alpha\nbeta\ngamma\ndelta\nepsilon\n",
	})
	if outcome.Data.(map[string]any)["status"] != "success" {
		t.Fatal("setup write failed")
	}

	// Read with keyword
	outcome = router.doFileRead(map[string]any{
		"path":    testPath,
		"keyword": "delta",
		"count":   2,
	})
	readData, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string, got %T", outcome.Data)
	}
	if !contains(readData, "delta") {
		t.Errorf("keyword search failed: %s", readData)
	}
	t.Logf("Keyword search result: %s", readData)
}

// ==================== Code Run 测试 ====================

func TestCodeRun_MissingCode(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doCodeRun(map[string]any{
		"type": "python",
	}, nil)
	// 缺少 code 时返回字符串错误, 不是 map
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	dataStr, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string error, got %T: %v", outcome.Data, outcome.Data)
	}
	if !contains(dataStr, "Error") && !contains(dataStr, "missing") {
		t.Errorf("expected error message, got: %s", dataStr)
	}
	t.Logf("Missing code error: %s", dataStr)
}

func TestCodeRun_BlockedCode(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doCodeRun(map[string]any{
		"code": "import os\nos.system('whoami')",
		"type": "python",
	}, nil)
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "error" {
		t.Errorf("expected error for blocked code, got %v", data["status"])
	}
	t.Logf("Blocked code error: %v", data["msg"])
}

func TestCodeRun_UnsupportedType(t *testing.T) {
	router := setupTestRouter(t)

	outcome := router.doCodeRun(map[string]any{
		"code": "test",
		"type": "ruby",
	}, nil)
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", outcome.Data)
	}
	if data["status"] != "error" {
		t.Errorf("expected error for unsupported type, got %v", data["status"])
	}
}

// ==================== Security 测试 ====================

func TestSecurity_PathBlocked(t *testing.T) {
	router := setupTestRouter(t)

	blockedPaths := []string{
		"/etc/passwd",
		"/etc/shadow",
		"/root/.ssh/id_rsa",
		"/var/log/auth.log",
	}

	for _, path := range blockedPaths {
		t.Run(path, func(t *testing.T) {
			outcome := router.doFileRead(map[string]any{
				"path": path,
			})
			dataStr, ok := outcome.Data.(string)
			if !ok {
				t.Fatalf("expected string, got %T", outcome.Data)
			}
			if !contains(dataStr, "Error") && !contains(dataStr, "denied") {
				t.Errorf("expected access denied for %s, got: %s", path, dataStr)
			}
			t.Logf("Blocked: %s => %s", path, dataStr)
		})
	}
}

func TestSecurity_WritePathBlocked(t *testing.T) {
	router := setupTestRouter(t)

	blockedPaths := []string{
		"/etc/test",
		"/root/test",
		"/boot/test",
		"/usr/test",
		"/bin/test",
	}

	for _, path := range blockedPaths {
		t.Run(path, func(t *testing.T) {
			outcome := router.doFileWrite(map[string]any{
				"path":    path,
				"content": "test",
			})
			data, ok := outcome.Data.(map[string]any)
			if !ok {
				t.Fatalf("expected map, got %T", outcome.Data)
			}
			if data["status"] != "error" {
				t.Errorf("expected error for %s, got %v", path, data["status"])
			}
			t.Logf("Blocked write: %s => %v", path, data["msg"])
		})
	}
}

// ==================== Task 包测试 (独立测试) ====================

func TestMessageRouter_BasicRouting(t *testing.T) {
	mr := task.NewMessageRouter()

	// 测试空路由器
	agents := mr.ListAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}

	// 测试发送到不存在的 agent
	err := mr.Send(task.MessageEnvelope{
		From:    "a",
		To:      "nonexistent",
		Content: "hello",
	})
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
	t.Logf("Nonexistent agent error: %v", err)
}

func TestMessageRouter_History(t *testing.T) {
	mr := task.NewMessageRouter()

	// 发送几条消息 (即使失败也会记录历史)
	for i := 0; i < 5; i++ {
		_ = mr.Send(task.MessageEnvelope{
			From:    "agent1",
			To:      "nonexistent",
			Content: "test message",
		})
	}

	history := mr.GetHistory()
	if len(history) != 5 {
		t.Errorf("expected 5 history entries, got %d", len(history))
	}
	t.Logf("History count: %d", len(history))
}

func TestMessageRouter_ShutdownProtocol(t *testing.T) {
	mr := task.NewMessageRouter()

	// 发送 shutdown 请求给不存在的 agent
	err := mr.SendShutdown("main", "nonexistent", "task complete")
	if err == nil {
		t.Error("expected error for shutdown to nonexistent agent")
	}
	t.Logf("Shutdown error: %v", err)
}

func TestContentReplacementState(t *testing.T) {
	crs := task.NewContentReplacementState()

	// 测试空状态 (不应 panic)
	result := crs.Apply(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}

	// 测试 Restore 空状态
	result = crs.Restore(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}

	t.Logf("ContentReplacementState created successfully")
}

func TestIdleTimeoutMonitor(t *testing.T) {
	called := false
	monitor := task.NewIdleTimeoutMonitor(100*1_000_000, func() { // 100ms in ns
		called = true
	})

	monitor.Start()
	monitor.Activity() // 重置

	// 等待超时
	// (在实际测试中可能需要 sleep)
	monitor.Stop()

	if called {
		t.Log("Timeout callback was called")
	} else {
		t.Log("Timeout was stopped before callback")
	}
}

func TestCombinedAbortSignal(t *testing.T) {
	signal := task.NewCombinedAbortSignal(context.Background())

	if signal.Err() != nil {
		t.Error("expected nil error initially")
	}

	signal.Trigger("test_reason")

	if signal.Err() == nil {
		t.Error("expected error after trigger")
	}

	sources := signal.Sources()
	if len(sources) != 1 || sources[0] != "test_reason" {
		t.Errorf("unexpected sources: %v", sources)
	}
	t.Logf("Sources: %v", sources)
}

func TestGracefulShutdown(t *testing.T) {
	gs := task.NewGracefulShutdown(100*1_000_000, 50*1_000_000) // 100ms, 50ms

	if gs.IsShuttingDown() {
		t.Error("expected not shutting down initially")
	}

	ch := gs.InitiateShutdown()

	if !gs.IsShuttingDown() {
		t.Error("expected shutting down after initiate")
	}

	gs.Complete()

	<-ch // 等待关闭完成

	t.Log("Graceful shutdown completed")
}

func TestAllowedPrompts(t *testing.T) {
	ap := agent.NewAllowedPrompts()

	ap.Allow("git status")
	ap.Allow("git diff")
	ap.Allow("npm run build")

	if !ap.IsAllowed("git status") {
		t.Error("git status should be allowed")
	}
	if !ap.IsAllowed("git diff main.go") {
		t.Error("git diff main.go should be allowed (prefix match)")
	}
	if ap.IsAllowed("rm -rf /") {
		t.Error("rm -rf / should NOT be allowed")
	}

	list := ap.List()
	if len(list) != 3 {
		t.Errorf("expected 3 allowed prompts, got %d", len(list))
	}
	t.Logf("Allowed prompts: %v", list)
}

func TestParseAllowedPromptsFromPlan(t *testing.T) {
	plan := `# 执行计划

## 步骤
1. 检查代码
2. 运行测试

## 允许的命令
- git status
- git diff
- npm run build
- go test
`

	ap := agent.ParseAllowedPromptsFromPlan(plan)

	if !ap.IsAllowed("git status") {
		t.Error("git status should be allowed")
	}
	if !ap.IsAllowed("go test ./...") {
		t.Error("go test should be allowed (prefix match)")
	}
	if ap.IsAllowed("rm -rf /") {
		t.Error("rm -rf / should NOT be allowed")
	}

	t.Logf("Parsed allowed prompts: %v", ap.List())
}

// ==================== Agent 包测试 ====================

func TestGoalTracker(t *testing.T) {
	tracker := agent.NewGoalTracker("完成单元测试")

	if tracker.Objective() != "完成单元测试" {
		t.Errorf("objective mismatch: %s", tracker.Objective())
	}

	if tracker.State() != agent.GoalStateActive {
		t.Errorf("expected active state, got %s", tracker.State())
	}

	tracker.Pause()
	if tracker.State() != agent.GoalStatePaused {
		t.Errorf("expected paused state, got %s", tracker.State())
	}

	tracker.Resume()
	if tracker.State() != agent.GoalStateActive {
		t.Errorf("expected active state after resume, got %s", tracker.State())
	}

	tracker.Complete("all tests passed")
	if tracker.State() != agent.GoalStateDone {
		t.Errorf("expected completed state, got %s", tracker.State())
	}

	report := tracker.StatusReport()
	if report == "" {
		t.Error("expected non-empty status report")
	}
	t.Logf("Status report: %s", report)
}

func TestPlanFile(t *testing.T) {
	pf := agent.NewPlanFile(t.TempDir(), "test-task-123")

	plan := "1. Step one\n2. Step two\n3. Step three"
	if err := pf.Save(plan); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	if pf.IsApproved() {
		t.Error("expected not approved initially")
	}

	pf.MarkApproved()
	if !pf.IsApproved() {
		t.Error("expected approved after MarkApproved")
	}

	loaded, err := pf.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !contains(loaded, plan) {
		t.Errorf("loaded content mismatch: %s", loaded)
	}
	t.Logf("Plan file path: %s", pf.GetPath())
}

// ==================== 辅助函数 ====================

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
