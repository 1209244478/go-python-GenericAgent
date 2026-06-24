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
	"strconv"
	"strings"
	"time"

	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/llm"
	"github.com/genericagent/ga/internal/task"
)

type Router struct {
	Cwd          string
	CodeStopSig  bool
	SkillDir     string
	PythonPath   string
	AllowedDirs  []string
	TaskRuntime  *task.Runtime // 子任务编排
	CurrentTaskID string       // 当前任务ID
}

var blockedCommands = []string{
	"rm -rf",
	"rm -r",
	"rmdir",
	"mkfs.",
	"dd if=",
	":(){ :|:& };:",
	"chmod",
	"chown",
	"wget",
	"curl",
	"nc -l",
	"ncat",
	"/etc/passwd",
	"/etc/shadow",
	"shutdown",
	"reboot",
	"init 0",
	"init 6",
	"systemctl",
	"service",
	"iptables",
	"ufw",
	"crontab",
	"at ",
	"nohup",
	"screen ",
	"tmux",
	"ssh ",
	"scp ",
	"rsync",
	"mount",
	"umount",
	"fdisk",
	"parted",
	"mkfs",
	"fsck",
	"swapoff",
	"swapon",
	"ln -s",
	"symlink",
	"ln -sf",
	"sqlite3 ",
	"mysql ",
	"mysqldump",
	"psql ",
	"pg_dump",
	"mongo ",
	"mongodump",
	"redis-cli",
	"mongosh",
	"sqlplus",
	"sqlcmd",
	"bt default",
	"bt ",
	"/etc/init.d/",
	"kill -9",
	"killall",
	"pkill",
	"pip install",
	"pip3 install",
	"npm install",
	"apt ",
	"yum ",
	"dnf ",
	"brew ",
	"base64 -d",
	"base64 --decode",
	"python -c",
	"python3 -c",
	"perl -e",
	"ruby -e",
	"node -e",
	"php -r",
	"env ",
	"env\n",
	"printenv",
	"export ",
	"source ",
	"bash -i",
	"sh -i",
	"/bin/bash",
	"/bin/sh",
	"dev/tcp",
	"dev/udp",
	"telnet ",
	"nc ",
	"socat",
	"openssl ",
	"jq ",
	"awk ",
	"sed -i",
	"find /",
	"xargs",
	"tee ",
	"cat /etc",
	"head /etc",
	"tail /etc",
	"less /etc",
	"more /etc",
}

var blockedCodePatterns = []string{
	"subprocess.call",
	"subprocess.Popen",
	"subprocess.run",
	"subprocess.check_output",
	"os.system",
	"os.popen",
	"exec(",
	"__import__",
	"importlib",
	"socket.socket",
	"socket.connect",
	"paramiko",
	"fabric",
	"ansible",
	"pexpect",
	"shutil.rmtree",
	"shutil.copy",
	"shutil.move",
	"os.remove",
	"os.unlink",
	"os.rmdir",
	"os.rename",
	"os.symlink",
	"os.link",
	"os.chmod",
	"os.chown",
	"os.mkdir",
	"os.makedirs",
	"os.walk",
	"os.scandir",
	"os.listdir",
	"glob.glob",
	"pathlib.Path",
	"ctypes",
	"signal.signal",
	"multiprocessing",
	"threading",
	"pickle.loads",
	"marshal.loads",
	"compile(",
	"eval(",
	"open(",
	"open (",
	"__builtins__",
	"getattr(",
	"globals()",
	"locals()",
	"vars()",
	"dir()",
	"type(",
	"base64.b64decode",
	"base64.b64encode",
	"base64.decode",
	"os.environ",
	"os.getenv",
	"os.exec",
	"os.spawn",
	"os.kill",
	"sys.exit",
	"atexit",
	"webbrowser",
	"http.server",
	"socketserver",
	"xmlrpc",
	"telnetlib",
	"smtplib",
	"ftplib",
	"urllib.request",
	"requests.get",
	"requests.post",
	"requests.put",
	"requests.delete",
	"requests.patch",
	"hmac",
	"hashlib",
	"secrets",
	"tempfile",
	"shlex",
	"codecs.decode",
	"codecs.encode",
}

var blockedDbPatterns = []string{
	"mysql.connector",
	"pymysql",
	"mysql.connector",
	"psycopg2",
	"sqlite3.connect",
	"mongodb",
	"pymongo",
	"sqlalchemy",
	"DROP DATABASE",
	"DROP TABLE",
	"DROP SCHEMA",
	"GRANT ALL",
	"CREATE USER",
	"ALTER USER",
	"mysqldump",
	"pg_dump",
}

var blockedReadPaths = []string{
	"/etc/",
	"/root/",
	"/home/",
	"/var/",
	"/www/server/",
	"/opt/genericagent/server.json",
	"/opt/genericagent/mykey.json",
	"/opt/genericagent/.env",
}

