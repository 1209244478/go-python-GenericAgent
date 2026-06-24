package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	RootDir string
	mu      sync.RWMutex
}

func NewManager(rootDir string) *Manager {
	memDir := filepath.Join(rootDir, "memory")
	os.MkdirAll(memDir, 0755)
	return &Manager{
		RootDir: rootDir,
	}
}

func (m *Manager) GetGlobalMemory() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	memPath := filepath.Join(m.RootDir, "memory", "global_mem.txt")
	data, err := os.ReadFile(memPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func (m *Manager) GetGlobalInsight() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path := filepath.Join(m.RootDir, "memory", "global_mem_insight.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (m *Manager) AppendGlobalMemory(content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	memPath := filepath.Join(m.RootDir, "memory", "global_mem.txt")
	f, err := os.OpenFile(memPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "\n[%s] %s", time.Now().Format("2006-01-02"), content)
	return err
}

func (m *Manager) ReadSOP(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path := filepath.Join(m.RootDir, "memory", name)
	if !strings.HasSuffix(name, ".md") {
		path += ".md"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("SOP not found: %s", name)
	}
	return string(data)
}

func (m *Manager) TrimHistory(history []string, contextWin int, keepRate float64) []string {
	if keepRate == 0 {
		keepRate = 0.6
	}

	totalLen := 0
	for _, h := range history {
		totalLen += len(h)
	}

	cap := contextWin * 3
	if totalLen <= cap {
		return history
	}

	target := int(float64(cap) * keepRate)
	for len(history) > 2 && totalLen > target {
		removed := len(history[0])
		history = history[1:]
		totalLen -= removed
	}

	return history
}

func (m *Manager) CompressHistoryTags(messages []map[string]any, keepRecent int, maxLen int) []map[string]any {
	if keepRecent == 0 {
		keepRecent = 10
	}
	if maxLen == 0 {
		maxLen = 800
	}

	thinkingRe := regexp.MustCompile(`(?s)<thinking>(.*?)</thinking>`)
	toolUseRe := regexp.MustCompile(`(?s)<tool_use>(.*?)</tool_use>`)
	toolResultRe := regexp.MustCompile(`(?s)<tool_result>(.*?)</tool_result>`)
	histRe := regexp.MustCompile(`(?s)<(?:history|key_info|earlier_context)>.*?</(?:history|key_info|earlier_context)>`)

	for i, msg := range messages {
		if i >= len(messages)-keepRecent {
			break
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}

		content = histRe.ReplaceAllString(content, "[...]")
		content = truncateTags(content, thinkingRe, maxLen)
		content = truncateTags(content, toolUseRe, maxLen)
		content = truncateTags(content, toolResultRe, maxLen)

		msg["content"] = content
	}

	return messages
}

func truncateTags(text string, re *regexp.Regexp, maxLen int) string {
	return re.ReplaceAllStringFunc(text, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		content := parts[1]
		if len(content) <= maxLen {
			return match
		}
		half := maxLen / 2
		return parts[0][:strings.Index(parts[0], ">")+1] +
			content[:half] + "\n...[Truncated]...\n" + content[len(content)-half:] +
			"</" + tagName(match) + ">"
	})
}

func tagName(tag string) string {
	re := regexp.MustCompile(`<(\w+)`)
	m := re.FindStringSubmatch(tag)
	if len(m) > 1 {
		return m[1]
	}
	return "unknown"
}

func (m *Manager) LogAccess(path string) {
	if !strings.Contains(path, "memory") {
		return
	}
	statsPath := filepath.Join(m.RootDir, "memory", "file_access_stats.json")

	// 读取现有统计 (持锁时间尽可能短)
	m.mu.Lock()
	stats := make(map[string]any)
	if data, err := os.ReadFile(statsPath); err == nil {
		json.Unmarshal(data, &stats)
	}

	fname := filepath.Base(path)
	if entry, ok := stats[fname].(map[string]any); ok {
		entry["count"] = intVal(entry["count"]) + 1
		entry["last"] = time.Now().Format("2006-01-02")
	} else {
		stats[fname] = map[string]any{
			"count": 1,
			"last":  time.Now().Format("2006-01-02"),
		}
	}

	data, _ := json.MarshalIndent(stats, "", "  ")
	m.mu.Unlock()

	// 写盘在锁外执行 (单写者语义由 mu 保证序列化，此处写入是幂等的)
	os.WriteFile(statsPath, data, 0644)
}

func intVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func ReadFileLines(path string, start, count int) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lines []string
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < start {
			continue
		}
		lines = append(lines, scanner.Text())
		if len(lines) >= count {
			break
		}
	}

	return strings.Join(lines, "\n")
}
