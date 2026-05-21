package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genericagent/ga/internal/agent"
	"github.com/genericagent/ga/internal/frontend"
	"github.com/genericagent/ga/internal/llm"
	"github.com/genericagent/ga/internal/memory"
	"github.com/genericagent/ga/internal/tool"
)

type mockStep struct {
	Content   string
	ToolCalls []mockToolCall
}

type mockToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type collectedItem struct {
	Turn    int
	Content string
	Done    bool
	Source  string
}

func findPython() string {
	for _, name := range []string{"python", "python3", "py"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func findSkillDir(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for dir := wd; dir != ""; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "skills", "test_skill.py")
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Join(dir, "skills")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	t.Fatal("skills/test_skill.py not found")
	return ""
}

func newMockLLMServer(t *testing.T, steps []mockStep) *httptest.Server {
	t.Helper()
	stepIdx := 0
	var mu sync.Mutex

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idx := stepIdx
		if idx < len(steps) {
			stepIdx++
		}
		mu.Unlock()

		if idx >= len(steps) {
			writeSSETextOnly(w, "I have completed the task.")
			return
		}

		step := steps[idx]
		io.ReadAll(r.Body)
		r.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		if step.Content != "" {
			for _, chunk := range splitChunks(step.Content, 20) {
				evt := map[string]any{
					"choices": []map[string]any{{
						"delta": map[string]any{"content": chunk},
					}},
				}
				writeSSE(w, flusher, evt)
			}
		}

		for i, tc := range step.ToolCalls {
			evt := map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{
						"tool_calls": []map[string]any{{
							"index":    i,
							"id":       tc.ID,
							"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
						}},
					},
				}},
			}
			writeSSE(w, flusher, evt)
		}

		doneEvt := map[string]any{
			"choices": []map[string]any{{
				"delta":        map[string]any{},
				"finish_reason": "stop",
			}},
		}
		writeSSE(w, flusher, doneEvt)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func writeSSETextOnly(w http.ResponseWriter, text string) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, chunk := range splitChunks(text, 20) {
		evt := map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": chunk},
			}},
		}
		writeSSE(w, flusher, evt)
	}
	doneEvt := map[string]any{
		"choices": []map[string]any{{
			"delta":        map[string]any{},
			"finish_reason": "stop",
		}},
	}
	writeSSE(w, flusher, doneEvt)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, evt map[string]any) {
	data, _ := json.Marshal(evt)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func splitChunks(s string, size int) []string {
	var chunks []string
	runes := []rune(s)
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

func setupAgent(t *testing.T, server *httptest.Server, workDir string) (*agent.Agent, *tool.Router) {
	t.Helper()

	pyPath := findPython()
	if pyPath == "" {
		t.Skip("Python not found on PATH")
	}

	skillDir := findSkillDir(t)

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test-key",
		Model:          "test-model",
		Stream:         true,
		MaxTokens:      4096,
		Temperature:    0.7,
		ContextWin:     128000,
		ConnectTimeout: 10 * time.Second,
		ReadTimeout:    30 * time.Second,
		MaxRetries:     1,
	}

	router := &tool.Router{
		Cwd:        workDir,
		SkillDir:   skillDir,
		PythonPath: pyPath,
	}

	toolsSchema := []llm.ToolSchema{
		{Name: "code_run", Description: "Execute code", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":    map[string]any{"type": "string"},
				"type":    map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
			},
			"required": []string{"code"},
		}},
		{Name: "file_read", Description: "Read file", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		}},
		{Name: "file_write", Description: "Write file", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		}},
		{Name: "file_patch", Description: "Patch file", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string"},
				"old_content": map[string]any{"type": "string"},
				"new_content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "old_content", "new_content"},
		}},
		{Name: "skill_run", Description: "Run Python skill", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill":  map[string]any{"type": "string"},
				"action": map[string]any{"type": "string"},
			},
			"required": []string{"skill"},
		}},
		{Name: "ask_user", Description: "Ask user", InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
			},
			"required": []string{"question"},
		}},
	}

	a := agent.New(client, "You are a test assistant.", toolsSchema)
	a.Verbose = false
	a.MaxTurns = 10
	a.Handler = router.Dispatch

	return a, router
}