var blockedWritePaths = []string{
	"/etc/",
	"/root/",
	"/home/",
	"/var/",
	"/www/server/",
	"/opt/genericagent/server.json",
	"/opt/genericagent/mykey.json",
	"/opt/genericagent/.env",
	"/boot/",
	"/usr/",
	"/lib/",
	"/sbin/",
	"/bin/",
	"/tmp/",
	"/dev/",
	"/proc/",
	"/sys/",
	"/run/",
	"/snap/",
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
		return r.doSkillRun(args, response)
	case "spawn_subagent":
		return r.doSpawnSubagent(args)
	case "spawn_teammate":
		return r.doSpawnTeammate(args)
	case "send_message":
		return r.doSendMessage(args)
	case "exit_plan_mode":
		return r.doExitPlanMode(args)
	case "set_goal":
		return r.doSetGoal(args)
	case "update_todo":
		return r.doUpdateTodo(args)
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
	if code == "" && response != nil {
		code = extractCodeBlock(response.Content, codeType)
	}
	if code == "" {
		return &agent.StepOutcome{
			Data:       "[Error] Code missing",
			NextPrompt: "\n",
		}
	}

	if blocked, reason := r.isCodeBlocked(code, codeType); blocked {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Security policy: " + reason},
			NextPrompt: "\n",
		}
	}

	timeout := intArg(args, "timeout", 60)
	if timeout > 300 {
		timeout = 300
	}
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
	if offset := intArg(args, "offset", 0); offset > 0 {
		start = offset
	}
	count := intArg(args, "count", 200)
	keyword := strArg(args, "keyword", "")

	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}

	if isPathBlockedRead(path) {
		return &agent.StepOutcome{
			Data:       "Error: Access denied - path is restricted by security policy",
			NextPrompt: "\n",
		}
	}

	if isOtherUserDir(path, r.Cwd) {
		return &agent.StepOutcome{
			Data:       "Error: Access denied - cannot access other users' workspace",
			NextPrompt: "\n",
		}
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

	if isPathBlockedWrite(path) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - path is restricted by security policy"},
			NextPrompt: "\n",
		}
	}

	if isOtherUserDir(path, r.Cwd) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - cannot access other users' workspace"},
			NextPrompt: "\n",
		}
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

	if isPathBlockedWrite(path) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - path is restricted by security policy"},
			NextPrompt: "\n",
		}
	}

	if isOtherUserDir(path, r.Cwd) {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Access denied - cannot access other users' workspace"},
			NextPrompt: "\n",
		}
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
	// 写入前备份原文件，便于失败时回滚
	backupPath := path + ".bak"
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": fmt.Sprintf("备份失败: %v", err)},
			NextPrompt: "\n",
		}
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		// 写入失败，尝试回滚
		os.Rename(backupPath, path)
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "\n",
		}
	}
	// 写入成功，删除备份
	os.Remove(backupPath)

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

