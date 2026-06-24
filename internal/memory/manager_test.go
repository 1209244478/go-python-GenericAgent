package memory

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// m7: LogAccess 缩小锁范围 — 并发调用不应死锁
func TestLogAccess_ConcurrentNoDeadlock(t *testing.T) {
	m := NewManager(t.TempDir())

	// 先创建一个 memory 文件以便 LogAccess 能匹配 "memory" 关键字
	memFile := filepath.Join(m.RootDir, "memory", "test_note.md")
	os.WriteFile(memFile, []byte("# note"), 0644)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.LogAccess(memFile)
		}()
	}
	wg.Wait()

	// 验证统计文件已更新
	statsPath := filepath.Join(m.RootDir, "memory", "file_access_stats.json")
	data, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("stats file should exist: %v", err)
	}
	if len(data) == 0 {
		t.Error("stats file should not be empty")
	}
}

// m7: LogAccess 忽略非 memory 路径
func TestLogAccess_IgnoresNonMemoryPath(t *testing.T) {
	m := NewManager(t.TempDir())

	// 非 memory 路径应被忽略，不产生统计文件
	m.LogAccess("/tmp/random_file.txt")

	statsPath := filepath.Join(m.RootDir, "memory", "file_access_stats.json")
	if _, err := os.Stat(statsPath); !os.IsNotExist(err) {
		t.Error("stats file should not be created for non-memory path")
	}
}

// m7: LogAccess 累计计数
func TestLogAccess_IncrementsCount(t *testing.T) {
	m := NewManager(t.TempDir())
	memFile := filepath.Join(m.RootDir, "memory", "note.md")
	os.WriteFile(memFile, []byte("note"), 0644)

	// 调用 3 次
	m.LogAccess(memFile)
	m.LogAccess(memFile)
	m.LogAccess(memFile)

	statsPath := filepath.Join(m.RootDir, "memory", "file_access_stats.json")
	data, _ := os.ReadFile(statsPath)

	// 应包含 count: 3 (JSON 中为 float64)
	if !contains(string(data), `"count": 3`) {
		t.Errorf("expected count 3 in stats, got: %s", string(data))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