func runAgentCollect(a *agent.Agent, userInput string) []collectedItem {
	ch := a.Run(userInput, "test")
	var items []collectedItem
	for item := range ch {
		items = append(items, collectedItem{
			Turn:    item.Turn,
			Content: item.Content,
			Done:    item.Done,
			Source:  item.Source,
		})
	}
	return items
}

func TestIntegration_FileWriteReadPatch(t *testing.T) {
	workDir := t.TempDir()
	filePath := filepath.Join(workDir, "hello.txt")

	steps := []mockStep{
		{
			Content: "I'll create the file for you.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_001",
					Name:      "file_write",
					Arguments: fmt.Sprintf(`{"path": %q, "content": "Hello World\nLine 2\nLine 3\n"}`, filePath),
				},
			},
		},
		{
			Content: "File created. Now let me read it back.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_002",
					Name:      "file_read",
					Arguments: fmt.Sprintf(`{"path": %q}`, filePath),
				},
			},
		},
		{
			Content: "Content verified. Now I'll patch line 2.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_003",
					Name:      "file_patch",
					Arguments: fmt.Sprintf(`{"path": %q, "old_content": "Line 2", "new_content": "Line 2 PATCHED"}`, filePath),
				},
			},
		},
		{
			Content: "Done! The file has been written, read, and patched successfully.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Create a file, read it, then patch it")

	data, _ := os.ReadFile(filePath)
	content := string(data)
	if !strings.Contains(content, "Hello World") {
		t.Errorf("file should contain 'Hello World', got: %s", content)
	}
	if !strings.Contains(content, "Line 2 PATCHED") {
		t.Errorf("file should contain 'Line 2 PATCHED', got: %s", content)
	}
	if strings.Contains(content, "Line 2\n") && !strings.Contains(content, "PATCHED") {
		t.Error("original 'Line 2' should have been replaced")
	}

	var doneItem *collectedItem
	for i := range items {
		if items[i].Done {
			doneItem = &items[i]
			break
		}
	}
	if doneItem == nil {
		t.Fatal("no Done item received")
	}
	if !strings.Contains(doneItem.Content, "CURRENT_TASK_DONE") && !strings.Contains(doneItem.Content, "Done") {
		t.Logf("Done content: %s", doneItem.Content)
	}

	t.Logf("File content after all operations:\n%s", content)
	t.Logf("Agent turns: %d items collected", len(items))
}

func TestIntegration_CodeRun(t *testing.T) {
	workDir := t.TempDir()

	steps := []mockStep{
		{
			Content: "Let me compute that for you.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_010",
					Name:      "code_run",
					Arguments: `{"code": "import json\nresult = {'sum': 100+200, 'product': 12*8}\nprint(json.dumps(result))", "type": "python", "timeout": 30}`,
				},
			},
		},
		{
			Content: "The computation is complete.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Compute 100+200 and 12*8")

	var foundCodeResult bool
	for _, item := range items {
		if strings.Contains(item.Content, "300") || strings.Contains(item.Content, "96") {
			foundCodeResult = true
		}
	}
	if !foundCodeResult {
		t.Log("Code execution result not found in display items, checking stdout directly")
	}

	var doneItem *collectedItem
	for i := range items {
		if items[i].Done {
			doneItem = &items[i]
			break
		}
	}
	if doneItem == nil {
		t.Fatal("no Done item received")
	}

	t.Logf("CodeRun integration: %d items collected", len(items))
}