func (r *Router) doSkillRun(args map[string]any, response *llm.Response) *agent.StepOutcome {
	skillName := strArg(args, "skill", "")

	if strings.Contains(skillName, "/") || strings.Contains(skillName, "\\") || strings.Contains(skillName, "..") || strings.Contains(skillName, " ") {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": "Invalid skill name"},
			NextPrompt: "\n",
		}
	}

	// 权限检查: 读取 skill_permissions.json
	if r.SkillDir != "" {
		if permData, err := os.ReadFile(filepath.Join(r.SkillDir, "skill_permissions.json")); err == nil {
			var perms struct {
				Deny  []string `json:"deny"`
				Allow []string `json:"allow"`
			}
			if json.Unmarshal(permData, &perms) == nil {
				allowed := true
				for _, p := range perms.Deny {
					if matchToolSkillPattern(p, skillName) {
						allowed = false
						break
					}
				}
				if allowed && len(perms.Allow) > 0 {
					allowed = false
					for _, p := range perms.Allow {
						if matchToolSkillPattern(p, skillName) {
							allowed = true
							break
						}
					}
				}
				if !allowed {
					return &agent.StepOutcome{
						Data:       map[string]any{"status": "error", "msg": "Skill denied by permission policy: " + skillName},
						NextPrompt: "技能被权限策略拒绝。如需使用, 请修改 skill_permissions.json。\n",
					}
				}
			}
		}
	}

	skillArgs, _ := json.Marshal(args)

	skillPath := filepath.Join(r.SkillDir, skillName+".py")
	if _, err := os.Stat(skillPath); err != nil {
		// 纯提示词 skill fallback: 无 .py 但有 SKILL.md 的目录
		skillMdPath := filepath.Join(r.SkillDir, skillName, "SKILL.md")
		if mdData, mdErr := os.ReadFile(skillMdPath); mdErr == nil {
			skillDirAbs := filepath.Join(r.SkillDir, skillName)
			processed := substituteSkillVars(string(mdData), args, skillDirAbs, skillName)
			// 执行内联 shell 命令 (!`cmd` 和 ```!cmd```)
			processed = r.executeInlineShell(processed, skillDirAbs)

			// 检测 context: fork — 隔离子 agent 执行
			fm, _ := parseSkillFrontmatter(string(mdData))
			if fm["context"] == "fork" && r.TaskRuntime != nil {
				return r.runSkillFork(skillName, processed, args, skillDirAbs)
			}

			action := strArg(args, "action", "")
			fileName := strArg(args, "file", "")
			// read_file 读取 SKILL.md 或目录内其他文件
			if action == "read_file" || action == "read_md" || action == "info" || action == "" {
				targetPath := skillMdPath
				if fileName != "" && fileName != "SKILL.md" {
					targetPath = filepath.Join(r.SkillDir, skillName, fileName)
				}
				if content, fErr := os.ReadFile(targetPath); fErr == nil {
					processedContent := substituteSkillVars(string(content), args, skillDirAbs, skillName)
					processedContent = r.executeInlineShell(processedContent, skillDirAbs)
					return &agent.StepOutcome{
						Data:       map[string]any{"status": "success", "content": processedContent, "skill": skillName, "type": "prompt"},
						NextPrompt: "\n",
					}
				}
			}
			// 其他 action: 返回处理后的 SKILL.md 内容供 LLM 参考后自行执行
			return &agent.StepOutcome{
				Data:       map[string]any{"status": "success", "content": processed, "skill": skillName, "type": "prompt", "note": "提示词技能, 请阅读 SKILL.md 后按指导执行 (可能需要 code_run/bash 调用外部命令)"},
				NextPrompt: "\n",
			}
		}
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": fmt.Sprintf("Skill not found: %s", skillName)},
			NextPrompt: "\n",
		}
	}

	result, err := r.runPythonSkill(skillPath, string(skillArgs))
	if err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: "Skill execution failed. Please check the error and try again.\n",
		}
	}

	if status, _ := result["status"].(string); status == "error" {
		errMsg, _ := result["msg"].(string)
		return &agent.StepOutcome{
			Data:       result,
			NextPrompt: fmt.Sprintf("Skill returned error: %s\nPlease fix the issue and try again.\n", errMsg),
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
	tmpPath := ""
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch codeType {
	case "python", "py":
		tmpFile, err := os.CreateTemp(cwd, "*.ai.py")
		if err != nil {
			return nil, err
		}
		tmpPath = tmpFile.Name()
		tmpFile.WriteString(code)
		tmpFile.Close()
		cmd = exec.CommandContext(ctx, r.PythonPath, "-X", "utf8", "-u", tmpPath)
	case "powershell", "bash", "sh", "shell":
		if isWindows() {
			cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", code)
		} else {
			cmd = exec.CommandContext(ctx, "bash", "-c", code)
		}
	default:
		return nil, fmt.Errorf("不支持的类型: %s", codeType)
	}

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

	err := cmd.Run()

	if stdout.Len() > 0 {
		var result map[string]any
		if jsonErr := json.Unmarshal(stdout.Bytes(), &result); jsonErr == nil {
			return result, nil
		}
	}

	if err != nil {
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

// parseSkillFrontmatter 解析 SKILL.md 的 frontmatter (轻量版, 仅返回 map)
func parseSkillFrontmatter(content string) (map[string]string, string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, content
	}
	fm := map[string]string{}
	i := 1
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			i++
			break
		}
		idx := strings.Index(line, ":")
		if idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			if len(val) >= 2 {
				if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
					val = val[1 : len(val)-1]
				}
			}
			fm[strings.ToLower(key)] = val
		}
		i++
	}
	body := strings.Join(lines[i:], "\n")
	return fm, body
}

// matchToolSkillPattern 前缀/通配匹配 (支持 * 后缀)
func matchToolSkillPattern(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(name, prefix)
	}
	return pattern == name
}

