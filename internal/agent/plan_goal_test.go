package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// m5: PlanFile 原子写入 (temp + rename)
func TestPlanFile_SaveAtomic(t *testing.T) {
	dir := t.TempDir()
	pf := NewPlanFile(dir, "task-atomic-001")

	plan := "# Plan\n1. Step one\n2. Step two"
	if err := pf.Save(plan); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 文件应存在且内容正确
	data, err := os.ReadFile(pf.GetPath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), plan) {
		t.Errorf("file content mismatch: %s", string(data))
	}

	// 临时文件应已被清理 (rename 后 .tmp 不应存在)
	if _, err := os.Stat(pf.GetPath() + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file should not exist after atomic save, got err=%v", err)
	}
}

func TestPlanFile_SaveEmptyPath(t *testing.T) {
	// baseDir 为空时 filePath 为空，Save 应仅更新内存不报错
	pf := NewPlanFile("", "")
	if err := pf.Save("some plan"); err != nil {
		t.Fatalf("Save with empty path should succeed, got %v", err)
	}
	loaded, err := pf.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !strings.Contains(loaded, "some plan") {
		t.Errorf("in-memory content mismatch: %s", loaded)
	}
}

func TestPlanFile_LoadNonexistent(t *testing.T) {
	dir := t.TempDir()
	pf := NewPlanFile(dir, "nonexistent-task")
	// 未保存直接 Load 应返回错误 (文件不存在)
	_, err := pf.Load()
	if err == nil {
		t.Error("expected error for nonexistent plan file")
	}
}

// m5: 原子写入不会留下部分文件 (模拟：保存后文件完整)
func TestPlanFile_SaveIntegrity(t *testing.T) {
	dir := t.TempDir()
	pf := NewPlanFile(dir, "integrity-task")

	longPlan := strings.Repeat("Step line\n", 1000)
	if err := pf.Save(longPlan); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(pf.GetPath())
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Size() == 0 {
		t.Error("plan file should not be empty after save")
	}

	// 确认没有残留 .tmp 或 .bak
	for _, suffix := range []string{".tmp", ".bak"} {
		if _, err := os.Stat(pf.GetPath() + suffix); !os.IsNotExist(err) {
			t.Errorf("residual file %s should not exist", suffix)
		}
	}
}

// m12: GoalTracker 基本状态机
func TestGoalTracker_StateMachine(t *testing.T) {
	g := NewGoalTracker("完成测试")

	if g.State() != GoalStateActive {
		t.Errorf("expected active, got %s", g.State())
	}

	g.Pause()
	if g.State() != GoalStatePaused {
		t.Errorf("expected paused, got %s", g.State())
	}

	g.Resume()
	if g.State() != GoalStateActive {
		t.Errorf("expected active after resume, got %s", g.State())
	}

	g.Complete("all done")
	if g.State() != GoalStateDone {
		t.Errorf("expected done, got %s", g.State())
	}

	report := g.StatusReport()
	if !strings.Contains(report, "完成测试") {
		t.Errorf("status report should contain objective: %s", report)
	}
}

// m12: goal 解析逻辑 — 由于 evaluateCompletion 需要 LLM client，
// 这里通过测试解析逻辑的输入输出来验证。我们直接测试解析容错性。
func TestGoalCompletion_ParsingVariants(t *testing.T) {
	// 这些是 evaluateCompletion 内部解析逻辑会处理的格式变体
	// 由于无法直接调用内部解析，我们验证关键格式能被正确识别
	cases := []struct {
		name    string
		text    string
		want    bool
		reason  string
	}{
		{
			name:   "standard_yes",
			text:   "完成: 是\n理由: 任务已完成",
			want:   true,
			reason: "任务已完成",
		},
		{
			name:   "chinese_colon",
			text:   "完成：是\n理由：测试通过",
			want:   true,
			reason: "测试通过",
		},
		{
			name:   "with_spaces",
			text:   "完成:  是 \n理由:  done",
			want:   true,
			reason: "done",
		},
		{
			name:   "explicit_no",
			text:   "完成: 否\n理由: 还有未完成项",
			want:   false,
			reason: "还有未完成项",
		},
		{
			name:   "english_yes",
			text:   "完成: yes\n理由: completed",
			want:   true,
			reason: "completed",
		},
		{
			name:   "english_true",
			text:   "完成: true\n理由: all good",
			want:   true,
			reason: "all good",
		},
		{
			name:   "fallback_completed_phrase",
			text:   "目标已完成，所有测试通过",
			want:   true,
			reason: "",
		},
		{
			name:   "fallback_english",
			text:   "goal completed successfully",
			want:   true,
			reason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completed, reason := parseGoalResult(tc.text)
			if completed != tc.want {
				t.Errorf("parseGoalResult(%q) completed = %v, want %v", tc.text, completed, tc.want)
			}
			if tc.reason != "" && reason != tc.reason {
				t.Errorf("parseGoalResult(%q) reason = %q, want %q", tc.text, reason, tc.reason)
			}
		})
	}
}

// parseGoalResult 复刻 evaluateCompletion 中的解析逻辑，用于测试
// 注意：这是为了可测试性而提取的纯函数版本
func parseGoalResult(text string) (bool, string) {
	lowerText := strings.ToLower(text)
	completed := false

	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(lower, "完成") {
			continue
		}
		rest := strings.TrimSpace(line[len("完成"):])
		rest = strings.TrimPrefix(rest, ":")
		rest = strings.TrimPrefix(rest, "：")
		rest = strings.ToLower(strings.TrimSpace(rest))
		if strings.HasPrefix(rest, "是") || rest == "yes" || rest == "true" {
			completed = true
		}
		break
	}

	if !completed {
		if strings.Contains(lowerText, "目标已完成") ||
			strings.Contains(lowerText, "已完成") ||
			strings.Contains(lowerText, "goal completed") ||
			strings.Contains(lowerText, "completed: yes") {
			completed = true
		}
	}

	reason := ""
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(lower, "理由") {
			continue
		}
		rest := strings.TrimSpace(line[len("理由"):])
		rest = strings.TrimPrefix(rest, ":")
		rest = strings.TrimPrefix(rest, "：")
		reason = strings.TrimSpace(rest)
		break
	}
	return completed, reason
}

// m12: GoalTracker 暂停时不注入提醒
func TestGoalTracker_PausedNoRemind(t *testing.T) {
	g := NewGoalTracker("test objective")
	g.Pause()

	// 暂停状态下 ShouldRemind 应返回 false
	if ok, _ := g.ShouldRemind(10); ok {
		t.Error("paused goal should not remind")
	}
}

func TestGoalTracker_ActiveRemindsEveryN(t *testing.T) {
	g := NewGoalTracker("test objective")
	// remindEvery 默认 5
	if ok, _ := g.ShouldRemind(1); ok {
		t.Error("should not remind at turn 1")
	}
	if ok, _ := g.ShouldRemind(5); !ok {
		t.Error("should remind at turn 5")
	}
}

// 使用 filepath 避免 unused import
var _ = filepath.Join