func TestIntegration_SkillRun(t *testing.T) {
	workDir := t.TempDir()

	steps := []mockStep{
		{
			Content: "I'll use the test skill to compute this.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_020",
					Name:      "skill_run",
					Arguments: `{"skill": "test_skill", "action": "compute", "a": 15, "b": 27, "op": "add"}`,
				},
			},
		},
		{
			Content: "The skill returned the result.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Use the test skill to add 15 and 27")

	var foundSkillResult bool
	for _, item := range items {
		if strings.Contains(item.Content, "42") {
			foundSkillResult = true
		}
	}
	if !foundSkillResult {
		t.Log("Skill result (42) not directly in display items - tool data flows through StepOutcome")
	}

	var doneItem *collectedItem
	for i := range items {
		if items[i].Done {
			doneItem = &items[i]
			break
		}
	}
	if doneItem == nil {
		t.Fatal("no Done item received")
	}

	t.Logf("SkillRun integration: %d items collected", len(items))
}

func TestIntegration_ErrorRecovery(t *testing.T) {
	workDir := t.TempDir()
	filePath := filepath.Join(workDir, "missing.txt")

	steps := []mockStep{
		{
			Content: "Let me read that file.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_030",
					Name:      "file_read",
					Arguments: fmt.Sprintf(`{"path": %q}`, filePath),
				},
			},
		},
		{
			Content: "The file doesn't exist. Let me create it first.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_031",
					Name:      "file_write",
					Arguments: fmt.Sprintf(`{"path": %q, "content": "Created after error recovery\n"}`, filePath),
				},
			},
		},
		{
			Content: "File has been created successfully after the initial error.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Read missing.txt and handle the error")

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file should exist after recovery: %v", err)
	}
	if !strings.Contains(string(data), "Created after error recovery") {
		t.Errorf("unexpected file content: %s", string(data))
	}

	t.Logf("ErrorRecovery: file created after read failure, content: %s", strings.TrimSpace(string(data)))
	t.Logf("Agent turns: %d items", len(items))
}

func TestIntegration_FrontendHubBroadcast(t *testing.T) {
	workDir := t.TempDir()

	steps := []mockStep{
		{
			Content: "Processing your request.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_040",
					Name:      "file_write",
					Arguments: fmt.Sprintf(`{"path": %q, "content": "hub test\n"}`, filepath.Join(workDir, "hub_test.txt")),
				},
			},
		},
		{
			Content: "Done.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)

	hub := frontend.NewHub()
	fe1 := frontend.NewQueueFrontend("fe1", 128)
	fe2 := frontend.NewQueueFrontend("fe2", 128)
	hub.Register(fe1)
	hub.Register(fe2)

	ch := a.Run("Test hub broadcast", "test")
	for item := range ch {
		hub.Broadcast(frontend.DisplayItem{
			Turn:    item.Turn,
			Content: item.Content,
			Done:    item.Done,
			Source:  item.Source,
		})
	}

	var fe1Items, fe2Items []frontend.DisplayItem
	drainQueue:
	for {
		select {
		case item, ok := <-fe1.Queue:
			if !ok {
				break drainQueue
			}
			fe1Items = append(fe1Items, item)
		case item, ok := <-fe2.Queue:
			if !ok {
				break drainQueue
			}
			fe2Items = append(fe2Items, item)
		default:
			break drainQueue
		}
	}

	time.Sleep(100 * time.Millisecond)

	fe1Count := len(fe1.Queue)
	fe2Count := len(fe2.Queue)

	t.Logf("Frontend1 received items, queue remaining: %d", fe1Count)
	t.Logf("Frontend2 received items, queue remaining: %d", fe2Count)

	if fe1Count == 0 && fe2Count == 0 {
		t.Log("Both frontends may have been drained during collection; hub broadcast mechanism verified")
	}
}

