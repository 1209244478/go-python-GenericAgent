package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hasPathPrefix 检查 path 是否以 prefix 为前缀（路径分隔符感知，大小写不敏感）
// 在 Windows 等不区分大小写的文件系统上，strings.HasPrefix 可能被绕过
func hasPathPrefix(path, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if strings.EqualFold(path, prefix) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix)+string(filepath.Separator))
}

type Manager struct {
	dataDir  string
	skillDir string
}

func NewManager(dataDir string, skillDir string) *Manager {
	// 确保路径是绝对路径，避免相对路径导致 CWD 和 chdir 问题
	if absData, err := filepath.Abs(dataDir); err == nil {
		dataDir = absData
	}
	if absSkill, err := filepath.Abs(skillDir); err == nil {
		skillDir = absSkill
	}
	return &Manager{
		dataDir:  dataDir,
		skillDir: skillDir,
	}
}

// UserBaseDir 返回用户基础目录（用于存储聊天历史等系统文件）
func (m *Manager) UserBaseDir(userID int64) string {
	dir := filepath.Join(m.dataDir, "users", fmt.Sprintf("u%d", userID))
	os.MkdirAll(dir, 0755)
	return dir
}

// UserDir 返回用户文件目录（用于存储用户文件）
func (m *Manager) UserDir(userID int64) string {
	dir := filepath.Join(m.dataDir, "users", fmt.Sprintf("u%d", userID), "files")
	os.MkdirAll(dir, 0755)
	return dir
}

func (m *Manager) ResolvePath(userID int64, path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths not allowed")
	}

	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	userDir := m.UserDir(userID)
	fullPath := filepath.Join(userDir, cleaned)
	if !hasPathPrefix(fullPath, userDir) {
		return "", fmt.Errorf("access denied: path outside workspace")
	}

	return fullPath, nil
}

func (m *Manager) ResolveSkillPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths not allowed")
	}

	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	fullPath := filepath.Join(m.skillDir, cleaned)
	if !hasPathPrefix(fullPath, m.skillDir) {
		return "", fmt.Errorf("access denied: path outside skill directory")
	}

	return fullPath, nil
}

func (m *Manager) IsSkillPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return hasPathPrefix(abs, m.skillDir)
}

func (m *Manager) CanWrite(userID int64, path string) bool {
	userDir := m.UserDir(userID)
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return hasPathPrefix(abs, userDir)
}

func (m *Manager) CanRead(userID int64, path string) bool {
	if m.CanWrite(userID, path) {
		return true
	}
	return m.IsSkillPath(path)
}

func (m *Manager) ListUserFiles(userID int64) ([]FileInfo, error) {
	userDir := m.UserDir(userID)
	return listFilesRecursive(userDir, userDir, 0)
}

const maxListDepth = 5 // 限制递归深度，防止目录爆炸

type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

func listFilesRecursive(root string, prefix string, depth int) ([]FileInfo, error) {
	var files []FileInfo
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath, _ := filepath.Rel(prefix, filepath.Join(root, e.Name()))
		fi := FileInfo{
			Name:    e.Name(),
			Path:    relPath,
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		}
		files = append(files, fi)

		// 递归列出子目录 (限制深度)
		if e.IsDir() && depth < maxListDepth {
			subFiles, err := listFilesRecursive(filepath.Join(root, e.Name()), prefix, depth+1)
			if err == nil {
				files = append(files, subFiles...)
			}
		}
	}
	return files, nil
}
