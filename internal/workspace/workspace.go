package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manager struct {
	dataDir  string
	skillDir string
}

func NewManager(dataDir string, skillDir string) *Manager {
	return &Manager{
		dataDir:  dataDir,
		skillDir: skillDir,
	}
}

func (m *Manager) UserDir(userID int64) string {
	dir := filepath.Join(m.dataDir, "users", fmt.Sprintf("u%d", userID))
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
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(userDir)) {
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
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(m.skillDir)) {
		return "", fmt.Errorf("access denied: path outside skill directory")
	}

	return fullPath, nil
}

func (m *Manager) IsSkillPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(filepath.Clean(abs), filepath.Clean(m.skillDir))
}

func (m *Manager) CanWrite(userID int64, path string) bool {
	userDir := m.UserDir(userID)
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(filepath.Clean(abs), filepath.Clean(userDir))
}

func (m *Manager) CanRead(userID int64, path string) bool {
	if m.CanWrite(userID, path) {
		return true
	}
	return m.IsSkillPath(path)
}

func (m *Manager) ListUserFiles(userID int64) ([]FileInfo, error) {
	userDir := m.UserDir(userID)
	return listFiles(userDir, userDir)
}

type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

func listFiles(root string, prefix string) ([]FileInfo, error) {
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
	}
	return files, nil
}
