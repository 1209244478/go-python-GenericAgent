package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	// teammate 通信
	inbox   chan MessageEnvelope // 接收其他 agent 的消息
	cleanup func()                // worktree 清理函数

	// 超时控制
	timeoutTimer *time.Timer
}

// Runtime 任务运行时，管理所有活跃任务
type Runtime struct {
	store         *Store
	mu            sync.RWMutex
	tasks         map[string]*Task
	agentFactory  AgentFactory
	worktreeMgr   *WorktreeManager
	messageRouter *MessageRouter // 跨 agent 消息路由
}

// AgentFactory 创建 agent 的工厂函数(避免循环依赖)
type AgentFactory func(config AgentConfig) *agent.Agent

// AgentConfig 创建 agent 的配置
type AgentConfig struct {
	UserID      int64
	TaskID      string
	Goal        string
	PlanMode    bool
	History     []llm.Message
	CwdOverride string // 工作目录覆盖 (worktree 隔离时)
}

// NewRuntime 创建运行时
func NewRuntime(store *Store, factory AgentFactory) *Runtime {
	r := &Runtime{
		store:        store,
		tasks:        make(map[string]*Task),
		agentFactory: factory,
	}
	r.worktreeMgr = NewWorktreeManager("")
	r.messageRouter = NewMessageRouter()
	return r
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
		Isolation:   cfg.Isolation,
		CwdOverride: cfg.CwdOverride,
		ForkFrom:    cfg.ForkFrom,
		AgentName:   cfg.AgentName,
		TeamName:    cfg.TeamName,
	}

	// worktree 隔离处理
	var cleanup func()
	if cfg.Isolation == IsolationWorktree {
		repoRoot := FindRepoRoot(cfg.CwdOverride)
		if repoRoot == "" {
			repoRoot, _ = os.Getwd()
		}
		wtPath, cln, err := r.worktreeMgr.CreateWorktree(repoRoot, taskID)
		if err != nil {
			// worktree 创建失败, 降级为无隔离
			state.Isolation = IsolationNone
		} else {
			state.WorktreePath = wtPath
			state.CwdOverride = wtPath
			cfg.CwdOverride = wtPath
			cleanup = cln
		}
	}

	// 创建 agent
	a := r.agentFactory(AgentConfig{
		UserID:      cfg.UserID,
		TaskID:      taskID,
		Goal:        cfg.Goal,
		PlanMode:    cfg.PlanMode,
		History:     cfg.History,
		CwdOverride: cfg.CwdOverride,
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
		inbox:        make(chan MessageEnvelope, 16),
		cleanup:      cleanup,
	}

	// 任务级超时
	if cfg.Timeout > 0 {
		t.timeoutTimer = time.AfterFunc(cfg.Timeout, func() {
			r.Abort(taskID)
			r.broadcast(t, agent.DisplayItem{
				Turn:    t.State.TurnCount,
				Content: fmt.Sprintf("⏰ 任务超时 (%s)，已自动中断", cfg.Timeout),
				Source:  "timeout",
			})
		})
	}

	// 注册到运行时
	r.mu.Lock()
	r.tasks[taskID] = t
	r.mu.Unlock()

	// 注册到消息路由 (teammate 可寻址)
	if cfg.AgentName != "" {
		r.messageRouter.Register(cfg.AgentName, t)
	}

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
		// 清理 worktree
		if t.cleanup != nil {
			// 检查是否有变更, 记录到日志
			if hasChanges, err := r.worktreeMgr.HasWorktreeChanges(t.State.ID); err == nil && hasChanges {
				r.store.AppendOutput(t.State.UserID, t.State.ID,
					fmt.Sprintf("\n[worktree %s 有未提交变更, 路径: %s]\n", t.State.ID, t.State.WorktreePath))
			}
			t.cleanup()
		}
		// 注销消息路由
		if t.State.AgentName != "" {
			r.messageRouter.Unregister(t.State.AgentName)
		}
		// 停止超时计时器
		if t.timeoutTimer != nil {
			t.timeoutTimer.Stop()
		}
	}()

	// 启动 inbox 消费 goroutine (teammate 模式)
	if cfg.Type == TypeTeammate {
		go r.consumeInbox(t)
	}

	// 启动 agent
	ch := t.Agent.Run(cfg.Prompt, "task", cfg.History)

	var finalContent string

	for item := range ch {
		// 广播给所有订阅者
		r.broadcast(t, item)

		// 持久化输出
		if item.Content != "" {
			r.store.AppendOutput(t.State.UserID, t.State.ID, item.Content+"\n")
		}

		// 收集最终内容
		if item.Done && item.Content != "" {
			finalContent = item.Content
		}

		// 更新 turn 计数
		if item.Turn > t.State.TurnCount {
			t.State.TurnCount = item.Turn
		}

		// 计划模式: 遇到 plan_submit 状态暂停
		if item.Source == "plan_submit" {
			t.State.Status = StatusPaused
			t.State.PendingPlan = item.Content
			r.store.Save(t.State)

			// 广播暂停状态
			r.broadcast(t, agent.DisplayItem{
				Turn:    item.Turn,
				Content: "⏸️ 计划已提交，等待用户审批...",
				Source:  "plan_paused",
			})

			// 等待审批
			approved := <-t.planApproval
			t.State.Status = StatusRunning
			t.State.PendingPlan = ""
			r.store.Save(t.State)

			if !approved {
				t.State.Status = StatusKilled
				t.State.Error = "plan rejected"
				now := time.Now()
				t.State.EndTime = &now
				r.store.Save(t.State)
				r.broadcast(t, agent.DisplayItem{Done: true, Source: "task_end"})
				return
			}

			// 审批通过, 通知 agent 继续
			t.Agent.ApprovePlan()
			r.broadcast(t, agent.DisplayItem{
				Turn:    item.Turn,
				Content: "✅ 计划已批准，继续执行...",
				Source:  "plan_approved",
			})
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

	// 保存消息历史 (供子 agent 恢复重放)
	if finalMsgs := t.Agent.GetFinalMessages(); len(finalMsgs) > 0 {
		r.store.SaveMessages(t.State.UserID, t.State.ID, finalMsgs)
	}

	// 持久化最终状态
	r.store.Save(t.State)

	// 广播结束信号
	r.broadcast(t, agent.DisplayItem{Done: true, Source: "task_end"})
}

// consumeInbox 消费 teammate 收到的消息
func (r *Runtime) consumeInbox(t *Task) {
	for {
		select {
		case <-t.done:
			return
		case msg, ok := <-t.inbox:
			if !ok {
				return
			}
			// 将消息注入 agent 上下文
			t.Agent.InjectMessage(fmt.Sprintf("[消息来自 %s]: %s", msg.From, msg.Content))
		}
	}
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

// SaveState 持久化任务状态 (公开方法, 供外部调用)
func (r *Runtime) SaveState(state *TaskState) error {
	return r.store.Save(state)
}

// SendMessage 跨 agent 发送消息
// 参考 cc-haha SendMessage 工具
func (r *Runtime) SendMessage(from, to, content string) error {
	return r.messageRouter.Send(MessageEnvelope{
		From:    from,
		To:      to,
		Content: content,
		SentAt:  time.Now(),
	})
}

// Wait 等待任务完成
func (t *Task) Wait() <-chan struct{} {
	return t.done
}

// Restore 恢复未完成的任务(服务重启时调用)
// 将所有 running 状态的任务标记为 failed
// 子 agent 级恢复: 保留 transcript 供主 agent 重放
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

				// 清理残留 worktree
				if state.WorktreePath != "" {
					repoRoot := FindRepoRoot(state.WorktreePath)
					if repoRoot != "" {
						r.worktreeMgr.RemoveWorktree(repoRoot, state.ID)
					} else {
						// 兜底: 直接删除目录
						os.RemoveAll(state.WorktreePath)
					}
				}
			}
		}
	}
	return nil
}

