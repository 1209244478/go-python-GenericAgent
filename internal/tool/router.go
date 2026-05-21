package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/llm"
)

type Router struct {
	Cwd          string
	CodeStopSig  bool
	SkillDir     string
	PythonPath   string
	AllowedDirs  []string
}

func NewRouter(cwd string) *Router {
	pyPath := "python"
	if p, err := exec.LookPath("python3"); err == nil {
		pyPath = p
	} else if p, err := exec.LookPath("python"); err == nil {
		pyPath = p
	}

	skillDir := filepath.Join(filepath.Dir(cwd), "skills")
	return &Router{
		Cwd:        cwd,
		SkillDir:   skillDir,
		PythonPath: pyPath,
	}
}

func (r *Router) Dispatch(toolName string, args map[string]any, response *llm.Response, index int, toolNum int) *agent.StepOutcome {
	switch toolName {
	case "code_run":
		return r.doCodeRun(args, response)
	case "ask_user":
		return r.doAskUser(args)
	case "file_read":
		return r.doFileRead(args)
	case "file_write":
		return r.doFileWrite(args)
	case "file_patch":
		return r.doFilePatch(args)
	case "web_scan":
		return r.doWebScan(args)
	case "web_execute_js":
		return r.doWebExecuteJS(args)
	case "update_working_checkpoint":
		return r.doUpdateWorkingCheckpoint(args)
	case "skill_run":
		return r.doSkillRun(args)
	default:
		return &agent.StepOutcome{
			Data:       nil,
			NextPrompt: fmt.Sprintf("未知工具 %s", toolName),
		}
	}
}

func (r *Router) doCodeRun(args map[string]any, response *llm.Response) *agent.StepOutcome {
	codeType := strArg(args, "type", "python")
	code := strArg(args, "code", "")
	if code == "" {
		code = strArg(args, "script", "")
	}
	if code == "" {
		code = extractCodeBlock(response.Content, codeType)
	}
	if code == "" {
		return &agent.StepOutcome{
			Data:       "[Error] Code missing",
			NextPrompt: "\n",
		}
	}

	timeout := intArg(args, "timeout", 60)
	cwd := strArg(args, "cwd", r.Cwd)
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(r.Cwd, cwd)
	}

	result, err := r.runCode(code, codeType, timeout, cwd)
	if err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}

	return &agent.StepOutcome{
		Data:       result,
		NextPrompt: "\n",
	}
}

func (r *Router) doAskUser(args map[string]any) *agent.StepOutcome {
	question := strArg(args, "question", "请提供输入：")
	candidates, _ := args["candidates"].([]any)
	return &agent.StepOutcome{
		Data: map[string]any{
			"status": "INTERRUPT",
			"intent": "HUMAN_INTERVENTION",
			"data": map[string]any{
				"question":    question,
				"candidates":  candidates,
			},
		},
		NextPrompt: "",
		ShouldExit: true,
	}
}

func (r *Router) doFileRead(args map[string]any) *agent.StepOutcome {
	path := strArg(args, "path", "")
	start := intArg(args, "start", 1)
	count := intArg(args, "count", 200)
	keyword := strArg(args, "keyword", "")

	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}

	if !r.isPathAllowed(path) {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("Error: Access denied - path outside allowed directories: %s", path),
			NextPrompt: "\n",
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("Error: File not found: %s", path),
			NextPrompt: "\n",
		}
	}

	lines := strings.Split(string(data), "\n")
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + count
	if end > len(lines) {
		end = len(lines)
	}

	if keyword != "" {
		for i := start - 1; i < len(lines); i++ {
			if strings.Contains(strings.ToLower(lines[i]), strings.ToLower(keyword)) {
				start = i + 1
				end = start + count
				if end > len(lines) {
					end = len(lines)
				}
				break
			}
		}
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("[FILE] %d lines | showing %d-%d\n", len(lines), start, end))
	for i := start - 1; i < end && i < len(lines); i++ {
		result.WriteString(fmt.Sprintf("%d|%s\n", i+1, lines[i]))
	}

	return &agent.StepOutcome{
		Data:       result.String(),
		NextPrompt: "\n",
	}
}

func (r *Router) doFileWrite(args map[string]any) *agent.StepOutcome {
	path := strArg(args, "path", "")
	content := strArg(args, "content", "")

	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}

	if !r.isWriteAllowed(path) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - cannot write outside your workspace"},
			NextPrompt: "\n",
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}

	return &agent.StepOutcome{
		Data:       map[string]any{"status": "success", "msg": fmt.Sprintf("Written %d bytes to %s", len(content), path)},
		NextPrompt: r.anchorPrompt(false),
	}
}

func (r *Router) doFilePatch(args map[string]any) *agent.StepOutcome {
	path := strArg(args, "path", "")
	oldContent := strArg(args, "old_content", "")
	newContent := strArg(args, "new_content", "")

	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}

	if !r.isWriteAllowed(path) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - cannot write outside your workspace"},
			NextPrompt: "\n",
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "文件不存在"},
			NextPrompt: "\n",
		}
	}

	fullText := string(data)
	count := strings.Count(fullText, oldContent)
	if count == 0 {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "未找到匹配的旧文本块"},
			NextPrompt: "\n",
		}
	}
	if count > 1 {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": fmt.Sprintf("找到 %d 处匹配，无法确定唯一位置", count)},
			NextPrompt: "\n",
		}
	}

	updated := strings.Replace(fullText, oldContent, newContent, 1)
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}

	return &agent.StepOutcome{
		Data:       map[string]any{"status": "success", "msg": "文件局部修改成功"},
		NextPrompt: r.anchorPrompt(false),
	}
}