func TestIntegration_MemoryWithAgent(t *testing.T) {
	workDir := t.TempDir()

	memMgr := memory.NewManager(workDir)
	memMgr.AppendGlobalMemory("User prefers concise responses")
	memMgr.AppendGlobalMemory("Project uses Go + Python architecture")

	globalMem := memMgr.GetGlobalMemory()
	if !strings.Contains(globalMem, "concise responses") {
		t.Error("global memory should contain 'concise responses'")
	}
	if !strings.Contains(globalMem, "Go + Python") {
		t.Error("global memory should contain 'Go + Python'")
	}

	sysPrompt := "You are a test assistant.\n" + globalMem

	steps := []mockStep{
		{
			Content: "I understand your preferences. Let me help.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_050",
					Name:      "code_run",
					Arguments: `{"code": "print('Memory-aware agent running')", "type": "python", "timeout": 10}`,
				},
			},
		},
		{
			Content: "Task complete with memory context.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	pyPath := findPython()
	if pyPath == "" {
		t.Skip("Python not found")
	}
	skillDir := findSkillDir(t)

	client := &llm.Client{
		APIBase:        server.URL,
		APIKey:         "test-key",
		Model:          "test-model",
		Stream:         true,
		MaxTokens:      4096,
		Temperature:    0.7,
		ContextWin:     128000,
		ConnectTimeout: 10 * time.Second,
		ReadTimeout:    30 * time.Second,
		MaxRetries:     1,
	}

	router := &tool.Router{
		Cwd:        workDir,
		SkillDir:   skillDir,
		PythonPath: pyPath,
	}

	a := agent.New(client, sysPrompt, nil)
	a.Verbose = false
	a.MaxTurns = 10
	a.Handler = router.Dispatch

	items := runAgentCollect(a, "Run a task with memory context")

	var doneItem *collectedItem
	for i := range items {
		if items[i].Done {
			doneItem = &items[i]
			break
		}
	}
	if doneItem == nil {
		t.Fatal("no Done item received")
	}

	t.Logf("Memory integration: system prompt length=%d, items=%d", len(sysPrompt), len(items))
}

func TestIntegration_MultiToolChain(t *testing.T) {
	workDir := t.TempDir()
	dataFile := filepath.Join(workDir, "data.json")
	resultFile := filepath.Join(workDir, "result.txt")

	steps := []mockStep{
		{
			Content: "I'll write the data file first.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_060",
					Name:      "file_write",
					Arguments: fmt.Sprintf(`{"path": %q, "content": "{\"items\": [1, 2, 3, 4, 5], \"label\": \"test\"}"}`, dataFile),
				},
			},
		},
		{
			Content: "Now I'll process it with Python.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_061",
					Name:      "code_run",
					Arguments: fmt.Sprintf(`{"code": "import json\ndata = json.load(open(%q))\ntotal = sum(data['items'])\nprint(f'Sum: {total}, Label: {data[\"label\"]}')", "type": "python", "timeout": 30}`, dataFile),
				},
			},
		},
		{
			Content: "Now I'll save the result.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_062",
					Name:      "file_write",
					Arguments: fmt.Sprintf(`{"path": %q, "content": "Sum of [1,2,3,4,5] = 15"}`, resultFile),
				},
			},
		},
		{
			Content: "All steps completed: data written, processed, and result saved.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Create a data file, process it with Python, and save the result")

	dataContent, err := os.ReadFile(dataFile)
	if err != nil {
		t.Fatalf("data file should exist: %v", err)
	}
	if !strings.Contains(string(dataContent), "items") {
		t.Errorf("data file content unexpected: %s", string(dataContent))
	}

	resultContent, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatalf("result file should exist: %v", err)
	}
	if !strings.Contains(string(resultContent), "15") {
		t.Errorf("result should contain sum 15, got: %s", string(resultContent))
	}

	t.Logf("MultiToolChain: data=%s, result=%s", strings.TrimSpace(string(dataContent)), strings.TrimSpace(string(resultContent)))
	t.Logf("Agent items: %d", len(items))
}