// runSkillFork 在隔离子 agent 中执行 skill (context: fork)
// 参考 cc-haha fork: 独立 token 预算, 不污染主上下文
func (r *Router) runSkillFork(skillName, skillPrompt string, args map[string]any, skillDir string) *agent.StepOutcome {
	timeoutSec := intArg(args, "timeout", 300)
	if timeoutSec > 600 {
		timeoutSec = 600
	}

	// 递归 fork 守卫
	var parentForkDepth int
	if r.CurrentTaskID != "" {
		if parentTask, err := r.TaskRuntime.Get(r.CurrentTaskID); err == nil {
			parentForkDepth = parentTask.State.ForkDepth
		}
	}
	const maxForkDepth = 3
	if parentForkDepth >= maxForkDepth {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": fmt.Sprintf("max fork depth %d exceeded", maxForkDepth)},
			NextPrompt: fmt.Sprintf("Skill fork 失败: 已达最大 fork 深度 %d", maxForkDepth),
		}
	}

	// 构造 cache safe params
	var cacheSafe *task.CacheSafeParams
	if r.CurrentTaskID != "" {
		if parentTask, err := r.TaskRuntime.Get(r.CurrentTaskID); err == nil && parentTask.Agent != nil {
			cacheSafe = &task.CacheSafeParams{
				Model:        parentTask.Agent.Model,
				Temperature:  parentTask.Agent.Temperature,
				MaxTokens:    parentTask.Agent.MaxTokens,
				SystemPrompt: parentTask.Agent.SystemPrompt,
			}
		}
	}

	// 解析 allowed-tools 限制
	fm, _ := parseSkillFrontmatter(skillPrompt)
	allowedTools := ""
	if v, ok := fm["allowed-tools"]; ok && v != "" {
		allowedTools = v
	}

	// 构造 fork prompt: skill 指导 + 参数上下文
	forkPrompt := fmt.Sprintf("[Skill: %s (fork mode)]\n\n%s", skillName, skillPrompt)
	if allowedTools != "" {
		forkPrompt += fmt.Sprintf("\n\n[注意: 本 skill 限制可用工具为: %s]", allowedTools)
	}

	subTask, err := r.TaskRuntime.Start(task.TaskConfig{
		Type:         task.TypeSubagent,
		ParentID:     r.CurrentTaskID,
		Prompt:       forkPrompt,
		Isolation:    task.IsolationNone, // fork 共享工作目录
		ForkFrom:     r.CurrentTaskID,
		Timeout:      time.Duration(timeoutSec) * time.Second,
		CacheSafe:    cacheSafe,
		ForkDepth:    parentForkDepth + 1,
	})
	if err != nil {
		return &agent.StepOutcome{
			Data:       map[string]any{"status": "error", "msg": err.Error()},
			NextPrompt: fmt.Sprintf("Skill fork 失败: %v", err),
		}
	}

	<-subTask.Wait()

	result := "skill 执行完成"
	if subTask.State.Status == task.StatusFailed {
		result = fmt.Sprintf("skill 执行失败: %s", subTask.State.Error)
	} else if subTask.State.Status == task.StatusKilled {
		result = "skill 执行被中断"
	}

	// 读取子 agent 输出文件 (如有)
	if subTask.State.OutputFile != "" {
		if out, oErr := os.ReadFile(subTask.State.OutputFile); oErr == nil && len(out) > 0 {
			outStr := string(out)
			if len(outStr) > 8000 {
				outStr = outStr[:4000] + "\n...[truncated]...\n" + outStr[len(outStr)-4000:]
			}
			result = outStr
		}
	}

	return &agent.StepOutcome{
		Data:       map[string]any{"status": "success", "content": result, "skill": skillName, "type": "fork"},
		NextPrompt: fmt.Sprintf("Skill [fork] %s 结果:\n%s\n请基于此结果继续。", skillName, result),
	}
}

func substituteSkillVars(content string, args map[string]any, skillDir, skillName string) string {
	// 1. ${SKILL_DIR} -> skill 目录绝对路径
	content = strings.ReplaceAll(content, "${SKILL_DIR}", skillDir)
	content = strings.ReplaceAll(content, "${CLAUDE_SKILL_DIR}", skillDir)

	// 2. ${SKILL_NAME}
	content = strings.ReplaceAll(content, "${SKILL_NAME}", skillName)

	// 3. 解析 $ARGUMENTS 为参数数组, 支持 $ARGUMENTS[0], $1 等索引
	argStr := ""
	if s, ok := args["arguments"].(string); ok {
		argStr = s
	} else if s, ok := args["args"].(string); ok {
		argStr = s
	}
	parsedArgs := parseShellArgs(argStr)

	// 3a. $ARGUMENTS[0], $ARGUMENTS[1] 索引
	content = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`).ReplaceAllStringFunc(content, func(m string) string {
		sub := regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`).FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		idx, _ := strconv.Atoi(sub[1])
		if idx >= 0 && idx < len(parsedArgs) {
			return parsedArgs[idx]
		}
		return ""
	})

	// 3b. $0, $1 简写索引 (不匹配 $ARGUMENTS)
	content = regexp.MustCompile(`\$(\d+)(?!\w)`).ReplaceAllStringFunc(content, func(m string) string {
		sub := regexp.MustCompile(`\$(\d+)(?!\w)`).FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		idx, _ := strconv.Atoi(sub[1])
		if idx >= 0 && idx < len(parsedArgs) {
			return parsedArgs[idx]
		}
		return ""
	})

	// 3c. $ARGUMENTS -> 完整参数字符串
	content = strings.ReplaceAll(content, "$ARGUMENTS", argStr)

	// 4. 命名参数替换: $key 或 ${key} -> args[key]
	// 仅替换已知参数键, 避免误伤 markdown 中的 $ 符号
	for key, val := range args {
		// 跳过保留字段
		if key == "skill" || key == "action" || key == "file" || key == "arguments" || key == "args" {
			continue
		}
		strVal := fmt.Sprintf("%v", val)
		content = strings.ReplaceAll(content, "${"+key+"}", strVal)
		// $key 后跟空格或行尾, 避免误匹配 $keyXxx
		content = regexp.MustCompile(`\$`+regexp.QuoteMeta(key)+`([\s\W]|$)`).ReplaceAllString(content, strVal+"$1")
	}

	return content
}

