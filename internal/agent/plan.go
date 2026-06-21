package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PlanFile 计划文件持久化
// 参考 cc-haha getPlanFilePath + plan.md
// 计划提交后持久化到磁盘, 供用户查看/编辑/恢复
type PlanFile struct {
	mu       sync.RWMutex
	filePath string // 计划文件路径
	content  string // 计划内容
	approved bool   // 是否已审批
	createdAt time.Time
	updatedAt time.Time
}

// NewPlanFile 创建计划文件
func NewPlanFile(baseDir, taskID string) *PlanFile {
	var path string
	if baseDir != "" && taskID != "" {
		dir := filepath.Join(baseDir, "plans")
		_ = os.MkdirAll(dir, 0755)
		path = filepath.Join(dir, fmt.Sprintf("plan-%s.md", taskID))
	}
	return &PlanFile{
		filePath:  path,
		createdAt: time.Now(),
		updatedAt: time.Now(),
	}
}

// Save 保存计划到文件
func (pf *PlanFile) Save(content string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	pf.content = content
	pf.updatedAt = time.Now()

	if pf.filePath == "" {
		return nil
	}

	planContent := fmt.Sprintf("# 执行计划\n\n> 创建时间: %s\n> 最后更新: %s\n\n---\n\n%s\n",
		pf.createdAt.Format("2006-01-02 15:04:05"),
		pf.updatedAt.Format("2006-01-02 15:04:05"),
		content,
	)

	return os.WriteFile(pf.filePath, []byte(planContent), 0644)
}

// Load 从文件加载计划
func (pf *PlanFile) Load() (string, error) {
	pf.mu.RLock()
	defer pf.mu.RUnlock()

	if pf.filePath == "" {
		return pf.content, nil
	}

	data, err := os.ReadFile(pf.filePath)
	if err != nil {
		return pf.content, err
	}
	return string(data), nil
}

// MarkApproved 标记计划已审批
func (pf *PlanFile) MarkApproved() {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.approved = true
	pf.updatedAt = time.Now()
}

// IsApproved 检查是否已审批
func (pf *PlanFile) IsApproved() bool {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.approved
}

// GetPath 获取计划文件路径
func (pf *PlanFile) GetPath() string {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.filePath
}

// AllowedPrompts 计划中允许执行的命令权限
// 参考 cc-haha allowedPromptSchema
// 计划审批时, 用户可以预先批准某些命令, agent 执行时无需再次确认
type AllowedPrompts struct {
	mu      sync.RWMutex
	allowed map[string]bool // 命令前缀 -> 是否允许
}

// NewAllowedPrompts 创建允许的命令列表
func NewAllowedPrompts() *AllowedPrompts {
	return &AllowedPrompts{
		allowed: make(map[string]bool),
	}
}

// Allow 添加允许的命令前缀
func (ap *AllowedPrompts) Allow(prefix string) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	ap.allowed[strings.TrimSpace(prefix)] = true
}

// IsAllowed 检查命令是否被允许
func (ap *AllowedPrompts) IsAllowed(command string) bool {
	ap.mu.RLock()
	defer ap.mu.RUnlock()

	command = strings.TrimSpace(command)
	for prefix := range ap.allowed {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

// List 列出所有允许的命令前缀
func (ap *AllowedPrompts) List() []string {
	ap.mu.RLock()
	defer ap.mu.RUnlock()
	result := make([]string, 0, len(ap.allowed))
	for prefix := range ap.allowed {
		result = append(result, prefix)
	}
	return result
}

// ParseAllowedPromptsFromPlan 从计划文本中解析允许的命令
// 计划格式:
// ## 允许的命令
// - git status
// - git diff
// - npm run build
func ParseAllowedPromptsFromPlan(plan string) *AllowedPrompts {
	ap := NewAllowedPrompts()

	lines := strings.Split(plan, "\n")
	inAllowedSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 检测 "允许的命令" 章节
		if strings.HasPrefix(trimmed, "#") {
			inAllowedSection = strings.Contains(trimmed, "允许的命令") ||
				strings.Contains(trimmed, "allowed") ||
				strings.Contains(trimmed, "Allowed")
			continue
		}

		if !inAllowedSection {
			continue
		}

		// 解析列表项
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") {
			cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "-"), "*"))
			if cmd != "" {
				ap.Allow(cmd)
			}
		}
	}

	return ap
}
