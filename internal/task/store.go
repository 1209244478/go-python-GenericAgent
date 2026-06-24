package task

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/genericagent/ga/internal/llm"
)

// Store 任务持久化层
//
// 磁盘布局:
//
//	<baseDir>/users/uN/tasks/
//	  ├── <taskID>/
//	  │   ├── state.json          # TaskState (原子写入 + 版本备份)
//	  │   ├── state.json.bak      # 上一版本 state (回滚用)
//	  │   ├── messages.jsonl      # 消息历史 (JSONL 追加写, 支持增量)
//	  │   ├── messages.json       # 旧格式 (兼容读取, 迁移后删除)
//	  │   ├── messages.meta.json  # JSONL 元数据 (saved_count + sha256)
//	  │   ├── output.log          # 增量输出日志 (带轮转)
//	  │   ├── output.1.log        # 轮转日志
//	  │   ├── replacements.json   # 内容替换状态
//	  │   ├── wal.log             # WAL (写前日志, 崩溃恢复)
//	  │   └── plan.md             # 待审批计划
//	  └── index.json              # 用户任务索引 (快速列表)
//
// JSONL 追加写优势:
//   - 增量保存: 每轮只追加新消息, 避免全量序列化
//   - 崩溃友好: 即使中途崩溃, 已写入的消息不丢失
//   - 流式读取: 可按需读取部分消息
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

func (s *Store) messagesJSONLPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "messages.jsonl")
}

func (s *Store) messagesMetaPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "messages.meta.json")
}

func (s *Store) outputPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "output.log")
}

func (s *Store) walPath(userID int64, taskID string) string {
	return filepath.Join(s.taskDir(userID, taskID), "wal.log")
}

// messagesMeta JSONL 元数据 (跟踪已保存消息数 + 校验和)
type messagesMeta struct {
	SavedCount int    `json:"saved_count"` // 已追加到 JSONL 的消息数
	SHA256     string `json:"sha256"`      // JSONL 文件校验和 (防损坏)
	Compacted  bool   `json:"compacted"`   // 是否已合并 (全量重写)
}

// state 版本备份配置
const (
	stateMaxBackups = 3 // 保留最近 3 份 state 备份
)

