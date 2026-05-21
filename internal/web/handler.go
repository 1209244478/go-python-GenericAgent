package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/auth"
	"github.com/genericagent/ga/internal/config"
	"github.com/genericagent/ga/internal/llm"
	"github.com/genericagent/ga/internal/memory"
	"github.com/genericagent/ga/internal/tool"
	"github.com/genericagent/ga/internal/workspace"
	"github.com/gorilla/websocket"
)

type Handler struct {
	users     *auth.UserStore
	codes     *auth.CodeStore
	jwtMgr    *auth.JWTManager
	smtpCfg   auth.SMTPConfig
	wsMgr     *workspace.Manager
	rootDir   string
	skillDir  string
	upgrader  websocket.Upgrader
	sessions  map[int64]*Session
}

type Session struct {
	Agent     *agent.Agent
	Router    *tool.Router
	Cancel    func()
	CreatedAt time.Time
}

func NewHandler(
	users *auth.UserStore,
	codes *auth.CodeStore,
	jwtMgr *auth.JWTManager,
	smtpCfg auth.SMTPConfig,
	wsMgr *workspace.Manager,
	rootDir string,
	skillDir string,
) *Handler {
	return &Handler{
		users:    users,
		codes:    codes,
		jwtMgr:   jwtMgr,
		smtpCfg:  smtpCfg,
		wsMgr:    wsMgr,
		rootDir:  rootDir,
		skillDir: skillDir,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[int64]*Session),
	}
}

func (h *Handler) SendCode(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}

	code, err := h.codes.GenerateCode(req.Email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate code"})
		return
	}

	if err := auth.SendVerificationCode(h.smtpCfg, req.Email, code); err != nil {
		log.Printf("[DEV] Verification code for %s: %s (SMTP error: %v)", req.Email, code, err)
		if h.smtpCfg.Host != "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send email: " + err.Error()})
			return
		}
	} else {
		log.Printf("[AUTH] Verification code sent to %s", req.Email)
	}

	c.JSON(http.StatusOK, gin.H{"message": "verification code sent"})
}

func (h *Handler) Register(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
		Pwd   string `json:"password" binding:"required,min=6"`
		Name  string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}

	ok, err := h.codes.VerifyCode(req.Email, req.Code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "verification failed"})
		return
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired code"})
		return
	}

	existing, _ := h.users.GetByEmail(req.Email)
	if existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
		return
	}

	name := req.Name
	if name == "" {
		parts := strings.Split(req.Email, "@")
		name = parts[0]
	}

	user, err := h.users.Create(req.Email, req.Pwd, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}

	token, _ := h.jwtMgr.GenerateToken(user.ID, user.Email)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":    user.ID,
			"email": user.Email,
			"name":  user.Name,
		},
	})
}

func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Pwd   string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	user, err := h.users.GetByEmail(req.Email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if !h.users.VerifyPassword(user, req.Pwd) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token, _ := h.jwtMgr.GenerateToken(user.ID, user.Email)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":    user.ID,
			"email": user.Email,
			"name":  user.Name,
		},
	})
}

func (h *Handler) GetProfile(c *gin.Context) {
	userID := c.GetInt64("user_id")
	user, err := h.users.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":         user.ID,
		"email":      user.Email,
		"name":       user.Name,
		"created_at": user.CreatedAt,
	})
}