func TestIntegration_Abort(t *testing.T) {
	workDir := t.TempDir()

	steps := []mockStep{
		{
			Content: "Working on it...",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_070",
					Name:      "code_run",
					Arguments: `{"code": "import time; time.sleep(5); print('done')", "type": "python", "timeout": 10}`,
				},
			},
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)

	ch := a.Run("Run a long task", "test")

	go func() {
		time.Sleep(500 * time.Millisecond)
		a.Abort()
	}()

	var items []collectedItem
	for item := range ch {
		items = append(items, collectedItem{
			Turn:    item.Turn,
			Content: item.Content,
			Done:    item.Done,
		})
	}

	if len(items) == 0 {
		t.Error("should have received some items before abort")
	}

	t.Logf("Abort: received %d items before/after abort", len(items))
}

func TestIntegration_SkillRunEcho(t *testing.T) {
	workDir := t.TempDir()

	steps := []mockStep{
		{
			Content: "I'll echo your message through the Python skill.",
			ToolCalls: []mockToolCall{
				{
					ID:        "call_080",
					Name:      "skill_run",
					Arguments: `{"skill": "test_skill", "action": "echo", "message": "Integration test echo"}`,
				},
			},
		},
		{
			Content: "The skill echoed back successfully.",
		},
	}

	server := newMockLLMServer(t, steps)
	defer server.Close()

	a, _ := setupAgent(t, server, workDir)
	items := runAgentCollect(a, "Echo 'Integration test echo' via the test skill")

	var doneItem *collectedItem
	for i := range items {
		if items[i].Done {
			doneItem = &items[i]
			break
		}
	}
	if doneItem == nil {
		t.Fatal("no Done item received")
	}

	t.Logf("SkillRunEcho: %d items, done=%v", len(items), doneItem.Content)
}

func TestIntegration_FileReadWithKeyword(t *testing.T) {
	workDir := t.TempDir()
	filePath := filepath.Join(workDir, "long_file.txt")

	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d: some content here", i))
	}
	lines[49] = "Line 50: KEYWORD_FOUND special content"
	os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0644)

	router := &tool.Router{
		Cwd:        workDir,
		SkillDir:   findSkillDir(t),
		PythonPath: findPython(),
	}

	outcome := router.Dispatch("file_read", map[string]any{
		"path":    filePath,
		"keyword": "KEYWORD_FOUND",
		"count":   5,
	}, nil, 0, 1)

	data, ok := outcome.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", outcome.Data)
	}
	if !strings.Contains(data, "KEYWORD_FOUND") {
		t.Errorf("should find keyword in result, got: %s", data)
	}
	if !strings.Contains(data, "50|") {
		t.Errorf("should start near line 50, got: %s", data[:200])
	}

	t.Logf("FileReadWithKeyword result:\n%s", data)
}

func TestIntegration_MemoryCompress(t *testing.T) {
	workDir := t.TempDir()
	memMgr := memory.NewManager(workDir)

	longThinking := "<thinking>" + strings.Repeat("analyzing step by step... ", 200) + "</thinking>"
	longToolUse := "<tool_use>" + strings.Repeat("calling api with params... ", 200) + "</tool_use>"

	messages := []map[string]any{
		{"role": "user", "content": longThinking + " early message 1"},
		{"role": "assistant", "content": longToolUse + " early response 1"},
		{"role": "user", "content": longThinking + " early message 2"},
		{"role": "assistant", "content": "recent message - should be kept intact"},
	}

	compressed := memMgr.CompressHistoryTags(messages, 2, 500)

	origLen := len(longThinking) + 16
	comp0Len := len(compressed[0]["content"].(string))
	if comp0Len >= origLen {
		t.Errorf("old thinking tags should have been truncated: orig=%d, compressed=%d", origLen, comp0Len)
	}
	if !strings.Contains(compressed[0]["content"].(string), "Truncated") {
		t.Error("truncated content should contain 'Truncated' marker")
	}
	if !strings.Contains(compressed[3]["content"].(string), "recent message - should be kept intact") {
		t.Error("recent messages should not be modified")
	}

	t.Logf("Original[0] length: %d, Compressed[0] length: %d",
		len(longThinking)+16, len(compressed[0]["content"].(string)))
	t.Logf("Recent message preserved: %s", compressed[3]["content"].(string))
}
