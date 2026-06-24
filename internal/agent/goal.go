package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/genericagent/ga/internal/llm"
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

// EvaluateCompletionWithLLM 使用 LLM 评估目标完成度
// 参考 cc-haha createGoalPromptHook: Stop hook 用 LLM 判断目标是否完成
// 返回: (已完成, 评估理由, 错误)
//
// 评估流程:
//  1. 构造评估 prompt (目标 + 最近对话摘要)
//  2. 调用 LLM 判断是否完成
//  3. 超时则返回 false (保守判断)
func (g *GoalTracker) EvaluateCompletionWithLLM(client *llm.Client, recentMessages []llm.Message) (bool, string, error) {
	g.mu.RLock()
	objective := g.objective
	g.mu.RUnlock()

	if objective == "" {
		return false, "无目标", nil
	}

	// 构造最近对话摘要
	var recentText strings.Builder
	recentText.WriteString("最近对话:\n")
	for _, m := range recentMessages {
		if m.Role == "system" {
			continue
		}
		content, ok := m.Content.(string)
		if !ok || content == "" {
			continue
		}
		// 截断过长内容
		if len([]rune(content)) > 200 {
			content = string([]rune(content)[:200]) + "..."
		}
		recentText.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, content))
	}

	prompt := fmt.Sprintf(`你是目标评估助手。请判断以下目标是否已完成。

目标: %s

%s

请回答:
1. 目标是否已完成? (是/否)
2. 完成理由 (一句话)

格式:
完成: 是/否
理由: <一句话>`, objective, recentText.String())

	// 带超时调用 LLM
	resultCh := make(chan struct {
		text string
		err  error
	}, 1)

	go func() {
		ch, err := client.Chat(llm.ChatParams{
			Messages: []llm.Message{
				{Role: "user", Content: prompt},
			},
			MaxTokens:   100,
			Temperature: 0,
		})
		if err != nil {
			resultCh <- struct {
				text string
				err  error
			}{"", err}
			return
		}

		var result string
		for chunk := range ch {
			if chunk.Error != nil {
				resultCh <- struct {
					text string
					err  error
				}{"", chunk.Error}
				return
			}
			if chunk.Text != "" {
				result += chunk.Text
			}
		}
		resultCh <- struct {
			text string
			err  error
		}{result, nil}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			return false, "", res.err
		}
		// 解析结果 — 容忍格式变体 (中英文冒号、空格、换行差异)
		lowerText := strings.ToLower(res.text)
		completed := false

		// 优先检查 "完成" 行的值
		for _, line := range strings.Split(res.text, "\n") {
			lower := strings.ToLower(strings.TrimSpace(line))
			if !strings.HasPrefix(lower, "完成") {
				continue
			}
			// 去掉 "完成" 前缀后，兼容中英文冒号
			rest := strings.TrimSpace(line[len("完成"):])
			rest = strings.TrimPrefix(rest, ":")
			rest = strings.TrimPrefix(rest, "：")
			rest = strings.ToLower(strings.TrimSpace(rest))
			if strings.HasPrefix(rest, "是") || rest == "yes" || rest == "true" {
				completed = true
			}
			break
		}

		// 降级: 如果没有 "完成:" 行，检查全文是否包含明确的肯定词
		if !completed {
			if strings.Contains(lowerText, "目标已完成") ||
				strings.Contains(lowerText, "已完成") ||
				strings.Contains(lowerText, "goal completed") ||
				strings.Contains(lowerText, "completed: yes") {
				completed = true
			}
		}

		// 提取理由
		reason := ""
		for _, line := range strings.Split(res.text, "\n") {
			lower := strings.ToLower(strings.TrimSpace(line))
			if !strings.HasPrefix(lower, "理由") {
				continue
			}
			rest := strings.TrimSpace(line[len("理由"):])
			rest = strings.TrimPrefix(rest, ":")
			rest = strings.TrimPrefix(rest, "：")
			reason = strings.TrimSpace(rest)
			break
		}
		return completed, reason, nil
	case <-time.After(g.hookTimeout):
		// 超时, 保守判断为未完成
		return false, "评估超时", fmt.Errorf("goal evaluation timeout after %v", g.hookTimeout)
	}
}

// RestoreFromTranscript 从 transcript 恢复目标状态
// 参考 cc-haha ensureThreadGoalHookFromTranscript
// 分析历史消息, 提取目标信息
func RestoreFromTranscript(messages []llm.Message) *GoalTracker {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		content, ok := m.Content.(string)
		if !ok {
			continue
		}
		// 检测 set_goal 工具调用结果
		if strings.Contains(content, "目标已设置:") {
			// 提取目标内容
			idx := strings.Index(content, "目标已设置:")
			if idx >= 0 {
				goalText := strings.TrimSpace(content[idx+len("目标已设置:"):])
				// 截断到第一个换行
				if newlineIdx := strings.Index(goalText, "\n"); newlineIdx > 0 {
					goalText = goalText[:newlineIdx]
				}
				if goalText != "" {
					return NewGoalTracker(goalText)
				}
			}
		}
	}
	return nil
}