func (h *Handler) RunAgent(c *gin.Context) {
	userID := c.GetInt64("user_id")

	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config error"})
		return
	}

	if len(cfg.LLMs) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no LLM configured"})
		return
	}

	var client *llm.Client
	for _, lc := range cfg.LLMs {
		client = &llm.Client{
			APIBase:        lc.APIBase,
			APIKey:         lc.APIKey,
			Model:          lc.Model,
			APIMode:        lc.APIMode,
			Name:           lc.Name,
			Stream:         lc.Stream,
			MaxTokens:      lc.MaxTokens,
			Temperature:    lc.Temperature,
			ContextWin:     lc.ContextWin,
			ConnectTimeout: time.Duration(lc.ConnectTimeout) * time.Second,
			ReadTimeout:    time.Duration(lc.ReadTimeout) * time.Second,
			MaxRetries:     lc.MaxRetries,
		}
		break
	}

	userDir := h.wsMgr.UserDir(userID)
	memMgr := memory.NewManager(h.rootDir)
	sysPrompt := buildSystemPrompt(memMgr)
	toolsSchema := loadToolsSchema(h.rootDir)

	a := agent.New(client, sysPrompt, toolsSchema)
	a.Verbose = true
	a.MaxTurns = 80

	router := tool.NewRouter(userDir)
	router.SkillDir = h.skillDir
	router.AllowedDirs = []string{h.skillDir}
	a.Handler = router.Dispatch

	ch := a.Run(req.Prompt, "web")

	var finalContent string
	var toolSteps []map[string]any
	var exitResult string

	for item := range ch {
		if item.Done {
			exitResult = strings.TrimPrefix(item.Content, "\n[Done] ")
		} else if item.Source == "final" {
			finalContent += item.Content
		} else if item.Source == "tool" {
			toolSteps = append(toolSteps, map[string]any{
				"turn":    item.Turn,
				"content": item.Content,
			})
		}
	}

	if finalContent == "" {
		finalContent = "Task completed."
	}

	h.saveChatMessage(userID, "user", req.Prompt)
	h.saveChatMessage(userID, "agent", finalContent)

	c.JSON(http.StatusOK, gin.H{
		"response":   finalContent,
		"tool_steps": toolSteps,
		"exit":       exitResult,
		"done":       true,
	})
}

func (h *Handler) StreamAgent(c *gin.Context) {
	userID := c.GetInt64("user_id")

	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config error"})
		return
	}

	if len(cfg.LLMs) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no LLM configured"})
		return
	}

	var client *llm.Client
	for _, lc := range cfg.LLMs {
		client = &llm.Client{
			APIBase:        lc.APIBase,
			APIKey:         lc.APIKey,
			Model:          lc.Model,
			APIMode:        lc.APIMode,
			Name:           lc.Name,
			Stream:         lc.Stream,
			MaxTokens:      lc.MaxTokens,
			Temperature:    lc.Temperature,
			ContextWin:     lc.ContextWin,
			ConnectTimeout: time.Duration(lc.ConnectTimeout) * time.Second,
			ReadTimeout:    time.Duration(lc.ReadTimeout) * time.Second,
			MaxRetries:     lc.MaxRetries,
		}
		break
	}

	userDir := h.wsMgr.UserDir(userID)
	memMgr := memory.NewManager(h.rootDir)
	sysPrompt := buildSystemPrompt(memMgr)
	toolsSchema := loadToolsSchema(h.rootDir)

	a := agent.New(client, sysPrompt, toolsSchema)
	a.Verbose = true
	a.MaxTurns = 80

	router := tool.NewRouter(userDir)
	router.SkillDir = h.skillDir
	router.AllowedDirs = []string{h.skillDir}
	a.Handler = router.Dispatch

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch := a.Run(req.Prompt, "sse")

	var finalContent string

	for item := range ch {
		data, _ := json.Marshal(map[string]any{
			"content": item.Content,
			"turn":    item.Turn,
			"done":    item.Done,
			"source":  item.Source,
		})

		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
			a.Abort()
			break
		}
		c.Writer.(http.Flusher).Flush()

		if item.Done {
			// no-op
		} else if item.Source == "final" {
			finalContent += item.Content
		}
	}

	if finalContent == "" {
		finalContent = "Task completed."
	}

	h.saveChatMessage(userID, "user", req.Prompt)
	h.saveChatMessage(userID, "agent", finalContent)
}

