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
	"github.com/genericagent/ga/internal/task"
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
	taskRT    *task.Runtime
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
	// 创建 Task Runtime (先创建 store 和 runtime shell, factory 内引用 runtime 指针)
	store := task.NewStore(filepath.Join(rootDir, "data"))
	var taskRT *task.Runtime
	taskRT = task.NewRuntime(store, func(cfg task.AgentConfig) *agent.Agent {
		// 加载 LLM 配置
		loaded, _ := config.Load()
		var client *llm.Client
		for _, lc := range loaded.LLMs {
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
		userDir := wsMgr.UserDir(cfg.UserID)
		memMgr := memory.NewManager(rootDir)
		sysPrompt := buildSystemPrompt(memMgr, userDir, skillDir)
		toolsSchema := loadToolsSchema(rootDir)
		a := agent.New(client, sysPrompt, toolsSchema)
		a.Verbose = true
		a.MaxTurns = 80
		a.Goal = cfg.Goal
		a.PlanMode = cfg.PlanMode
		a.TaskID = cfg.TaskID
		a.CwdOverride = cfg.CwdOverride
		a.PlanApprovalCh = make(chan bool, 1)
		// planApproved channel 在 loop 首次 plan_submit 时初始化
		router := tool.NewRouter(userDir)
		router.SkillDir = skillDir
		router.AllowedDirs = []string{skillDir}
		router.TaskRuntime = taskRT
		router.CurrentTaskID = cfg.TaskID
		if cfg.CwdOverride != "" {
			router.Cwd = cfg.CwdOverride
		}
		a.Handler = router.Dispatch
		return a
	})

	// 恢复未完成任务
	taskRT.Restore()

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
		taskRT:   taskRT,
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
		Prompt    string `json:"prompt" binding:"required"`
		SessionID int64  `json:"session_id"`
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
	sysPrompt := buildSystemPrompt(memMgr, userDir, h.skillDir)
	toolsSchema := loadToolsSchema(h.rootDir)

	a := agent.New(client, sysPrompt, toolsSchema)
	a.Verbose = true
	a.MaxTurns = 80

	router := tool.NewRouter(userDir)
	router.SkillDir = h.skillDir
	router.AllowedDirs = []string{h.skillDir}
	a.Handler = router.Dispatch

	ch := a.Run(req.Prompt, "web", chatHistoryToMessages(func() []ChatMessage {
		if req.SessionID > 0 {
			return h.loadChatHistorySession(userID, req.SessionID)
		}
		return h.loadChatHistory(userID)
	}()))

	var finalContent string
	var toolSteps []map[string]any
	var exitResult string
	var historyItems []ChatMessage

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

		if item.Source == "assistant" {
			historyItems = append(historyItems, ChatMessage{
				Role:      "assistant",
				Content:   item.Content,
				ToolCalls: item.ToolCalls,
			})
		} else if item.Source == "tool_result" {
			historyItems = append(historyItems, ChatMessage{
				Role:       "tool",
				Content:    item.Content,
				ToolCallID: item.ToolCallID,
			})
		}
	}

	if finalContent == "" {
		finalContent = "Task completed."
	}

	if req.SessionID > 0 {
		h.saveChatMessageSession(userID, req.SessionID, "user", req.Prompt, nil, "")
		for _, hi := range historyItems {
			h.saveChatMessageSession(userID, req.SessionID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		}
		h.saveChatMessageSession(userID, req.SessionID, "agent", finalContent, nil, "")
	} else {
		h.saveChatMessage(userID, "user", req.Prompt, nil, "")
		for _, hi := range historyItems {
			h.saveChatMessage(userID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		}
		h.saveChatMessage(userID, "agent", finalContent, nil, "")
	}

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
		Prompt    string `json:"prompt" binding:"required"`
		SessionID int64  `json:"session_id"`
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
	sysPrompt := buildSystemPrompt(memMgr, userDir, h.skillDir)
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

	// Load chat history for context
	var history []llm.Message
	if req.SessionID > 0 {
		history = chatHistoryToMessages(h.loadChatHistorySession(userID, req.SessionID))
	} else {
		history = chatHistoryToMessages(h.loadChatHistory(userID))
	}

	ch := a.Run(req.Prompt, "sse", history)

	var finalContent string
	var toolSteps []string
	// Collect history items for saving
	var historyItems []ChatMessage

	for item := range ch {
		data, _ := json.Marshal(map[string]any{
			"content": item.Content,
			"turn":    item.Turn,
			"done":    item.Done,
			"source":  item.Source,
			"outputs": item.Outputs,
		})

		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
			a.Abort()
			break
		}
		c.Writer.(http.Flusher).Flush()

		if item.Done {
			if item.Content != "" {
				finalContent = item.Content
			}
		} else if item.Source == "final" {
			finalContent += item.Content
		} else if item.Source == "tool" && item.Content != "" {
			toolSteps = append(toolSteps, item.Content)
		}

		// Collect structured history items
		if item.Source == "assistant" {
			historyItems = append(historyItems, ChatMessage{
				Role:      "assistant",
				Content:   item.Content,
				ToolCalls: item.ToolCalls,
			})
		} else if item.Source == "tool_result" {
			historyItems = append(historyItems, ChatMessage{
				Role:       "tool",
				Content:    item.Content,
				ToolCallID: item.ToolCallID,
			})
		}
	}

	if finalContent == "" {
		finalContent = "Task completed."
	}

	// Save user message
	if req.SessionID > 0 {
		h.saveChatMessageSession(userID, req.SessionID, "user", req.Prompt, nil, "")
	} else {
		h.saveChatMessage(userID, "user", req.Prompt, nil, "")
	}

	// Save structured history (assistant+tool_calls, tool+tool_call_id)
	for _, hi := range historyItems {
		if req.SessionID > 0 {
			h.saveChatMessageSession(userID, req.SessionID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		} else {
			h.saveChatMessage(userID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		}
	}

	// Save final agent response
	if req.SessionID > 0 {
		h.saveChatMessageSession(userID, req.SessionID, "agent", finalContent, nil, "")
	} else {
		h.saveChatMessage(userID, "agent", finalContent, nil, "")
	}
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
		Prompt    string `json:"prompt"`
		SessionID int64  `json:"session_id"`
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
	sysPrompt := buildSystemPrompt(memMgr, userDir, h.skillDir)
	toolsSchema := loadToolsSchema(h.rootDir)

	a := agent.New(client, sysPrompt, toolsSchema)
	a.Verbose = true
	a.MaxTurns = 80

	router := tool.NewRouter(userDir)
	router.SkillDir = h.skillDir
	router.AllowedDirs = []string{h.skillDir}
	a.Handler = router.Dispatch

	ch := a.Run(req.Prompt, "ws", chatHistoryToMessages(func() []ChatMessage {
		if req.SessionID > 0 {
			return h.loadChatHistorySession(userID, req.SessionID)
		}
		return h.loadChatHistory(userID)
	}()))

	var finalContent string
	var toolSteps []string
	var historyItems []ChatMessage

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

		if item.Done {
			if item.Content != "" {
				finalContent = item.Content
			}
		} else if item.Source == "final" {
			finalContent += item.Content
		} else if item.Source == "tool" && item.Content != "" {
			toolSteps = append(toolSteps, item.Content)
		}

		if item.Source == "assistant" {
			historyItems = append(historyItems, ChatMessage{
				Role:      "assistant",
				Content:   item.Content,
				ToolCalls: item.ToolCalls,
			})
		} else if item.Source == "tool_result" {
			historyItems = append(historyItems, ChatMessage{
				Role:       "tool",
				Content:    item.Content,
				ToolCallID: item.ToolCallID,
			})
		}
	}

	if finalContent == "" {
		finalContent = "Task completed."
	}

	if req.SessionID > 0 {
		h.saveChatMessageSession(userID, req.SessionID, "user", req.Prompt, nil, "")
	} else {
		h.saveChatMessage(userID, "user", req.Prompt, nil, "")
	}

	for _, hi := range historyItems {
		if req.SessionID > 0 {
			h.saveChatMessageSession(userID, req.SessionID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		} else {
			h.saveChatMessage(userID, hi.Role, hi.Content, hi.ToolCalls, hi.ToolCallID)
		}
	}

	if req.SessionID > 0 {
		h.saveChatMessageSession(userID, req.SessionID, "agent", finalContent, nil, "")
	} else {
		h.saveChatMessage(userID, "agent", finalContent, nil, "")
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

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 50*1024*1024)

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

func (h *Handler) SaveFile(c *gin.Context) {
	userID := c.GetInt64("user_id")

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path and content required"})
		return
	}
	if req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}

	// Do not allow saving to skill templates
	if strings.HasPrefix(req.Path, "skills/") {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot save to skill templates"})
		return
	}

	fullPath, err := h.wsMgr.ResolvePath(userID, req.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "saved", "path": req.Path})
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

func (h *Handler) PreviewFile(c *gin.Context) {
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

	stat, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	if stat.Size() > 10*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large for preview (max 10MB)"})
		return
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read failed"})
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	contentType := "application/octet-stream"

	switch ext {
	case ".html", ".htm":
		contentType = "text/plain; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	case ".json":
		contentType = "application/json; charset=utf-8"
	case ".txt", ".log", ".csv", ".md", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf", ".env", ".sh", ".bash", ".zsh", ".py", ".go", ".java", ".c", ".cpp", ".h", ".hpp", ".rs", ".rb", ".php", ".ts", ".tsx", ".jsx", ".vue", ".svelte", ".sql", ".xml", ".svg":
		contentType = "text/plain; charset=utf-8"
	case ".png":
		contentType = "image/png"
	case ".jpg", ".jpeg":
		contentType = "image/jpeg"
	case ".gif":
		contentType = "image/gif"
	case ".webp":
		contentType = "image/webp"
	case ".ico":
		contentType = "image/x-icon"
	case ".bmp":
		contentType = "image/bmp"
	case ".pdf":
		contentType = "application/pdf"
	case ".mp4":
		contentType = "video/mp4"
	case ".mp3":
		contentType = "audio/mpeg"
	case ".wav":
		contentType = "audio/wav"
	}

	c.Data(http.StatusOK, contentType, data)
}

func (h *Handler) ListSkills(c *gin.Context) {
	type SkillInfo struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		HasDir      bool                   `json:"has_dir"`
		IsPrompt    bool                   `json:"is_prompt"`
		WhenToUse   string                 `json:"when_to_use,omitempty"`
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

	processed := map[string]bool{}
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
		processed[skillName] = true
		info := SkillInfo{
			Name:   skillName,
			HasDir: dirs[skillName],
		}

		if data, err := os.ReadFile(filepath.Join(h.skillDir, name)); err == nil {
			info.Description = extractPyDescription(string(data))
		}

		if dirs[skillName] {
			if meta := loadSkillMeta(h.skillDir, skillName); meta != nil {
				if meta.Description != "" {
					info.Description = meta.Description
				}
				info.WhenToUse = meta.WhenToUse
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

	// 纯提示词 skill: 含 SKILL.md 但无对应 .py 的目录
	for dirName := range dirs {
		if processed[dirName] {
			continue
		}
		meta := loadSkillMeta(h.skillDir, dirName)
		if meta == nil {
			continue
		}
		info := SkillInfo{
			Name:        dirName,
			HasDir:      true,
			IsPrompt:    true,
			Description: meta.Description,
			WhenToUse:   meta.WhenToUse,
		}
		if info.Description == "" {
			info.Description = "提示词技能 (查阅 SKILL.md)"
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

func buildSkillsSection(skillDir string) string {
	return buildSkillsSectionWithBudget(skillDir, 0)
}

// buildSkillsSectionWithBudget 按上下文窗口预算构建 skill 列表
// 参考 cc-haha: 仅注入 frontmatter (name/description/when_to_use), 正文懒加载
func buildSkillsSectionWithBudget(skillDir string, contextWindowTokens int) string {
	if skillDir == "" {
		return ""
	}
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return ""
	}

	dirs := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(entry.Name(), "_") {
			dirs[entry.Name()] = true
		}
	}

	var metas []*SkillMeta
	processed := map[string]bool{}

	// 1. 脚本 skill (.py + 可选 SKILL.md 目录)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".py") {
			continue
		}

		skillName := strings.TrimSuffix(name, ".py")
		processed[skillName] = true

		meta := &SkillMeta{
			Name:         skillName,
			HasScript:    true,
			UserInvocable: true,
		}
		// 优先从 SKILL.md 读取 frontmatter
		if dirs[skillName] {
			if m := loadSkillMeta(skillDir, skillName); m != nil {
				m.HasScript = true
				m.IsPromptSkill = false
				meta = m
			}
		}
		// description fallback: 从 .py 注释提取
		if meta.Description == "" {
			if data, err := os.ReadFile(filepath.Join(skillDir, name)); err == nil {
				meta.Description = extractPyDescription(string(data))
			}
		}
		if meta.Description == "" {
			meta.Description = "可用技能"
		}
		// 跳过 disable-model-invocation 的 skill
		if !meta.DisableModelInvoke {
			metas = append(metas, meta)
		}
	}

	// 2. 纯提示词 skill (有 SKILL.md 但无 .py 的目录)
	for dirName := range dirs {
		if processed[dirName] {
			continue
		}
		meta := loadSkillMeta(skillDir, dirName)
		if meta == nil {
			continue
		}
		meta.IsPromptSkill = true
		if meta.Description == "" {
			meta.Description = "提示词技能 (查阅 SKILL.md)"
		}
		if !meta.DisableModelInvoke {
			metas = append(metas, meta)
		}
	}

	return formatSkillsWithinBudget(metas, contextWindowTokens)
}

func buildSystemPrompt(memMgr *memory.Manager, userDir string, skillDir string) string {
	promptPath := filepath.Join(config.RootDir(), "assets", "sys_prompt.txt")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		data = []byte("You are GenericAgent, a helpful autonomous AI assistant.")
	}
	prompt := string(data)
	if userDir != "" {
		prompt += fmt.Sprintf("\n## 工作目录\n你的工作目录 (CWD) 是: %s。所有 file_write、file_read、code_run 生成的文件默认都在此目录下。skill_run 生成的文件如果不指定 output_path 也会在此目录。\n", userDir)
	}
	if skillDir != "" {
		prompt += fmt.Sprintf("\n## 技能目录\n技能安装目录: %s\n", skillDir)
	}
	prompt += buildSkillsSectionWithBudget(skillDir, 200000)
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
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Timestamp  string           `json:"timestamp"`
	ToolCalls  []llm.ToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

func (h *Handler) chatHistoryPath(userID int64) string {
	userDir := h.wsMgr.UserDir(userID)
	return filepath.Join(userDir, ".chat_history.json")
}

func (h *Handler) chatHistoryPathSession(userID int64, sessionID int64) string {
	userDir := h.wsMgr.UserDir(userID)
	return filepath.Join(userDir, fmt.Sprintf(".chat_history_%d.json", sessionID))
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

func (h *Handler) loadChatHistorySession(userID int64, sessionID int64) []ChatMessage {
	path := h.chatHistoryPathSession(userID, sessionID)
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

func (h *Handler) saveChatMessage(userID int64, role string, content string, toolCalls []llm.ToolCall, toolCallID string) {
	messages := h.loadChatHistory(userID)
	messages = append(messages, ChatMessage{
		Role:       role,
		Content:    content,
		Timestamp:  time.Now().Format("2006-01-02 15:04:05"),
		ToolCalls:  toolCalls,
		ToolCallID: toolCallID,
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

func (h *Handler) saveChatMessageSession(userID int64, sessionID int64, role string, content string, toolCalls []llm.ToolCall, toolCallID string) {
	messages := h.loadChatHistorySession(userID, sessionID)
	messages = append(messages, ChatMessage{
		Role:       role,
		Content:    content,
		Timestamp:  time.Now().Format("2006-01-02 15:04:05"),
		ToolCalls:  toolCalls,
		ToolCallID: toolCallID,
	})
	const maxMessages = 200
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return
	}
	os.WriteFile(h.chatHistoryPathSession(userID, sessionID), data, 0644)
}

// chatHistoryToMessages converts stored ChatMessage history to llm.Message format for agent context
func chatHistoryToMessages(history []ChatMessage) []llm.Message {
	var messages []llm.Message
	for _, m := range history {
		msg := llm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		}
		messages = append(messages, msg)
	}
	return messages
}

func (h *Handler) GetChatHistory(c *gin.Context) {
	userID := c.GetInt64("user_id")
	sessionIDStr := c.Query("session_id")
	if sessionIDStr != "" {
		sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
		if err == nil {
			messages := h.loadChatHistorySession(userID, sessionID)
			if messages == nil {
				messages = []ChatMessage{}
			}
			c.JSON(http.StatusOK, gin.H{"messages": messages})
			return
		}
	}
	messages := h.loadChatHistory(userID)
	if messages == nil {
		messages = []ChatMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

func (h *Handler) ClearChatHistory(c *gin.Context) {
	userID := c.GetInt64("user_id")
	sessionIDStr := c.Query("session_id")
	if sessionIDStr != "" {
		sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
		if err == nil {
			os.Remove(h.chatHistoryPathSession(userID, sessionID))
			c.JSON(http.StatusOK, gin.H{"message": "cleared"})
			return
		}
	}
	os.Remove(h.chatHistoryPath(userID))
	c.JSON(http.StatusOK, gin.H{"message": "cleared"})
}

func (h *Handler) ListSessions(c *gin.Context) {
	userID := c.GetInt64("user_id")
	sessions, err := h.users.ListSessions(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sessions"})
		return
	}
	if sessions == nil {
		sessions = []auth.Session{}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

func (h *Handler) CreateSession(c *gin.Context) {
	userID := c.GetInt64("user_id")
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	sess, err := h.users.CreateSession(userID, req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"session": sess})
}

func (h *Handler) DeleteSession(c *gin.Context) {
	userID := c.GetInt64("user_id")
	sessionIDStr := c.Query("session_id")
	if sessionIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id required"})
		return
	}
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session_id"})
		return
	}

	sess, err := h.users.GetSession(userID, sessionID)
	if err != nil || sess == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	sessions, _ := h.users.ListSessions(userID)
	if len(sessions) <= 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete the last session"})
		return
	}

	os.Remove(h.chatHistoryPathSession(userID, sessionID))

	if err := h.users.DeleteSession(userID, sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ==================== Task API (长任务能力) ====================

// StartTask 异步启动任务，立即返回 taskId
// POST /agent/run-task
func (h *Handler) StartTask(c *gin.Context) {
	userID := c.GetInt64("user_id")

	var req struct {
		Prompt    string `json:"prompt" binding:"required"`
		SessionID int64  `json:"session_id"`
		Goal      string `json:"goal"`
		PlanMode  bool   `json:"plan_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt required"})
		return
	}

	// 加载历史消息
	var history []llm.Message
	if req.SessionID > 0 {
		history = chatHistoryToMessages(h.loadChatHistorySession(userID, req.SessionID))
	} else {
		history = chatHistoryToMessages(h.loadChatHistory(userID))
	}

	t, err := h.taskRT.Start(task.TaskConfig{
		Type:      task.TypeMain,
		UserID:    userID,
		SessionID: req.SessionID,
		Prompt:    req.Prompt,
		Goal:      req.Goal,
		PlanMode:  req.PlanMode,
		History:   history,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id": t.State.ID,
		"status":  t.State.Status,
	})
}

// StreamTask SSE 订阅任务输出
// GET /agent/stream-task/:taskId
func (h *Handler) StreamTask(c *gin.Context) {
	taskID := c.Param("taskId")
	userID := c.GetInt64("user_id")

	// 验证任务属于该用户
	t, err := h.taskRT.Get(taskID)
	if err != nil || t.State.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch, unsub, err := h.taskRT.Subscribe(taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	defer unsub()

	notify := c.Request.Context().Done()
	flusher, _ := c.Writer.(http.Flusher)

	for {
		select {
		case item, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(item)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		case <-notify:
			return
		}
	}
}

// AbortTask 中断任务
// POST /agent/abort-task/:taskId
func (h *Handler) AbortTask(c *gin.Context) {
	taskID := c.Param("taskId")
	userID := c.GetInt64("user_id")

	t, err := h.taskRT.Get(taskID)
	if err != nil || t.State.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	if err := h.taskRT.Abort(taskID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "aborted"})
}

// ResumeTask 恢复暂停的任务(计划审批)
// POST /agent/resume-task/:taskId
func (h *Handler) ResumeTask(c *gin.Context) {
	taskID := c.Param("taskId")
	userID := c.GetInt64("user_id")

	t, err := h.taskRT.Get(taskID)
	if err != nil || t.State.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	var req struct {
		Approved bool `json:"approved"`
	}
	c.ShouldBindJSON(&req)

	if err := h.taskRT.Resume(taskID, req.Approved); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "resumed"})
}

// ListTasks 列出用户所有任务
// GET /agent/tasks
func (h *Handler) ListTasks(c *gin.Context) {
	userID := c.GetInt64("user_id")
	states, err := h.taskRT.ListByUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": states})
}

// GetTask 获取任务详情
// GET /agent/task/:taskId
func (h *Handler) GetTask(c *gin.Context) {
	taskID := c.Param("taskId")
	userID := c.GetInt64("user_id")

	t, err := h.taskRT.Get(taskID)
	if err != nil || t.State.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": t.State})
}