// GetMessages 获取任务的消息历史 (用于子 agent 恢复)
func (r *Runtime) GetMessages(userID int64, taskID string) ([]llm.Message, error) {
	return r.store.LoadMessages(userID, taskID)
}

// ResumeTask 恢复已中断的任务 (子 agent transcript 重放)
// 参考 cc-haha resumeAgent.ts: 加载 transcript -> 重放 -> 继续执行
//
// 与 Restore() 不同:
//   - Restore: 服务重启时将 running 标记为 failed (清理)
//   - ResumeTask: 用户主动恢复已 failed 的任务 (重放)
//
// 返回新的 Task 实例
func (r *Runtime) ResumeTask(taskID string, userID int64) (*Task, error) {
	// 加载原任务状态
	origState, err := r.store.Load(userID, taskID)
	if err != nil {
		return nil, fmt.Errorf("load task state: %w", err)
	}

	// 只允许恢复 failed/killed 状态的任务
	if !IsTerminal(origState.Status) {
		return nil, fmt.Errorf("task %s is not in terminal state (current: %s)", taskID, origState.Status)
	}

	// 加载消息历史 (transcript)
	history, err := r.store.LoadMessages(userID, taskID)
	if err != nil {
		history = nil // 无历史, 从头开始
	}

	// 构造恢复配置
	cfg := TaskConfig{
		Type:      origState.Type,
		ParentID:  origState.ParentID,
		UserID:    userID,
		SessionID: origState.SessionID,
		Prompt:    fmt.Sprintf("[恢复任务 %s] %s", taskID, origState.Prompt),
		Goal:      origState.Goal,
		PlanMode:  false, // 恢复时不进计划模式
		History:   history,
	}

	// 启动新任务 (会生成新 taskID)
	t, err := r.Start(cfg)
	if err != nil {
		return nil, err
	}

	// 在新任务状态中记录恢复来源
	t.State.Description = fmt.Sprintf("[恢复自 %s] %s", taskID, origState.Description)
	r.store.Save(t.State)

	return t, nil
}

