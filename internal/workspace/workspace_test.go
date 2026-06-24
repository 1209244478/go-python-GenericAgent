package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// m9: hasPathPrefix 大小写不敏感 + 路径分隔符感知
func TestHasPathPrefix_Basic(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		prefix string
		want   bool
	}{
		{"exact_match", "/a/b", "/a/b", true},
		{"subdir", "/a/b/c", "/a/b", true},
		{"sibling_false", "/a/other", "/a/b", false},
		{"prefix_partial_false", "/a/bbb", "/a/b", false}, // 不能只匹配前缀字符
		{"case_insensitive_windows", `C:\Users\foo`, `c:\users`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPathPrefix(tc.path, tc.prefix); got != tc.want {
				t.Errorf("hasPathPrefix(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
			}
		})
	}
}

// m9: ResolvePath 拒绝绝对路径和路径穿越
func TestResolvePath_RejectsTraversal(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())

	cases := []struct {
		name string
		path string
	}{
		{"dotdot", "../../etc/passwd"},
		{"absolute_windows", `C:\Windows\System32`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.ResolvePath(1, tc.path)
			if err == nil {
				t.Errorf("expected error for path %q, got nil", tc.path)
			}
		})
	}
}

// Windows 上 /etc/passwd 不是绝对路径 (需要盘符)，但路径穿越仍应被拒绝
func TestResolvePath_RejectsUnixAbsOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/etc/passwd is not absolute on Windows")
	}
	m := NewManager(t.TempDir(), t.TempDir())
	_, err := m.ResolvePath(1, "/etc/passwd")
	if err == nil {
		t.Error("expected error for /etc/passwd")
	}
}

func TestResolvePath_ValidRelative(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	full, err := m.ResolvePath(1, "notes.txt")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.HasSuffix(full, "notes.txt") {
		t.Errorf("unexpected path: %s", full)
	}
}

// m9: 递归列表
func TestListUserFiles_Recursive(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, t.TempDir())

	userDir := m.UserDir(1)
	// 创建嵌套结构
	os.WriteFile(filepath.Join(userDir, "top.txt"), []byte("top"), 0644)
	os.MkdirAll(filepath.Join(userDir, "sub", "deep"), 0755)
	os.WriteFile(filepath.Join(userDir, "sub", "mid.txt"), []byte("mid"), 0644)
	os.WriteFile(filepath.Join(userDir, "sub", "deep", "leaf.txt"), []byte("leaf"), 0644)
	// 隐藏文件应被跳过
	os.WriteFile(filepath.Join(userDir, ".hidden"), []byte("hidden"), 0644)

	files, err := m.ListUserFiles(1)
	if err != nil {
		t.Fatalf("ListUserFiles failed: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range files {
		names[f.Path] = true
	}

	if !names["top.txt"] {
		t.Error("missing top.txt in recursive listing")
	}
	if !names[filepath.Join("sub", "mid.txt")] {
		t.Error("missing sub/mid.txt in recursive listing")
	}
	if !names[filepath.Join("sub", "deep", "leaf.txt")] {
		t.Error("missing sub/deep/leaf.txt in recursive listing")
	}
	if names[".hidden"] {
		t.Error("hidden file should be excluded")
	}
}

// m9: 大小写不敏感路径比较 (Windows 特定)
func TestCanWrite_CaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive test is Windows-specific")
	}
	root := t.TempDir()
	m := NewManager(root, t.TempDir())
	userDir := m.UserDir(1)

	// 用不同大小写访问同一路径应被允许
	mixed := filepath.Join(strings.ToUpper(userDir), "file.txt")
	if !m.CanWrite(1, mixed) {
		t.Errorf("CanWrite should be case-insensitive on Windows: %s", mixed)
	}
}