// parseShellArgs 将参数字符串解析为参数数组 (简易 shell-quote)
func parseShellArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var args []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	hasContent := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && !inSingle && i+1 < len(s):
			// 转义下一字符
			cur.WriteByte(s[i+1])
			hasContent = true
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			hasContent = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			hasContent = true
		case (c == ' ' || c == '\t' || c == '\n') && !inSingle && !inDouble:
			if hasContent {
				args = append(args, cur.String())
				cur.Reset()
				hasContent = false
			}
		default:
			cur.WriteByte(c)
			hasContent = true
		}
	}
	if hasContent {
		args = append(args, cur.String())
	}
	return args
}

// executeInlineShell 在 SKILL.md 内容中执行内联 shell 命令并替换为输出
// 支持: !`command` (行内) 和 ```!\ncommand\n``` (代码块)
func (r *Router) executeInlineShell(content string, cwd string) string {
	// 1. 代码块: ```!\ncommand\n```
	blockRe := regexp.MustCompile("(?s)```!\\s*\\n(.*?)\\n?```")
	content = blockRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := blockRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		cmd := strings.TrimSpace(sub[1])
		out := r.runShellForInline(cmd, cwd)
		return out
	})

	// 2. 行内: !`command` (前面必须是行首或空白)
	inlineRe := regexp.MustCompile("(?m)(?:^|\\s)!`([^`]+)`")
	content = inlineRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := inlineRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		// 保留前导空白/行首
		prefix := ""
		if len(m) > 0 && (m[0] == ' ' || m[0] == '\t') {
			prefix = string(m[0])
		}
		cmd := strings.TrimSpace(sub[1])
		out := r.runShellForInline(cmd, cwd)
		// 行内输出压缩为单行
		out = strings.ReplaceAll(out, "\n", " ")
		return prefix + out
	})

	return content
}

