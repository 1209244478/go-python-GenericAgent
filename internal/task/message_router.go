package task

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// 消息 UI 上限 (参考 cc-haha TEAMMATE_MESSAGES_UI_CAP=50)
const TeammateMessagesUICap = 50

// shutdown 协议消息前缀 (参考 cc-haha shutdown_request/shutdown_response)
const (
	ShutdownRequestPrefix  = "[shutdown_request]"
	ShutdownResponsePrefix = "[shutdown_response]"
)

// MessageRouter 跨 agent 消息路由
// 参考 cc-haha SendMessage 工具: 按名称寻址 teammate
//
// 路由规则:
//   - to="all": 广播给同团队所有 agent
//   - to="<name>": 定向发送给指定 agent
//
// 增强:
//   - shutdown 协议: 优雅关闭 teammate
//   - 消息 UI cap: 防止 inbox 内存爆炸
//   - 消息历史: 保留最近 N 条供 UI 展示
type MessageRouter struct {
	mu       sync.RWMutex
	agents   map[string]*Task // name -> task
	teams    map[string]map[string]*Task // teamName -> name -> task

	// 消息历史 (供 UI 展示, 限制数量防内存爆炸)
	historyMu sync.RWMutex
	history   []MessageEnvelope
}

// NewMessageRouter 创建消息路由器
func NewMessageRouter() *MessageRouter {
	return &MessageRouter{
		agents:  make(map[string]*Task),
		teams:   make(map[string]map[string]*Task),
		history: make([]MessageEnvelope, 0, TeammateMessagesUICap),
	}
}

// Register 注册 agent 到路由表
func (m *MessageRouter) Register(name string, t *Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[name] = t

	team := t.State.TeamName
	if team != "" {
		if m.teams[team] == nil {
			m.teams[team] = make(map[string]*Task)
		}
		m.teams[team][name] = t
	}
}

// Unregister 注销 agent
func (m *MessageRouter) Unregister(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t, ok := m.agents[name]; ok {
		team := t.State.TeamName
		if team != "" && m.teams[team] != nil {
			delete(m.teams[team], name)
			if len(m.teams[team]) == 0 {
				delete(m.teams, team)
			}
		}
		delete(m.agents, name)
	}
}

// Send 发送消息
func (m *MessageRouter) Send(msg MessageEnvelope) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 记录到历史 (限制数量)
	m.recordHistory(msg)

	// 检测 shutdown 协议消息
	if strings.HasPrefix(msg.Content, ShutdownRequestPrefix) {
		return m.handleShutdownRequest(msg)
	}
	if strings.HasPrefix(msg.Content, ShutdownResponsePrefix) {
		// shutdown 响应, 仅记录, 不路由
		return nil
	}

	// 广播给同团队所有成员
	if msg.To == "all" {
		// 查找发送者所在团队
		sender, ok := m.agents[msg.From]
		if !ok || sender.State.TeamName == "" {
			return fmt.Errorf("sender %s not in any team", msg.From)
		}
		team := sender.State.TeamName
		teamMembers, ok := m.teams[team]
		if !ok {
			return fmt.Errorf("team %s not found", team)
		}
		for name, t := range teamMembers {
			if name == msg.From {
				continue // 不发给自己
			}
			select {
			case t.inbox <- msg:
			default:
				// inbox 满, 跳过
			}
		}
		return nil
	}

	// 定向发送
	t, ok := m.agents[msg.To]
	if !ok {
		return fmt.Errorf("agent %s not found", msg.To)
	}
	select {
	case t.inbox <- msg:
		return nil
	default:
		return fmt.Errorf("agent %s inbox full", msg.To)
	}
}

// handleShutdownRequest 处理 shutdown 请求
// 参考 cc-haha shutdown_request/shutdown_response 协议
// 接收者应优雅停止当前任务并回复 shutdown_response
func (m *MessageRouter) handleShutdownRequest(msg MessageEnvelope) error {
	m.mu.RLock()
	target, ok := m.agents[msg.To]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent %s not found", msg.To)
	}

	// 注入 shutdown 消息到 inbox
	select {
	case target.inbox <- msg:
	default:
		return fmt.Errorf("agent %s inbox full", msg.To)
	}

	// 触发 agent 的 stop 信号 (优雅关闭)
	if target.Agent != nil {
		target.Agent.Stop()
	}

	return nil
}

// recordHistory 记录消息到历史 (限制数量)
func (m *MessageRouter) recordHistory(msg MessageEnvelope) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()

	m.history = append(m.history, msg)
	// 超过上限, 保留最新的 TeammateMessagesUICap 条
	if len(m.history) > TeammateMessagesUICap {
		m.history = m.history[len(m.history)-TeammateMessagesUICap:]
	}
}

// GetHistory 获取消息历史 (供 UI 展示)
func (m *MessageRouter) GetHistory() []MessageEnvelope {
	m.historyMu.RLock()
	defer m.historyMu.RUnlock()
	result := make([]MessageEnvelope, len(m.history))
	copy(result, m.history)
	return result
}

// SendShutdown 发送 shutdown 请求给指定 agent
// 主 agent 可用此方法优雅关闭 teammate
func (m *MessageRouter) SendShutdown(from, to string, reason string) error {
	content := fmt.Sprintf("%s %s", ShutdownRequestPrefix, reason)
	return m.Send(MessageEnvelope{
		From:    from,
		To:      to,
		Content: content,
		SentAt:  time.Now(),
	})
}

// BroadcastShutdown 向同团队所有成员广播 shutdown 请求
func (m *MessageRouter) BroadcastShutdown(from string, reason string) error {
	content := fmt.Sprintf("%s %s", ShutdownRequestPrefix, reason)
	return m.Send(MessageEnvelope{
		From:    from,
		To:      "all",
		Content: content,
		SentAt:  time.Now(),
	})
}

// ListAgents 列出所有已注册 agent
func (m *MessageRouter) ListAgents() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.agents))
	for name := range m.agents {
		names = append(names, name)
	}
	return names
}

// ListTeam 列出团队成员
func (m *MessageRouter) ListTeam(teamName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	members, ok := m.teams[teamName]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	return names
}
