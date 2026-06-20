package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/llm"
)

// Task 运行中的任务实例
type Task struct {
	State        *TaskState
	Agent        *agent.Agent
	Cancel       context.CancelFunc
	ctx          context.Context
	mu           sync.RWMutex
	subscribers  map[chan agent.DisplayItem]bool
	done         chan struct{}
	planApproval chan bool // 计划审批信号
}

// Runtime 任务运行时，管理所有活跃任务
type Runtime struct {
	store     *Store
	mu        sync.RWMutex
	tasks     map[string]*Task
	agentFactory AgentFactory
}

// AgentFactory 创建 agent 的工厂函数(避免循环依赖)
type AgentFactory func(config AgentConfig) *agent.Agent

// AgentConfig 创建 agent 的配置
type AgentConfig struct {
	UserID    int64
	TaskID    string
	Goal      string
	PlanMode  bool
	History   []llm.Message
}

// NewRuntime 创建运行时
func NewRuntime(store *Store, factory AgentFactory) *Runtime {
	return &Runtime{
		store:        store,
		tasks:        make(map[string]*Task),
		agentFactory: factory,
	}
}

// Start 启动新任务
func (r *Runtime) Start(cfg TaskConfig) (*Task, error) {
	taskID := generateTaskID(cfg.Type)
	now := time.Now()

	state := &TaskState{
		ID:          taskID,
		Type:        cfg.Type,
		Status:      StatusRunning,
		ParentID:    cfg.ParentID,
		UserID:      cfg.UserID,
		SessionID:   cfg.SessionID,
		Prompt:      cfg.Prompt,
		Description: truncate(cfg.Prompt, 80),
		StartTime:   now,
		OutputFile:  "output.log",
		Goal:        cfg.Goal,
		PlanMode:    cfg.PlanMode,
	}

	// 创建 agent
	a := r.agentFactory(AgentConfig{
		UserID:   cfg.UserID,
		TaskID:   taskID,
		Goal:     cfg.Goal,
		PlanMode: cfg.PlanMode,
		History:  cfg.History,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t := &Task{
		State:        state,
		Agent:        a,
		Cancel:       cancel,
		ctx:          ctx,
		subscribers:  make(map[chan agent.DisplayItem]bool),
		done:         make(chan struct{}),
		planApproval: make(chan bool, 1),
	}

	// 注册到运行时
	r.mu.Lock()
	r.tasks[taskID] = t
	r.mu.Unlock()

	// 持久化初始状态
	r.store.Save(state)

	// 启动执行 goroutine
	go r.runTask(t, cfg)

	return t, nil
}

// runTask 执行任务主循环
func (r *Runtime) runTask(t *Task, cfg TaskConfig) {
	defer close(t.done)
	defer func() {
		r.mu.Lock()
		// 任务结束后保留在 registry 中供查询，但标记终态
		r.mu.Unlock()
	}()

	// 启动 agent
	ch := t.Agent.Run(cfg.Prompt, "task", cfg.History)

	var finalContent string
	var lastMessages []llm.Message

	for item := range ch {
		// 广播给所有订阅者
		r.broadcast(t, item)

		// 持久化输出
		if item.Content != "" {
			r.store.AppendOutput(t.State.UserID, t.State.ID, item.Content+"\n")
		}

		// 收集最终内容
		if item.Done {
			if item.Content != "" {
				finalContent = item.Content
			}
		}

		// 更新 turn 计数
		if item.Turn > t.State.TurnCount {
			t.State.TurnCount = item.Turn
		}
	}

	// 检查是否被取消
	select {
	case <-t.ctx.Done():
		t.State.Status = StatusKilled
		t.State.Error = "aborted by user"
	default:
		if finalContent == "" {
			t.State.Status = StatusFailed
			t.State.Error = "no output"
		} else {
			t.State.Status = StatusCompleted
		}
	}

	now := time.Now()
	t.State.EndTime = &now

	// 保存消息历史(用于恢复)
	if lastMessages != nil {
		r.store.SaveMessages(t.State.UserID, t.State.ID, lastMessages)
	}

	// 持久化最终状态
	r.store.Save(t.State)

	// 广播结束信号
	r.broadcast(t, agent.DisplayItem{Done: true, Source: "task_end"})
}

// broadcast 广播输出给所有订阅者
func (r *Runtime) broadcast(t *Task, item agent.DisplayItem) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for ch := range t.subscribers {
		select {
		case ch <- item:
		default:
			// 订阅者缓冲区满，跳过(避免阻塞)
		}
	}
}

// Subscribe 订阅任务输出
func (r *Runtime) Subscribe(taskID string) (<-chan agent.DisplayItem, func(), error) {
	r.mu.RLock()
	t, ok := r.tasks[taskID]
	r.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("task not found: %s", taskID)
	}

	ch := make(chan agent.DisplayItem, 64)
	t.mu.Lock()
	t.subscribers[ch] = true
	t.mu.Unlock()

	unsub := func() {
		t.mu.Lock()
		delete(t.subscribers, ch)
		t.mu.Unlock()
		close(ch)
	}

	return ch, unsub, nil
}

// Abort 中断任务
func (r *Runtime) Abort(taskID string) error {
	r.mu.RLock()
	t, ok := r.tasks[taskID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	t.Cancel()
	t.Agent.Abort()
	return nil
}

// Resume 恢复暂停的任务(计划审批后)
func (r *Runtime) Resume(taskID string, approved bool) error {
	r.mu.RLock()
	t, ok := r.tasks[taskID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	select {
	case t.planApproval <- approved:
		return nil
	default:
		return fmt.Errorf("task not waiting for approval")
	}
}

// Get 获取任务
func (r *Runtime) Get(taskID string) (*Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	return t, nil
}

// ListByUser 列出用户所有任务
func (r *Runtime) ListByUser(userID int64) ([]*TaskState, error) {
	return r.store.ListByUser(userID)
}

// Wait 等待任务完成
func (t *Task) Wait() <-chan struct{} {
	return t.done
}

// Restore 恢复未完成的任务(服务重启时调用)
// 将所有 running 状态的任务标记为 failed
func (r *Runtime) Restore() error {
	userIDs, err := r.store.ListAllUsers()
	if err != nil {
		return err
	}
	for _, uid := range userIDs {
		states, err := r.store.ListByUser(uid)
		if err != nil {
			continue
		}
		for _, state := range states {
			if state.Status == StatusRunning || state.Status == StatusPaused {
				state.Status = StatusFailed
				state.Error = "interrupted by server restart"
				now := time.Now()
				state.EndTime = &now
				r.store.Save(state)
			}
		}
	}
	return nil
}

// generateTaskID 生成任务ID
func generateTaskID(t Type) string {
	prefix := "x"
	switch t {
	case TypeMain:
		prefix = "m"
	case TypeSubagent:
		prefix = "s"
	case TypeRemote:
		prefix = "r"
	}
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