// runShellForInline 执行单条 shell 命令, 返回输出 (stdout+stderr), 超时 30s
func (r *Router) runShellForInline(command, cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if isWindows() {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("[error: %v]", err)
	}
	out := stdout.String()
	if stderr.Len() > 0 && stdout.Len() == 0 {
		out = stderr.String()
	}
	// 限制输出长度
	if len(out) > 4000 {
		out = out[:2000] + "\n...[truncated]...\n" + out[len(out)-2000:]
	}
	return strings.TrimRight(out, "\n")
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

	realPath, err := resolveSymlinks(abs)
	if err == nil {
		abs = realPath
	}

	// 解析 r.Cwd 的符号链接/短名称 (Windows 8.3 短名称兼容)
	cwdResolved := r.Cwd
	if rp, err := resolveSymlinks(filepath.Clean(r.Cwd)); err == nil {
		cwdResolved = rp
	}

	if strings.HasPrefix(abs, cwdResolved) {
		return true
	}

	if r.SkillDir != "" {
		skillDirResolved := r.SkillDir
		if rp, err := resolveSymlinks(filepath.Clean(r.SkillDir)); err == nil {
			skillDirResolved = rp
		}
		if strings.HasPrefix(abs, skillDirResolved) {
			return true
		}
	}

	for _, dir := range r.AllowedDirs {
		if dir == "" {
			continue
		}
		dirResolved := dir
		if rp, err := resolveSymlinks(filepath.Clean(dir)); err == nil {
			dirResolved = rp
		}
		if strings.HasPrefix(abs, dirResolved) {
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

	realPath, err := resolveSymlinks(abs)
	if err == nil {
		abs = realPath
	}

	// 解析 r.Cwd 的符号链接/短名称 (Windows 8.3 短名称兼容)
	cwdResolved := r.Cwd
	if rp, err := resolveSymlinks(filepath.Clean(r.Cwd)); err == nil {
		cwdResolved = rp
	}

	if strings.HasPrefix(abs, cwdResolved) {
		return true
	}

	return false
}

func (r *Router) isCodeBlocked(code string, codeType string) (bool, string) {
	lowerCode := strings.ToLower(code)

	if codeType == "python" || codeType == "py" {
		normalized := normalizePythonCode(lowerCode)
		for _, pattern := range blockedDbPatterns {
			if strings.Contains(normalized, strings.ToLower(pattern)) {
				return true, "database access is not allowed"
			}
		}
		for _, pattern := range blockedCodePatterns {
			if strings.Contains(normalized, strings.ToLower(pattern)) {
				return true, "system-level code execution is not allowed (" + pattern + ")"
			}
		}
		for _, blocked := range blockedReadPaths {
			if strings.Contains(normalized, strings.ToLower(blocked)) {
				return true, "access to restricted path is not allowed (" + blocked + ")"
			}
		}
	}

	if codeType == "powershell" || codeType == "bash" || codeType == "sh" || codeType == "shell" {
		normalized := normalizeShellCode(lowerCode)
		for _, cmd := range blockedCommands {
			if strings.Contains(normalized, strings.ToLower(cmd)) {
				return true, "dangerous system command is not allowed (" + cmd + ")"
			}
		}

		exactBlockedCommands := []string{"env"}
		for _, cmd := range exactBlockedCommands {
			pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(cmd) + `\b`)
			if pattern.MatchString(normalized) {
				return true, "dangerous system command is not allowed (" + cmd + ")"
			}
		}

		for _, blocked := range blockedReadPaths {
			if strings.Contains(normalized, strings.ToLower(blocked)) {
				return true, "access to restricted path is not allowed (" + blocked + ")"
			}
		}
	}

	return false, ""
}

// ==================== 长任务工具 ====================

// doSpawnSubagent 启动子 agent 处理子任务
// 参考 cc-haha AgentTool: 支持 isolation (worktree) 和 fork (缓存共享)
// 增强: 递归 fork 守卫 + cache safe params + built-in agent 类型
func (r *Router) doSpawnSubagent(args map[string]any) *agent.StepOutcome {
	prompt := strArg(args, "prompt", "")
	if prompt == "" {
		return &agent.StepOutcome{
			Data:       "[Error] prompt required",
			NextPrompt: "子任务启动失败: 缺少 prompt 参数",
		}
	}
	timeoutSec := intArg(args, "timeout", 300)
	isolation := task.IsolationMode(strArg(args, "isolation", ""))
	cwdOverride := strArg(args, "cwd", "")
	builtinAgent := task.BuiltinAgentType(strArg(args, "agent_type", ""))

	if r.TaskRuntime == nil {
		return &agent.StepOutcome{
			Data:       "[Error] TaskRuntime not configured",
			NextPrompt: "子任务启动失败: 运行时未配置",
		}
	}

	// 递归 fork 守卫: 防止子 agent 再次 fork 导致栈溢出
	// 参考 cc-haha isInForkChild: 检测 boilerplate fork
	var parentForkDepth int
	if r.CurrentTaskID != "" {
		if parentTask, err := r.TaskRuntime.Get(r.CurrentTaskID); err == nil {
			parentForkDepth = parentTask.State.ForkDepth
		}
	}
	const maxForkDepth = 3 // 最大 fork 深度
	if parentForkDepth >= maxForkDepth {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] max fork depth %d exceeded", maxForkDepth),
			NextPrompt: fmt.Sprintf("子任务启动失败: 已达最大 fork 深度 %d, 避免递归 fork", maxForkDepth),
		}
	}

	// 构造 cache safe params (与父任务对齐以共享缓存前缀)
	// 参考 cc-haha CacheSafeParams
	var cacheSafe *task.CacheSafeParams
	if r.CurrentTaskID != "" {
		if parentTask, err := r.TaskRuntime.Get(r.CurrentTaskID); err == nil && parentTask.Agent != nil {
			cacheSafe = &task.CacheSafeParams{
				Model:        parentTask.Agent.Model,
				Temperature:  parentTask.Agent.Temperature,
				MaxTokens:    parentTask.Agent.MaxTokens,
				SystemPrompt: parentTask.Agent.SystemPrompt,
			}
		}
	}

	// 启动子任务
	subTask, err := r.TaskRuntime.Start(task.TaskConfig{
		Type:         task.TypeSubagent,
		ParentID:     r.CurrentTaskID,
		Prompt:       prompt,
		Isolation:    isolation,
		CwdOverride:  cwdOverride,
		ForkFrom:     r.CurrentTaskID, // fork 共享缓存前缀
		Timeout:      time.Duration(timeoutSec) * time.Second,
		BuiltinAgent: builtinAgent,
		CacheSafe:    cacheSafe,
		ForkDepth:    parentForkDepth + 1,
	})
	if err != nil {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] %v", err),
			NextPrompt: fmt.Sprintf("子任务启动失败: %v", err),
		}
	}

	// 等待子任务完成 (timeout 已由 Runtime 管理, 这里仅等待)
	<-subTask.Wait()

	// 读取子任务输出
	result := "子任务已完成"
	if subTask.State.Status == task.StatusFailed {
		result = fmt.Sprintf("子任务失败: %s", subTask.State.Error)
	} else if subTask.State.Status == task.StatusKilled {
		result = "子任务被中断"
	}

	// worktree 隔离时, 提示变更位置
	if subTask.State.WorktreePath != "" {
		result += fmt.Sprintf("\n[worktree 路径: %s]", subTask.State.WorktreePath)
	}

	return &agent.StepOutcome{
		Data:       result,
		NextPrompt: fmt.Sprintf("子任务结果: %s。请基于此结果继续。", result),
	}
}

