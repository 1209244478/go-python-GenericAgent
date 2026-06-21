package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// WorktreeManager 管理 git worktree 隔离
// 参考 cc-haha utils/worktree.ts: createAgentWorktree / removeAgentWorktree
//
// worktree 优势:
//   - 子 agent 在独立工作树操作, 不影响主仓库
//   - 完成后可合并或丢弃变更
//   - 共享 .git 对象库, 节省磁盘
type WorktreeManager struct {
	mu       sync.Mutex
	baseDir  string // worktree 根目录 (如 .ga/worktrees)
	registry map[string]string // taskID -> worktree path
}

// NewWorktreeManager 创建 worktree 管理器
func NewWorktreeManager(baseDir string) *WorktreeManager {
	if baseDir == "" {
		baseDir = ".ga/worktrees"
	}
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = baseDir
	}
	return &WorktreeManager{
		baseDir:  abs,
		registry: make(map[string]string),
	}
}

// CreateWorktree 为任务创建 git worktree
// repoRoot: 主仓库根目录
// taskID: 任务ID (用作分支名后缀)
// returns: worktree 路径, 清理函数, error
func (w *WorktreeManager) CreateWorktree(repoRoot, taskID string) (string, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 确保 baseDir 存在
	if err := os.MkdirAll(w.baseDir, 0755); err != nil {
		return "", nil, fmt.Errorf("create worktree base dir: %w", err)
	}

	// 检查 repoRoot 是否是 git 仓库
	gitDir := filepath.Join(repoRoot, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return "", nil, fmt.Errorf("not a git repo: %s", repoRoot)
	}

	// 生成分支名和 worktree 路径
	branchName := fmt.Sprintf("ga-task/%s", taskID)
	wtPath := filepath.Join(w.baseDir, taskID)

	// 创建 worktree (基于当前 HEAD)
	// git worktree add -b <branch> <path> HEAD
	cmd := exec.Command("git", "worktree", "add", "-b", branchName, wtPath, "HEAD")
	cmd.Dir = repoRoot
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("git worktree add: %w", err)
	}

	// 注册
	w.registry[taskID] = wtPath

	// 清理函数
	cleanup := func() {
		w.RemoveWorktree(repoRoot, taskID)
	}

	return wtPath, cleanup, nil
}

// RemoveWorktree 移除 worktree 和对应分支
func (w *WorktreeManager) RemoveWorktree(repoRoot, taskID string) error {
	w.mu.Lock()
	wtPath, ok := w.registry[taskID]
	delete(w.registry, taskID)
	w.mu.Unlock()

	if !ok {
		return nil // 未注册, 忽略
	}

	branchName := fmt.Sprintf("ga-task/%s", taskID)

	// git worktree remove --force <path>
	cmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
	cmd.Dir = repoRoot
	_ = cmd.Run() // 忽略错误 (可能已被删除)

	// git branch -D <branch>
	cmd = exec.Command("git", "branch", "-D", branchName)
	cmd.Dir = repoRoot
	_ = cmd.Run()

	return nil
}

// GetWorktreePath 获取任务的 worktree 路径
func (w *WorktreeManager) GetWorktreePath(taskID string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.registry[taskID]
}

// HasWorktreeChanges 检查 worktree 是否有未提交变更
// 参考 cc-haha hasWorktreeChanges
func (w *WorktreeManager) HasWorktreeChanges(taskID string) (bool, error) {
	w.mu.Lock()
	wtPath := w.registry[taskID]
	w.mu.Unlock()

	if wtPath == "" {
		return false, fmt.Errorf("worktree not found for task %s", taskID)
	}

	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = wtPath
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// ListWorktrees 列出所有 worktree
func (w *WorktreeManager) ListWorktrees() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]string, len(w.registry))
	for k, v := range w.registry {
		result[k] = v
	}
	return result
}

// FindRepoRoot 从给定路径向上查找 git 仓库根目录
func FindRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