func (h *Handler) WebSocketAgent(c *gin.Context) {
	userID := c.GetInt64("user_id")

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	json.Unmarshal(msg, &req)
	if req.Prompt == "" {
		conn.WriteJSON(gin.H{"error": "prompt required"})
		return
	}

	cfg, _ := config.Load()
	var client *llm.Client
	for _, lc := range cfg.LLMs {
		client = &llm.Client{
			APIBase:        lc.APIBase,
			APIKey:         lc.APIKey,
			Model:          lc.Model,
			APIMode:        lc.APIMode,
			Name:           lc.Name,
			Stream:         lc.Stream,
			MaxTokens:      lc.MaxTokens,
			Temperature:    lc.Temperature,
			ContextWin:     lc.ContextWin,
			ConnectTimeout: time.Duration(lc.ConnectTimeout) * time.Second,
			ReadTimeout:    time.Duration(lc.ReadTimeout) * time.Second,
			MaxRetries:     lc.MaxRetries,
		}
		break
	}

	userDir := h.wsMgr.UserDir(userID)
	memMgr := memory.NewManager(h.rootDir)
	sysPrompt := buildSystemPrompt(memMgr)
	toolsSchema := loadToolsSchema(h.rootDir)

	a := agent.New(client, sysPrompt, toolsSchema)
	a.Verbose = true
	a.MaxTurns = 80

	router := tool.NewRouter(userDir)
	router.SkillDir = h.skillDir
	router.AllowedDirs = []string{h.skillDir}
	a.Handler = router.Dispatch

	ch := a.Run(req.Prompt, "ws")

	for item := range ch {
		if err := conn.WriteJSON(gin.H{
			"content": item.Content,
			"turn":    item.Turn,
			"done":    item.Done,
			"source":  item.Source,
		}); err != nil {
			a.Abort()
			break
		}
	}
}

func (h *Handler) ListFiles(c *gin.Context) {
	userID := c.GetInt64("user_id")
	files, err := h.wsMgr.ListUserFiles(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

func (h *Handler) ReadFile(c *gin.Context) {
	userID := c.GetInt64("user_id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}

	var fullPath string
	var err error

	if strings.HasPrefix(path, "skills/") {
		skillRelPath := strings.TrimPrefix(path, "skills/")
		fullPath, err = h.wsMgr.ResolveSkillPath(skillRelPath)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	} else {
		fullPath, err = h.wsMgr.ResolvePath(userID, path)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	c.Data(http.StatusOK, "application/octet-stream", data)
}

func (h *Handler) UploadFile(c *gin.Context) {
	userID := c.GetInt64("user_id")
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	defer file.Close()

	relPath := c.PostForm("path")
	if relPath == "" {
		relPath = header.Filename
	}

	fullPath, err := h.wsMgr.ResolvePath(userID, relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	os.MkdirAll(filepath.Dir(fullPath), 0755)
	dst, err := os.Create(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	defer dst.Close()

	io.Copy(dst, file)
	c.JSON(http.StatusOK, gin.H{"message": "uploaded", "path": relPath})
}

func (h *Handler) DownloadFile(c *gin.Context) {
	userID := c.GetInt64("user_id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}

	fullPath, err := h.wsMgr.ResolvePath(userID, path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if _, err := os.Stat(fullPath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	c.FileAttachment(fullPath, filepath.Base(fullPath))
}

func (h *Handler) DeleteFile(c *gin.Context) {
	userID := c.GetInt64("user_id")
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}

	fullPath, err := h.wsMgr.ResolvePath(userID, path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if err := os.Remove(fullPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *Handler) ListSkills(c *gin.Context) {
	type SkillInfo struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		HasDir      bool                   `json:"has_dir"`
		Templates   []map[string]any       `json:"templates,omitempty"`
	}

	var skills []SkillInfo

	entries, err := os.ReadDir(h.skillDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "skills dir not found"})
		return
	}

	dirs := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(entry.Name(), "_") {
			dirs[entry.Name()] = true
		}
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}

		if entry.IsDir() {
			continue
		}

		if !strings.HasSuffix(name, ".py") {
			continue
		}

		skillName := strings.TrimSuffix(name, ".py")
		info := SkillInfo{
			Name:   skillName,
			HasDir: dirs[skillName],
		}

		if data, err := os.ReadFile(filepath.Join(h.skillDir, name)); err == nil {
			info.Description = extractPyDescription(string(data))
		}

		if dirs[skillName] {
			skillMd := filepath.Join(h.skillDir, skillName, "SKILL.md")
			if data, err := os.ReadFile(skillMd); err == nil {
				if desc := extractSkillDescription(string(data)); desc != "" {
					info.Description = desc
				}
			}
			indexFile := filepath.Join(h.skillDir, skillName, "index.json")
			if data, err := os.ReadFile(indexFile); err == nil {
				var idx map[string]any
				json.Unmarshal(data, &idx)
				if ts, ok := idx["templates"].([]any); ok {
					for _, t := range ts {
						if tm, ok := t.(map[string]any); ok {
							info.Templates = append(info.Templates, map[string]any{
								"slug":      tm["slug"],
								"name":      tm["name"],
								"tagline":   tm["tagline"],
								"mood":      tm["mood"],
								"scheme":    tm["scheme"],
								"formality": tm["formality"],
							})
						}
					}
				}
			}
		}

		skills = append(skills, info)
	}

	c.JSON(http.StatusOK, gin.H{"skills": skills})
}

func extractSkillDescription(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" && i > 0 {
			for j := i + 1; j < len(lines); j++ {
				l := strings.TrimSpace(lines[j])
				if l != "" && l != "---" && !strings.HasPrefix(l, "#") {
					return l
				}
			}
		}
	}
	return ""
}

func extractPyDescription(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			desc := strings.TrimLeft(line, "# ")
			if desc != "" && !strings.HasPrefix(desc, "!") && !strings.HasPrefix(desc, "-") {
				return desc
			}
		} else if line != "" && !strings.HasPrefix(line, "import") {
			break
		}
	}
	return ""
}

func (h *Handler) ListTemplates(c *gin.Context) {
	indexFile := filepath.Join(h.skillDir, "frontend-slides", "index.json")
	data, err := os.ReadFile(indexFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "templates not found"})
		return
	}

	var index map[string]any
	json.Unmarshal(data, &index)

	templates, _ := index["templates"].([]any)
	var result []map[string]any
	for _, t := range templates {
		if tm, ok := t.(map[string]any); ok {
			result = append(result, map[string]any{
				"slug":  tm["slug"],
				"name":  tm["name"],
				"mood":  tm["mood"],
				"occasion": tm["occasion"],
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"templates": result})
}

func buildSystemPrompt(memMgr *memory.Manager) string {
	promptPath := filepath.Join(config.RootDir(), "assets", "sys_prompt.txt")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		data = []byte("You are GenericAgent, a helpful autonomous AI assistant.")
	}
	prompt := string(data)
	prompt += fmt.Sprintf("\nToday: %s %s\n", time.Now().Format("2006-01-02"), time.Now().Format("Mon"))
	globalMem := memMgr.GetGlobalMemory()
	if globalMem != "" {
		prompt += globalMem
	}
	return prompt
}