// doSpawnTeammate 启动异步 teammate (非阻塞)
// 参考 cc-haha spawnTeammate: 异步协作, 通过 SendMessage 通信
func (r *Router) doSpawnTeammate(args map[string]any) *agent.StepOutcome {
	prompt := strArg(args, "prompt", "")
	if prompt == "" {
		return &agent.StepOutcome{
			Data:       "[Error] prompt required",
			NextPrompt: "teammate 启动失败: 缺少 prompt 参数",
		}
	}
	name := strArg(args, "name", "")
	if name == "" {
		return &agent.StepOutcome{
			Data:       "[Error] name required",
			NextPrompt: "teammate 启动失败: 缺少 name 参数 (用于 SendMessage 寻址)",
		}
	}
	teamName := strArg(args, "team_name", "")
	isolation := task.IsolationMode(strArg(args, "isolation", ""))

	if r.TaskRuntime == nil {
		return &agent.StepOutcome{
			Data:       "[Error] TaskRuntime not configured",
			NextPrompt: "teammate 启动失败: 运行时未配置",
		}
	}

	teammateTask, err := r.TaskRuntime.Start(task.TaskConfig{
		Type:      task.TypeTeammate,
		ParentID:  r.CurrentTaskID,
		Prompt:    prompt,
		AgentName: name,
		TeamName:  teamName,
		Isolation: isolation,
	})
	if err != nil {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] %v", err),
			NextPrompt: fmt.Sprintf("teammate 启动失败: %v", err),
		}
	}

	return &agent.StepOutcome{
		Data:       fmt.Sprintf("[Teammate %s 已启动, taskID: %s]", name, teammateTask.State.ID),
		NextPrompt: fmt.Sprintf("teammate %s 已在后台运行, 可通过 send_message 向其发送消息。taskID: %s", name, teammateTask.State.ID),
	}
}

// doSendMessage 跨 agent 发送消息
// 参考 cc-haha SendMessage 工具
func (r *Router) doSendMessage(args map[string]any) *agent.StepOutcome {
	to := strArg(args, "to", "")
	content := strArg(args, "content", "")
	if to == "" || content == "" {
		return &agent.StepOutcome{
			Data:       "[Error] to and content required",
			NextPrompt: "消息发送失败: 缺少 to 或 content 参数",
		}
	}

	if r.TaskRuntime == nil {
		return &agent.StepOutcome{
			Data:       "[Error] TaskRuntime not configured",
			NextPrompt: "消息发送失败: 运行时未配置",
		}
	}

	// 发送者名称: 当前任务的 AgentName, 或默认 "main"
	from := "main"
	if r.CurrentTaskID != "" {
		if t, err := r.TaskRuntime.Get(r.CurrentTaskID); err == nil && t.State.AgentName != "" {
			from = t.State.AgentName
		}
	}

	if err := r.TaskRuntime.SendMessage(from, to, content); err != nil {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] %v", err),
			NextPrompt: fmt.Sprintf("消息发送失败: %v", err),
		}
	}

	return &agent.StepOutcome{
		Data:       fmt.Sprintf("[消息已发送给 %s]", to),
		NextPrompt: fmt.Sprintf("消息已发送给 %s", to),
	}
}

// doExitPlanMode 计划模式: 提交计划等待审批
// 参考 cc-haha ExitPlanModeTool: 提交计划 -> 暂停 -> 用户审批 -> 继续/终止
func (r *Router) doExitPlanMode(args map[string]any) *agent.StepOutcome {
	plan := strArg(args, "plan", "")
	if plan == "" {
		return &agent.StepOutcome{
			Data:       "[Error] plan required",
			NextPrompt: "请提供计划内容",
		}
	}

	// 通过 PlanSubmit 字段触发 agent loop 的审批流程
	// loop 会发出 DisplayItem{Source:"plan_submit"} 并阻塞等待 Runtime.Resume
	return &agent.StepOutcome{
		Data:       plan,
		NextPrompt: "计划已提交，等待审批。",
		PlanSubmit: plan,
	}
}

// doSetGoal 设置或更新目标
// 参考 cc-haha goalState.ts: 支持 set/pause/resume/complete/clear 操作
func (r *Router) doSetGoal(args map[string]any) *agent.StepOutcome {
	goal := strArg(args, "goal", "")
	action := strArg(args, "action", "set") // set/pause/resume/complete/clear

	// 获取当前 agent 的 GoalTracker (通过 TaskRuntime)
	if r.TaskRuntime == nil || r.CurrentTaskID == "" {
		return &agent.StepOutcome{
			Data:       "[Error] TaskRuntime not configured",
			NextPrompt: "目标设置失败: 运行时未配置",
		}
	}

	t, err := r.TaskRuntime.Get(r.CurrentTaskID)
	if err != nil {
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] %v", err),
			NextPrompt: fmt.Sprintf("目标设置失败: %v", err),
		}
	}

	tracker := t.Agent.GoalTracker

	switch action {
	case "set":
		if goal == "" {
			return &agent.StepOutcome{
				Data:       "[Error] goal required",
				NextPrompt: "请提供目标内容",
			}
		}
		t.Agent.GoalTracker = agent.NewGoalTracker(goal)
		t.Agent.Goal = goal
		t.State.Goal = goal
		r.TaskRuntime.SaveState(t.State)
		return &agent.StepOutcome{
			Data:       goal,
			NextPrompt: fmt.Sprintf("目标已设置: %s。后续操作将围绕此目标进行。", goal),
		}

	case "pause":
		if tracker == nil {
			return &agent.StepOutcome{Data: "[Error] no active goal", NextPrompt: "无活跃目标可暂停"}
		}
		tracker.Pause()
		return &agent.StepOutcome{
			Data:       tracker.StatusReport(),
			NextPrompt: fmt.Sprintf("目标已暂停: %s", tracker.Objective()),
		}

	case "resume":
		if tracker == nil {
			return &agent.StepOutcome{Data: "[Error] no goal", NextPrompt: "无目标可恢复"}
		}
		tracker.Resume()
		return &agent.StepOutcome{
			Data:       tracker.StatusReport(),
			NextPrompt: fmt.Sprintf("目标已恢复: %s", tracker.Objective()),
		}

	case "complete":
		if tracker == nil {
			return &agent.StepOutcome{Data: "[Error] no goal", NextPrompt: "无目标可完成"}
		}
		reason := strArg(args, "reason", "已完成")
		tracker.Complete(reason)
		return &agent.StepOutcome{
			Data:       tracker.StatusReport(),
			NextPrompt: fmt.Sprintf("目标已完成: %s (%s)", tracker.Objective(), reason),
		}

	case "clear":
		t.Agent.GoalTracker = nil
		t.Agent.Goal = ""
		t.State.Goal = ""
		r.TaskRuntime.SaveState(t.State)
		return &agent.StepOutcome{
			Data:       "",
			NextPrompt: "目标已清除",
		}

	default:
		return &agent.StepOutcome{
			Data:       fmt.Sprintf("[Error] unknown action: %s", action),
			NextPrompt: fmt.Sprintf("未知操作: %s (支持 set/pause/resume/complete/clear)", action),
		}
	}
}