// Save 原子写入 state.json (带多版本备份 + 索引更新)
func (s *Store) Save(state *TaskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := s.statePath(state.UserID, state.ID)

	// 多版本备份: state.json.bak -> state.json.bak.1 -> ... (删除最老的)
	// 先备份当前版本
	if _, err := os.Stat(path); err == nil {
		s.rotateStateBackup(state.UserID, state.ID)
		// 将当前 state.json 复制为 .bak (不是 rename, 因为后面要原子替换)
		if oldData, err := os.ReadFile(path); err == nil {
			os.WriteFile(path+".bak", oldData, 0644)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	// 同步更新索引 (不加锁, 已持有 s.mu)
	s.updateIndexLocked(state.UserID, state)
	return nil
}

// rotateStateBackup 轮转 state 备份文件
// state.json.bak -> state.json.bak.1 -> state.json.bak.2 -> ... (删除最老的)
func (s *Store) rotateStateBackup(userID int64, taskID string) {
	dir := s.taskDir(userID, taskID)
	base := "state.json.bak"
	// 删除最老的备份
	oldest := filepath.Join(dir, fmt.Sprintf("%s.%d", base, stateMaxBackups))
	os.Remove(oldest)
	// 依次重命名 bak.N -> bak.(N+1)
	for i := stateMaxBackups - 1; i >= 1; i-- {
		src := filepath.Join(dir, fmt.Sprintf("%s.%d", base, i))
		dst := filepath.Join(dir, fmt.Sprintf("%s.%d", base, i+1))
		os.Rename(src, dst)
	}
	// state.json.bak -> state.json.bak.1
	os.Rename(filepath.Join(dir, base), filepath.Join(dir, base+".1"))
}

// RollbackState 回滚到上一版本 state (用于错误恢复)
// 返回回滚后的 state, 如果没有备份则返回 nil
func (s *Store) RollbackState(userID int64, taskID string) (*TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bakPath := s.statePath(userID, taskID) + ".bak"
	data, err := os.ReadFile(bakPath)
	if err != nil {
		return nil, fmt.Errorf("no backup available: %w", err)
	}

	var state TaskState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	// 用备份覆盖当前 state.json
	path := s.statePath(userID, taskID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}

	// 更新索引
	s.updateIndexLocked(userID, &state)
	return &state, nil
}

// SaveMessages 全量保存消息历史 (JSONL 格式, 覆盖写)
// 用于任务结束时的最终快照, 或压缩后的全量重写
func (s *Store) SaveMessages(userID int64, taskID string, msgs []llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jsonlPath := s.messagesJSONLPath(userID, taskID)
	tmp := jsonlPath + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	for _, m := range msgs {
		if err := enc.Encode(&m); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, jsonlPath); err != nil {
		return err
	}

	// 删除旧格式 JSON (迁移完成)
	os.Remove(s.messagesPath(userID, taskID))

	// 更新元数据
	meta := messagesMeta{
		SavedCount: len(msgs),
		Compacted:  true,
	}
	s.saveMessagesMeta(userID, taskID, meta)
	return nil
}

// AppendMessages 增量追加新消息到 JSONL
// 只追加 msgs 中超出已保存数量的部分, 避免全量写
// 返回实际追加的消息数
func (s *Store) AppendMessages(userID int64, taskID string, msgs []llm.Message) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta := s.loadMessagesMeta(userID, taskID)
	start := meta.SavedCount
	if start > len(msgs) {
		start = len(msgs) // 已保存的比传入多 (可能是压缩后重置), 全量重写
	}
	if start == len(msgs) {
		return 0, nil // 无新消息
	}

	newMsgs := msgs[start:]
	jsonlPath := s.messagesJSONLPath(userID, taskID)

	// 如果是首次写入或已压缩, 直接追加
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	appended := 0
	for _, m := range newMsgs {
		if err := enc.Encode(&m); err != nil {
			bw.Flush()
			return appended, err
		}
		appended++
	}
	if err := bw.Flush(); err != nil {
		return appended, err
	}

	// 更新元数据
	meta.SavedCount = len(msgs)
	meta.Compacted = false
	s.saveMessagesMeta(userID, taskID, meta)
	return appended, nil
}

// LoadMessages 加载消息历史
// 优先读 JSONL, 回退到旧 JSON 格式 (兼容)
func (s *Store) LoadMessages(userID int64, taskID string) ([]llm.Message, error) {
	// 优先 JSONL
	jsonlPath := s.messagesJSONLPath(userID, taskID)
	if data, err := os.ReadFile(jsonlPath); err == nil {
		var msgs []llm.Message
		dec := json.NewDecoder(strings.NewReader(string(data)))
		for {
			var m llm.Message
			if err := dec.Decode(&m); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("decode jsonl: %w", err)
			}
			msgs = append(msgs, m)
		}
		return msgs, nil
	}

	// 回退旧 JSON
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

// loadMessagesMeta 加载 JSONL 元数据
func (s *Store) loadMessagesMeta(userID int64, taskID string) messagesMeta {
	var meta messagesMeta
	data, err := os.ReadFile(s.messagesMetaPath(userID, taskID))
	if err == nil {
		json.Unmarshal(data, &meta)
	}
	return meta
}

// saveMessagesMeta 保存 JSONL 元数据
func (s *Store) saveMessagesMeta(userID int64, taskID string, meta messagesMeta) {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		os.WriteFile(s.messagesMetaPath(userID, taskID), data, 0644)
	}
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

// AppendOutput 追加输出日志 (带轮转, 默认 10MB 单文件 + 保留 3 份)
func (s *Store) AppendOutput(userID int64, taskID, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.outputPath(userID, taskID)

	// 检查是否需要轮转
	if info, err := os.Stat(path); err == nil && info.Size() > outputLogMaxSize {
		s.rotateOutputLog(userID, taskID)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// outputLog 轮转配置
const (
	outputLogMaxSize    = 10 * 1024 * 1024 // 10MB
	outputLogMaxBackups = 3                // 保留 3 份历史
)

// rotateOutputLog 轮转输出日志
// output.log -> output.1.log -> output.2.log -> ... (删除最老的)
func (s *Store) rotateOutputLog(userID int64, taskID string) {
	dir := s.taskDir(userID, taskID)
	// 删除最老的备份
	oldest := filepath.Join(dir, fmt.Sprintf("output.%d.log", outputLogMaxBackups))
	os.Remove(oldest)
	// 依次重命名 output.N.log -> output.(N+1).log
	for i := outputLogMaxBackups - 1; i >= 1; i-- {
		src := filepath.Join(dir, fmt.Sprintf("output.%d.log", i))
		dst := filepath.Join(dir, fmt.Sprintf("output.%d.log", i+1))
		os.Rename(src, dst)
	}
	// output.log -> output.1.log
	os.Rename(filepath.Join(dir, "output.log"), filepath.Join(dir, "output.1.log"))
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

// ========== WAL (Write-Ahead Log) 崩溃恢复 ==========

// walEntry WAL 日志条目
type walEntry struct {
	Op    string        `json:"op"`     // 操作类型: "save_state" / "append_msgs" / "save_msgs"
	TS    int64         `json:"ts"`     // 时间戳 (纳秒)
	State *TaskState    `json:"state,omitempty"`
	Msgs  []llm.Message `json:"msgs,omitempty"`
}

// WALAppend 追加 WAL 条目 (在执行实际写操作前调用)
func (s *Store) WALAppend(userID int64, taskID string, entry walEntry) error {
	entry.TS = time.Now().UnixNano()
	data, err := json.Marshal(&entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.walPath(userID, taskID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// WALRecover 从 WAL 恢复未完成的操作
// 在服务启动时调用, 重放 WAL 中未 checkpoint 的操作
func (s *Store) WALRecover(userID int64, taskID string) error {
	path := s.walPath(userID, taskID)
	f, err := os.Open(path)
	if err != nil {
		return nil // 无 WAL, 无需恢复
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 最大 10MB 行
	var lastEntry *walEntry

	for scanner.Scan() {
		var entry walEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // 跳过损坏行
		}
		lastEntry = &entry
	}

	// 只重放最后一条 (假设之前的已 checkpoint)
	if lastEntry == nil {
		return nil
	}

	switch lastEntry.Op {
	case "save_state":
		if lastEntry.State != nil {
			// 直接写入 state (可能上次 Save 中途崩溃)
			data, _ := json.MarshalIndent(lastEntry.State, "", "  ")
			os.WriteFile(s.statePath(userID, taskID), data, 0644)
		}
	case "save_msgs":
		if lastEntry.Msgs != nil {
			s.SaveMessages(userID, taskID, lastEntry.Msgs)
		}
	}

	// 清理 WAL
	os.Remove(path)
	return nil
}

// WALCheckpoint 清理 WAL (操作成功完成后调用)
func (s *Store) WALCheckpoint(userID int64, taskID string) {
	os.Remove(s.walPath(userID, taskID))
}

// ========== Index 索引 (快速列表) ==========

// taskIndex 用户任务索引
type taskIndex struct {
	Tasks []indexEntry `json:"tasks"`
}

type indexEntry struct {
	ID          string    `json:"id"`
	Status      Status    `json:"status"`
	Type        Type      `json:"type"`
	Description string    `json:"description"`
	StartTime   time.Time `json:"start_time"`
	EndTime     *time.Time `json:"end_time,omitempty"`
}

func (s *Store) indexPath(userID int64) string {
	return filepath.Join(s.userTasksDir(userID), "index.json")
}

// UpdateIndex 更新任务索引 (公开接口, 加锁)
func (s *Store) UpdateIndex(state *TaskState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateIndexLocked(state.UserID, state)
}

// updateIndexLocked 更新任务索引 (不加锁, 调用方需持有 s.mu)
func (s *Store) updateIndexLocked(userID int64, state *TaskState) {
	idx := s.loadIndex(userID)

	// 更新或插入
	found := false
	for i, e := range idx.Tasks {
		if e.ID == state.ID {
			idx.Tasks[i] = indexEntry{
				ID:          state.ID,
				Status:      state.Status,
				Type:        state.Type,
				Description: state.Description,
				StartTime:   state.StartTime,
				EndTime:     state.EndTime,
			}
			found = true
			break
		}
	}
	if !found {
		idx.Tasks = append(idx.Tasks, indexEntry{
			ID:          state.ID,
			Status:      state.Status,
			Type:        state.Type,
			Description: state.Description,
			StartTime:   state.StartTime,
			EndTime:     state.EndTime,
		})
	}

	s.saveIndex(userID, idx)
}

// loadIndex 加载用户任务索引
func (s *Store) loadIndex(userID int64) taskIndex {
	var idx taskIndex
	data, err := os.ReadFile(s.indexPath(userID))
	if err == nil {
		json.Unmarshal(data, &idx)
	}
	return idx
}

// saveIndex 保存用户任务索引
func (s *Store) saveIndex(userID int64, idx taskIndex) {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err == nil {
		os.WriteFile(s.indexPath(userID), data, 0644)
	}
}

// ListByUserFast 快速列出用户任务 (从索引读取, O(1))
// 如果索引不存在, 回退到 ListByUser
func (s *Store) ListByUserFast(userID int64) ([]*TaskState, error) {
	idx := s.loadIndex(userID)
	if len(idx.Tasks) == 0 {
		return s.ListByUser(userID)
	}

	// 从索引快速构建 (只含列表字段, 完整状态需 Load)
	var states []*TaskState
	for _, e := range idx.Tasks {
		states = append(states, &TaskState{
			ID:          e.ID,
			Status:      e.Status,
			Type:        e.Type,
			Description: e.Description,
			StartTime:   e.StartTime,
			EndTime:     e.EndTime,
		})
	}
	return states, nil
}

// State 回滚见 RollbackState (在 Save 附近定义, 含多版本备份支持)