func (r *Router) doWebScan(args map[string]any) *agent.StepOutcome {
	return &agent.StepOutcome{
		Data:       map[string]any{"status": "error", "msg": "web_scan requires TMWebDriver (call Python skill)"},
		NextPrompt: "\n",
	}
}

func (r *Router) doWebExecuteJS(args map[string]any) *agent.StepOutcome {
	return &agent.StepOutcome{
		Data:       map[string]any{"status": "error", "msg": "web_execute_js requires TMWebDriver (call Python skill)"},
		NextPrompt: "\n",
	}
}

func (r *Router) doUpdateWorkingCheckpoint(args map[string]any) *agent.StepOutcome {
	keyInfo := strArg(args, "key_info", "")
	return &agent.StepOutcome{
		Data:       map[string]any{"status": "success", "key_info": keyInfo},
		NextPrompt: "\n",
	}
}

func (r *Router) doSkillRun(args map[string]any) *agent.StepOutcome {
	skillName := strArg(args, "skill", "")
	skillArgs, _ := json.Marshal(args)

	skillPath := filepath.Join(r.SkillDir, skillName+".py")
	if _, err := os.Stat(skillPath); err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": fmt.Sprintf("Skill not found: %s", skillName)},
			NextPrompt: "\n",
		}
	}

	result, err := r.runPythonSkill(skillPath, string(skillArgs))
	if err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}

	if action, _ := result["action"].(string); action == "read_file" {
		if content, _ := result["content"].(string); content != "" {
			return &agent.StepOutcome{
				Data:       content,
				NextPrompt: "\n",
			}
		}
	}

	return &agent.StepOutcome{
		Data:       result,
		NextPrompt: r.anchorPrompt(false),
	}
}

func (r *Router) runCode(code, codeType string, timeout int, cwd string) (map[string]any, error) {
	var cmd *exec.Cmd
	tmpPath := ""

	switch codeType {
	case "python", "py":
		tmpFile, err := os.CreateTemp(cwd, "*.ai.py")
		if err != nil {
			return nil, err
		}
		tmpPath = tmpFile.Name()
		tmpFile.WriteString(code)
		tmpFile.Close()
		cmd = exec.Command(r.PythonPath, "-X", "utf8", "-u", tmpPath)
	case "powershell", "bash", "sh", "shell":
		if isWindows() {
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", code)
		} else {
			cmd = exec.Command("bash", "-c", code)
		}
	default:
		return nil, fmt.Errorf("不支持的类型: %s", codeType)
	}

	cmd.Dir = cwd
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
	cmd.Dir = cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if tmpPath != "" {
		os.Remove(tmpPath)
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	status := "success"
	if exitCode != 0 {
		status = "error"
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n" + stderr.String()
	}

	return map[string]any{
		"status":    status,
		"stdout":    smartFormat(output, 10000),
		"exit_code": exitCode,
	}, nil
}

func (r *Router) runPythonSkill(scriptPath, argsJSON string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.PythonPath, "-X", "utf8", "-u", scriptPath, argsJSON)
	cmd.Dir = r.Cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %s", err.Error(), stderr.String())
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return map[string]any{"status": "success", "output": stdout.String()}, nil
	}
	return result, nil
}

func (r *Router) anchorPrompt(skip bool) string {
	if skip {
		return "\n"
	}
	return "\n"
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

func extractCodeBlock(content, codeType string) string {
	pattern := fmt.Sprintf("```(?:%s)\\n([\\s\\S]*?)\\n```", regexp.QuoteMeta(codeType))
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(matches[len(matches)-1][1])
}

func smartFormat(data string, maxLen int) string {
	if len(data) < maxLen {
		return data
	}
	half := maxLen / 2
	return data[:half] + "\n\n[omitted long output]\n\n" + data[len(data)-half:]
}

func isWindows() bool {
	return strings.EqualFold(os.Getenv("OS"), "windows_NT") || strings.Contains(strings.ToLower(os.Getenv("PATH")), "\\windows\\")
}

func readLines(r io.Reader, maxLines int) []string {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() && len(lines) < maxLines {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func (r *Router) isPathAllowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)

	if strings.HasPrefix(abs, filepath.Clean(r.Cwd)) {
		return true
	}

	if r.SkillDir != "" && strings.HasPrefix(abs, filepath.Clean(r.SkillDir)) {
		return true
	}

	for _, dir := range r.AllowedDirs {
		if dir != "" && strings.HasPrefix(abs, filepath.Clean(dir)) {
			return true
		}
	}

	return false
}

func (r *Router) isWriteAllowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)

	if strings.HasPrefix(abs, filepath.Clean(r.Cwd)) {
		return true
	}

	return false
}