// doUpdateTodo 更新任务清单
func (r *Router) doUpdateTodo(args map[string]any) *agent.StepOutcome {
	todosRaw, ok := args["todos"]
	if !ok {
		return &agent.StepOutcome{
			Data:       "[Error] todos required",
			NextPrompt: "请提供 todos 数组",
		}
	}

	todosJSON, _ := json.Marshal(todosRaw)
	return &agent.StepOutcome{
		Data:       string(todosJSON),
		NextPrompt: fmt.Sprintf("任务清单已更新:\n%s\n\n请继续执行下一项任务。", string(todosJSON)),
	}
}

func normalizePythonCode(code string) string {
	normalized := strings.ReplaceAll(code, `\n`, "")
	normalized = strings.ReplaceAll(normalized, `\t`, "")
	normalized = strings.ReplaceAll(normalized, `\r`, "")
	normalized = strings.ReplaceAll(normalized, `' + '`, "")
	normalized = strings.ReplaceAll(normalized, `" + "`, "")
	normalized = strings.ReplaceAll(normalized, `'+ '`, "")
	normalized = strings.ReplaceAll(normalized, `"+ "`, "")
	normalized = strings.ReplaceAll(normalized, `'+'`, "")
	normalized = strings.ReplaceAll(normalized, `"+"`, "")
	normalized = strings.ReplaceAll(normalized, `  `, " ")
	var result strings.Builder
	for _, ch := range normalized {
		if ch != '\\' {
			result.WriteRune(ch)
		}
	}
	return result.String()
}

func normalizeShellCode(code string) string {
	normalized := strings.ReplaceAll(code, `\`, "")
	normalized = strings.ReplaceAll(normalized, `'`, "")
	normalized = strings.ReplaceAll(normalized, `"`, "")
	normalized = strings.ReplaceAll(normalized, `$()`, "")
	normalized = strings.ReplaceAll(normalized, `$()`, "")
	normalized = regexp.MustCompile(`\$\{[^}]*\}`).ReplaceAllString(normalized, "")
	normalized = strings.ReplaceAll(normalized, `  `, " ")
	return normalized
}

func isPathBlockedRead(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	realPath, err := resolveSymlinks(abs)
	if err == nil {
		abs = realPath
	}
	for _, blocked := range blockedReadPaths {
		if strings.HasPrefix(abs, blocked) {
			return true
		}
	}
	return false
}

func isPathBlockedWrite(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	realPath, err := resolveSymlinks(abs)
	if err == nil {
		abs = realPath
	}
	for _, blocked := range blockedWritePaths {
		if strings.HasPrefix(abs, blocked) {
			return true
		}
	}
	return false
}

func isOtherUserDir(path string, currentUserDir string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	realPath, err := resolveSymlinks(abs)
	if err == nil {
		abs = realPath
	}
	// 解析 currentUserDir 的符号链接/短名称 (Windows 8.3 短名称兼容)
	cleanUserDir := filepath.Clean(currentUserDir)
	if rp, err := resolveSymlinks(cleanUserDir); err == nil {
		cleanUserDir = rp
	}
	parentDir := filepath.Dir(cleanUserDir)
	if !strings.HasPrefix(abs, parentDir+string(filepath.Separator)) {
		return false
	}
	rel, err := filepath.Rel(parentDir, abs)
	if err != nil {
		return true
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 {
		return true
	}
	otherUserDir := filepath.Join(parentDir, parts[0])
	return otherUserDir != cleanUserDir
}

func resolveSymlinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// 路径不存在时, 尝试解析父目录 (文件尚未创建的写入场景)
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		if dirResolved, dirErr := filepath.EvalSymlinks(dir); dirErr == nil {
			return filepath.Clean(filepath.Join(dirResolved, base)), nil
		}
		return path, err
	}
	return filepath.Clean(resolved), nil
}