func loadToolsSchema(rootDir string) []llm.ToolSchema {
	schemaPath := filepath.Join(rootDir, "assets", "tools_schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return defaultToolsSchema()
	}

	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return defaultToolsSchema()
	}

	var schemas []llm.ToolSchema
	for _, item := range raw {
		fn, _ := item["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		if name == "" {
			continue
		}
		schemas = append(schemas, llm.ToolSchema{
			Name:        name,
			Description: desc,
			InputSchema: params,
		})
	}
	return schemas
}

func defaultToolsSchema() []llm.ToolSchema {
	return []llm.ToolSchema{
		{Name: "code_run", Description: "Execute code", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}}}},
		{Name: "file_read", Description: "Read file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		{Name: "file_write", Description: "Write file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}},
		{Name: "skill_run", Description: "Run skill", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"skill": map[string]any{"type": "string"}}}},
	}
}

func parseInt(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

type ChatMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

func (h *Handler) chatHistoryPath(userID int64) string {
	userDir := h.wsMgr.UserDir(userID)
	return filepath.Join(userDir, ".chat_history.json")
}

func (h *Handler) loadChatHistory(userID int64) []ChatMessage {
	path := h.chatHistoryPath(userID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var messages []ChatMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil
	}
	return messages
}

func (h *Handler) saveChatMessage(userID int64, role string, content string) {
	messages := h.loadChatHistory(userID)
	messages = append(messages, ChatMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	})
	const maxMessages = 200
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return
	}
	os.WriteFile(h.chatHistoryPath(userID), data, 0644)
}

func (h *Handler) GetChatHistory(c *gin.Context) {
	userID := c.GetInt64("user_id")
	messages := h.loadChatHistory(userID)
	if messages == nil {
		messages = []ChatMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

func (h *Handler) ClearChatHistory(c *gin.Context) {
	userID := c.GetInt64("user_id")
	path := h.chatHistoryPath(userID)
	os.Remove(path)
	c.JSON(http.StatusOK, gin.H{"message": "cleared"})
}
