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
	TypeMain     Type = "main"     // 主会话任务
	TypeSubagent Type = "subagent" // fork 子任务
	TypeRemote   Type = "remote"   // 远程任务(占位)
)

// TokenUsage token 用量
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// TaskState 持久化到磁盘的任务状态
type TaskState struct {
	ID          string     `json:"id"`
	Type        Type       `json:"type"`
	Status      Status     `json:"status"`
	ParentID    string     `json:"parent_id,omitempty"`
	UserID      int64      `json:"user_id"`
	SessionID   int64      `json:"session_id"`
	Prompt      string     `json:"prompt"`
	Description string     `json:"description"`
	StartTime   time.Time  `json:"start_time"`
	EndTime     *time.Time `json:"end_time,omitempty"`
	TurnCount   int        `json:"turn_count"`
	TokenUsage  TokenUsage `json:"token_usage"`
	OutputFile  string     `json:"output_file"`
	Error       string     `json:"error,omitempty"`
	Goal        string     `json:"goal,omitempty"`
	PlanMode    bool       `json:"plan_mode"`
	PendingPlan string     `json:"pending_plan,omitempty"`
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
}