// GetSubtaskResult 获取子任务结果摘要 (供主 agent 重放用)
// 参考 cc-haha reconstructForSubagentResume
func (r *Runtime) GetSubtaskResult(userID int64, taskID string) (string, error) {
	state, err := r.store.Load(userID, taskID)
	if err != nil {
		return "", err
	}

	result := fmt.Sprintf("子任务 %s (%s):\n", taskID, state.Description)
	result += fmt.Sprintf("  状态: %s\n", state.Status)
	if state.Error != "" {
		result += fmt.Sprintf("  错误: %s\n", state.Error)
	}
	result += fmt.Sprintf("  轮次: %d\n", state.TurnCount)
	result += fmt.Sprintf("  Token: %d\n", state.TokenUsage.TotalTokens)

	// 读取输出日志最后 500 字符
	outputPath := r.store.outputPath(userID, taskID)
	if data, err := os.ReadFile(outputPath); err == nil && len(data) > 0 {
		output := string(data)
		if len(output) > 500 {
			output = "..." + output[len(output)-500:]
		}
		result += fmt.Sprintf("  输出摘要:\n%s\n", output)
	}

	return result, nil
}

// generateTaskID 生成任务ID
func generateTaskID(t Type) string {
	prefix := "x"
	switch t {
	case TypeMain:
		prefix = "m"
	case TypeSubagent:
		prefix = "s"
	case TypeTeammate:
		prefix = "t"
	case TypeRemote:
		prefix = "r"
	case TypeMonitor:
		prefix = "n"
	}
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// _ filepath 引用, 避免删除 import 后报错
var _ = filepath.Join
