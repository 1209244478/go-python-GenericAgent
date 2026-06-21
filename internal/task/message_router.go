package task

import (
	"fmt"
	"sync"
)

// MessageRouter 跨 agent 消息路由
// 参考 cc-haha SendMessage 工具: 按名称寻址 teammate
//
// 路由规则:
//   - to="all": 广播给同团队所有 agent
//   - to="<name>": 定向发送给指定 agent
type MessageRouter struct {
	mu       sync.RWMutex
	agents   map[string]*Task // name -> task
	teams    map[string]map[string]*Task // teamName -> name -> task
}

// NewMessageRouter 创建消息路由器
func NewMessageRouter() *MessageRouter {
	return &MessageRouter{
		agents: make(map[string]*Task),
		teams:  make(map[string]map[string]*Task),
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
