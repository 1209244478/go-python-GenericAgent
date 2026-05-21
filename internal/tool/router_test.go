package tool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/genericagent/ga/internal/agent"
)

func TestPythonAvailable(t *testing.T) {
	pyPath := findPython()
	if pyPath == "" {
		t.Skip("Python not found on PATH, skipping subprocess tests")
	}
	t.Logf("Python found: %s", pyPath)

	cmd := exec.Command(pyPath, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python --version failed: %v\n%s", err, out)
	}
	t.Logf("Python version: %s", string(out))
}

func TestSkillRun_Echo(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":   "test_skill",
		"action":  "echo",
		"message": "Hello from Go!",
	}

	outcome := router.doSkillRun(args)
	assertSuccess(t, outcome)

	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", outcome.Data)
	}

	if data["echo"] != "Hello from Go!" {
		t.Errorf("echo mismatch: got %v", data["echo"])
	}
	if data["action"] != "echo" {
		t.Errorf("action mismatch: got %v", data["action"])
	}
	t.Logf("Echo result: %v", data)
}

func TestSkillRun_Compute(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":  "test_skill",
		"action": "compute",
		"a":      42,
		"b":      58,
		"op":     "add",
	}

	outcome := router.doSkillRun(args)
	assertSuccess(t, outcome)

	data := outcome.Data.(map[string]any)
	result := toFloat(data["result"])
	if result != 100 {
		t.Errorf("42 + 58 = %v, want 100", result)
	}
	t.Logf("Compute result: %v + %v = %v", data["a"], data["b"], data["result"])
}

func TestSkillRun_ComputeMul(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":  "test_skill",
		"action": "compute",
		"a":      7,
		"b":      6,
		"op":     "mul",
	}

	outcome := router.doSkillRun(args)
	assertSuccess(t, outcome)

	data := outcome.Data.(map[string]any)
	result := toFloat(data["result"])
	if result != 42 {
		t.Errorf("7 * 6 = %v, want 42", result)
	}
}

func TestSkillRun_Env(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":  "test_skill",
		"action": "env",
	}

	outcome := router.doSkillRun(args)
	assertSuccess(t, outcome)

	data := outcome.Data.(map[string]any)
	t.Logf("Env result: platform_cwd=%v", data["cwd"])
}

func TestSkillRun_NotFound(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill": "nonexistent_skill",
	}

	outcome := router.doSkillRun(args)
	data := outcome.Data.(map[string]any)

	if data["status"] != "error" {
		t.Errorf("expected error status for missing skill, got %v", data["status"])
	}
	t.Logf("NotFound result: %v", data)
}

func TestSkillRun_ErrorExit(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":  "test_skill",
		"action": "error",
	}

	outcome := router.doSkillRun(args)
	data := outcome.Data.(map[string]any)

	if data["status"] != "error" {
		t.Errorf("expected error status, got %v", data["status"])
	}
	t.Logf("ErrorExit result: %v", data)
}

func TestSkillRun_Slow(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"skill":  "test_skill",
		"action": "slow",
		"delay":  1,
	}

	start := time.Now()
	outcome := router.doSkillRun(args)
	elapsed := time.Since(start)

	assertSuccess(t, outcome)
	data := outcome.Data.(map[string]any)
	if toFloat(data["slept"]) != 1 {
		t.Errorf("slept mismatch: %v", data["slept"])
	}
	t.Logf("Slow result: slept=%v, elapsed=%v", data["slept"], elapsed)
}

func TestCodeRun_Python(t *testing.T) {
	router := setupTestRouter(t)

	args := map[string]any{
		"code":    "import json; print(json.dumps({'status': 'success', 'computed': 2+3}))",
		"type":    "python",
		"timeout": 30,
	}

	outcome := router.doCodeRun(args, nil)
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T: %v", outcome.Data, outcome.Data)
	}

	if data["status"] != "success" {
		t.Errorf("expected success, got %v", data["status"])
	}
	t.Logf("CodeRun result: %v", data)
}

func TestCodeRun_PowerShell(t *testing.T) {
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("PowerShell not available")
	}

	router := setupTestRouter(t)

	args := map[string]any{
		"code":    "Write-Output '{\"status\": \"success\", \"shell\": \"powershell\"}'",
		"type":    "powershell",
		"timeout": 30,
	}

	outcome := router.doCodeRun(args, nil)
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T: %v", outcome.Data, outcome.Data)
	}

	t.Logf("PowerShell result: %v", data)
}

func TestDispatch_Routing(t *testing.T) {
	router := setupTestRouter(t)

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"code_run", map[string]any{"code": "print('hi')", "type": "python", "timeout": 10}},
		{"file_read", map[string]any{"path": "nonexistent_test_file.txt"}},
		{"ask_user", map[string]any{"question": "test?"}},
		{"skill_run", map[string]any{"skill": "test_skill", "action": "echo", "message": "dispatch test"}},
	}

	for _, tc := range cases {
		outcome := router.Dispatch(tc.tool, tc.args, nil, 0, 1)
		if outcome == nil {
			t.Errorf("Dispatch(%s) returned nil", tc.tool)
			continue
		}
		t.Logf("Dispatch(%s) => Data=%v", tc.tool, outcome.Data)
	}
}

func setupTestRouter(t *testing.T) *Router {
	t.Helper()

	pyPath := findPython()
	if pyPath == "" {
		t.Skip("Python not found on PATH")
	}

	skillDir := findSkillDir(t)
	cwd := t.TempDir()

	router := &Router{
		Cwd:        cwd,
		SkillDir:   skillDir,
		PythonPath: pyPath,
	}

	return router
}

func findPython() string {
	for _, name := range []string{"python", "python3", "py"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func findSkillDir(t *testing.T) string {
	t.Helper()

	candidates := []string{
		filepath.Join("..", "..", "skills"),
		filepath.Join("..", "..", "..", "skills"),
	}

	for _, c := range candidates {
		abs, _ := filepath.Abs(c)
		if _, err := os.Stat(filepath.Join(abs, "test_skill.py")); err == nil {
			return abs
		}
	}

	t.Fatalf("skills/test_skill.py not found, tried: %v", candidates)
	return ""
}

func assertSuccess(t *testing.T, outcome *agent.StepOutcome) {
	t.Helper()
	if outcome == nil {
		t.Fatal("outcome is nil")
	}
	data, ok := outcome.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T: %v", outcome.Data, outcome.Data)
	}
	if status, _ := data["status"].(string); status == "error" {
		t.Fatalf("unexpected error: %v", data["msg"])
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
