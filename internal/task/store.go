package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/genericagent/ga/internal/llm"
)

// Store 任务持久化层
//
// 磁盘布局:
//
//	<baseDir>/users/uN/tasks/
//	  ├── <taskID>/
//	  │   ├── state.json          # TaskState (原子写入)
//	  │   ├── messages.json       # 完整 llm.Message 历史
//	  │   ├── output.log          # 增量输出日志
//	  │   └── plan.md             # 待审批计划
//	  └── index.json              # 任务索引(可选)
type Store struct {
	baseDir string
	mu      sync.Mutex
}

// NewStore 创建持久化层
func NewStore(dataDir string) *Store {
	return &Store{baseDir: filepath.Join(dataDir, "users")}
}

func (s *Store) userTasksDir(userID int64) string {
	dir := filepath.Join(s.baseDir, fmt.Sprintf("u%d", userID), "tasks")
	os.MkdirAll(dir, 0755)
	return dir
}

func (s *Store) taskDir(userID int64, taskID string) string {
	dir := filepath.Join(s.userTasksDir(userID), taskID)
	os.MkdirAll(dir, 0755)
	return dir
}

func (s *Store) statePath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "state.json")
}

func (s *Store) messagesPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "messages.json")
}

func (s *Store) outputPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "output.log")
}

// Save 原子写入 state.json
func (s *Store) Save(state *TaskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := s.statePath(state.UserID, state.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveMessages 保存完整消息历史
func (s *Store) SaveMessages(userID int64, taskID string, msgs []llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	return os.WriteFile(s.messagesPath(userID, taskID), data, 0644)
}

// LoadMessages 加载消息历史
func (s *Store) LoadMessages(userID int64, taskID string) ([]llm.Message, error) {
	data, err := os.ReadFile(s.messagesPath(userID, taskID))
	if err != nil {
		return nil, err
	}
	var msgs []llm.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// Load 加载任务状态
func (s *Store) Load(userID int64, taskID string) (*TaskState, error) {
	data, err := os.ReadFile(s.statePath(userID, taskID))
	if err != nil {
		return nil, err
	}
	var state TaskState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ListByUser 列出用户所有任务状态
func (s *Store) ListByUser(userID int64) ([]*TaskState, error) {
	tasksDir := s.userTasksDir(userID)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, nil
	}
	var states []*TaskState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := s.Load(userID, entry.Name())
		if err != nil {
			continue
		}
		states = append(states, state)
	}
	return states, nil
}

// AppendOutput 追加输出日志
func (s *Store) AppendOutput(userID int64, taskID, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.outputPath(userID, taskID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// ListAllUsers 列出所有有任务的用户ID(用于服务重启恢复)
func (s *Store) ListAllUsers() ([]int64, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, nil
	}
	var userIDs []int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var uid int64
		if _, err := fmt.Sscanf(entry.Name(), "u%d", &uid); err == nil {
			userIDs = append(userIDs, uid)
		}
	}
	return userIDs, nil
}
