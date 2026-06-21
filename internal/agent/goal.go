package agent

import (
	"fmt"
	"sync"
	"time"
)

// GoalState 目标状态
// 参考 cc-haha goals/goalState.ts: ThreadGoal + 暂停/恢复/完成状态机
type GoalState string

const (
	GoalStateActive   GoalState = "active"   // 活跃: 每N轮注入提醒
	GoalStatePaused   GoalState = "paused"   // 暂停: 不注入提醒
	GoalStateDone     GoalState = "done"     // 完成: 注入完成确认
	GoalStateFailed   GoalState = "failed"   // 失败: 注入失败原因
)

// GoalTracker 目标追踪器
// 参考 cc-haha goalState.ts:
//   - 状态机: active -> paused/done/failed
//   - hook 超时: GOAL_HOOK_TIMEOUT_SECONDS = 45
//   - 可暂停/恢复/完成
type GoalTracker struct {
	mu          sync.RWMutex
	objective   string        // 目标描述
	state       GoalState     // 当前状态
	createdAt   time.Time     // 创建时间
	updatedAt   time.Time     // 最后更新时间
	remindEvery int           // 每N轮注入提醒
	hookTimeout time.Duration // hook 超时 (默认 45s, 参考 cc-haha)
	lastRemind  int           // 上次注入提醒的轮次
	completion  string        // 完成/失败原因
}

// NewGoalTracker 创建目标追踪器
func NewGoalTracker(objective string) *GoalTracker {
	now := time.Now()
	return &GoalTracker{
		objective:   objective,
		state:       GoalStateActive,
		createdAt:   now,
		updatedAt:   now,
		remindEvery: 5,                       // 每5轮提醒
		hookTimeout: 45 * time.Second,        // 参考 cc-haha GOAL_HOOK_TIMEOUT_SECONDS
	}
}

// State 获取当前状态
func (g *GoalTracker) State() GoalState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.state
}

// Objective 获取目标描述
func (g *GoalTracker) Objective() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.objective
}

// Pause 暂停目标提醒
func (g *GoalTracker) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == GoalStateActive {
		g.state = GoalStatePaused
		g.updatedAt = time.Now()
	}
}

// Resume 恢复目标提醒
func (g *GoalTracker) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == GoalStatePaused {
		g.state = GoalStateActive
		g.updatedAt = time.Now()
	}
}

// Complete 标记目标完成
func (g *GoalTracker) Complete(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = GoalStateDone
	g.completion = reason
	g.updatedAt = time.Now()
}

// Fail 标记目标失败
func (g *GoalTracker) Fail(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = GoalStateFailed
	g.completion = reason
	g.updatedAt = time.Now()
}

// ShouldRemind 判断当前轮次是否需要注入目标提醒
// turn: 当前轮次
// 返回: 是否需要提醒, 提醒消息
func (g *GoalTracker) ShouldRemind(turn int) (bool, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.state != GoalStateActive {
		return false, ""
	}

	if turn <= 1 || turn%g.remindEvery != 0 {
		return false, ""
	}

	if turn == g.lastRemind {
		return false, "" // 本轮已提醒
	}

	msg := fmt.Sprintf(
		"<goal_reminder status=\"%s\">\n"+
			"当前目标: %s\n"+
			"已进行 %d 轮。请确保所有操作都服务于该目标。\n"+
			"如目标已完成, 使用 set_goal 工具更新状态。\n"+
			"</goal_reminder>",
		g.state, g.objective, turn,
	)
	return true, msg
}

// MarkReminded 标记本轮已提醒
func (g *GoalTracker) MarkReminded(turn int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastRemind = turn
}

// StatusReport 生成目标状态报告
func (g *GoalTracker) StatusReport() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	switch g.state {
	case GoalStateActive:
		return fmt.Sprintf("目标[活跃]: %s (已进行 %v)", g.objective, time.Since(g.createdAt).Round(time.Second))
	case GoalStatePaused:
		return fmt.Sprintf("目标[暂停]: %s", g.objective)
	case GoalStateDone:
		return fmt.Sprintf("目标[完成]: %s (%s)", g.objective, g.completion)
	case GoalStateFailed:
		return fmt.Sprintf("目标[失败]: %s (%s)", g.objective, g.completion)
	default:
		return fmt.Sprintf("目标[未知]: %s", g.objective)
	}
}

// HookTimeout 返回 hook 超时时长
func (g *GoalTracker) HookTimeout() time.Duration {
	return g.hookTimeout
}
