package task

import (
	"time"

	"github.com/genericagent/ga/internal/llm"
)

// Status 任务状态
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused" // 等待计划审批
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusKilled    Status = "killed"
)

// IsTerminal 判断是否终态
func IsTerminal(s Status) bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusKilled
}

// Type 任务类型
type Type string

const (
	TypeMain      Type = "main"      // 主会话任务
	TypeSubagent  Type = "subagent"  // fork 子任务 (同步阻塞)
	TypeTeammate  Type = "teammate"  // 异步协作 agent (非阻塞)
	TypeRemote    Type = "remote"    // 远程任务(占位)
	TypeMonitor   Type = "monitor"   // 监控任务
)

// IsolationMode 隔离模式
// 参考 cc-haha AgentTool.tsx:99 isolation 参数
type IsolationMode string

const (
	IsolationNone     IsolationMode = ""         // 无隔离, 共享当前工作目录
	IsolationWorktree IsolationMode = "worktree" // git worktree 隔离
)

// BuiltinAgentType 预定义 agent 类型
// 参考 cc-haha built-in agents: general-purpose / plan / explore / verification
type BuiltinAgentType string

const (
	BuiltinNone         BuiltinAgentType = ""                // 默认通用 agent
	BuiltinGeneral      BuiltinAgentType = "general-purpose" // 通用任务
	BuiltinExplore      BuiltinAgentType = "explore"         // 探索/调研 (只读)
	BuiltinPlan         BuiltinAgentType = "plan"            // 计划制定
	BuiltinVerification BuiltinAgentType = "verification"    // 验证/测试
)

// CacheSafeParams 缓存安全参数
// 参考 cc-haha forkedAgent.ts: CacheSafeParams
// fork 子任务时, 这些参数与父任务对齐, 以共享 LLM 缓存前缀
// 任何不同都会导致缓存失效
type CacheSafeParams struct {
	Model       string  `json:"model"`        // 必须与父任务一致
	SystemPrompt string `json:"system_prompt"` // 必须与父任务一致
	Temperature float64 `json:"temperature"`  // 必须与父任务一致
	MaxTokens   int     `json:"max_tokens"`   // 必须与父任务一致
}

// TokenUsage token 用量
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// TaskState 持久化到磁盘的任务状态
type TaskState struct {
	ID          string        `json:"id"`
	Type        Type          `json:"type"`
	Status      Status        `json:"status"`
	ParentID    string        `json:"parent_id,omitempty"`
	UserID      int64         `json:"user_id"`
	SessionID   int64         `json:"session_id"`
	Prompt      string        `json:"prompt"`
	Description string        `json:"description"`
	StartTime   time.Time     `json:"start_time"`
	EndTime     *time.Time    `json:"end_time,omitempty"`
	TurnCount   int           `json:"turn_count"`
	TokenUsage  TokenUsage    `json:"token_usage"`
	OutputFile  string        `json:"output_file"`
	Error       string        `json:"error,omitempty"`
	Goal        string        `json:"goal,omitempty"`
	PlanMode    bool          `json:"plan_mode"`
	PendingPlan string        `json:"pending_plan,omitempty"`

	// 子任务编排增强字段
	Isolation    IsolationMode `json:"isolation,omitempty"`     // 隔离模式
	WorktreePath string        `json:"worktree_path,omitempty"` // worktree 路径 (隔离时)
	CwdOverride  string        `json:"cwd_override,omitempty"`  // 工作目录覆盖
	ForkFrom     string        `json:"fork_from,omitempty"`     // fork 来源任务ID (缓存共享)
	AgentName    string        `json:"agent_name,omitempty"`    // teammate 名称 (用于 SendMessage 寻址)
	TeamName     string        `json:"team_name,omitempty"`     // 团队名称

	// 新增字段
	BuiltinAgent BuiltinAgentType `json:"builtin_agent,omitempty"` // 预定义 agent 类型
	CacheSafe    *CacheSafeParams `json:"cache_safe,omitempty"`    // 缓存安全参数 (fork 时对齐)
	ForkDepth    int              `json:"fork_depth,omitempty"`    // fork 深度 (递归守卫)
}

// TaskConfig 启动任务的配置
type TaskConfig struct {
	Type      Type
	ParentID  string
	UserID    int64
	SessionID int64
	Prompt    string
	Goal      string
	PlanMode  bool
	History   []llm.Message // 历史消息(恢复用)

	// 子任务编排增强
	Isolation   IsolationMode // 隔离模式
	CwdOverride string        // 工作目录覆盖
	ForkFrom    string        // fork 来源 (共享缓存前缀)
	AgentName   string        // teammate 名称
	TeamName    string        // 团队名
	Timeout     time.Duration // 任务级超时 (0=无限)

	// 新增
	BuiltinAgent BuiltinAgentType // 预定义 agent 类型
	CacheSafe    *CacheSafeParams // 缓存安全参数
	ForkDepth    int              // fork 深度
}

// MessageEnvelope 跨 agent 消息信封
// 参考 cc-haha SendMessage 工具
type MessageEnvelope struct {
	From    string    `json:"from"`    // 发送者 agent 名称
	To      string    `json:"to"`      // 接收者 agent 名称 (或 "all")
	Content string    `json:"content"` // 消息内容
	SentAt  time.Time `json:"sent_at"`
}
