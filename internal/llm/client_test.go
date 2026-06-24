package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// m4: HTTP 客户端连接池复用 — doRequest 应初始化 httpClient
func TestClient_HTTPClientInitialized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":      map[string]any{"role": "assistant", "content": "hi"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()

	client := &Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		MaxTokens:      100,
		Temperature:    0.7,
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	// 直接调用 doRequest 验证 httpClient 被初始化
	body, _ := json.Marshal(map[string]any{
		"model": "test-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	resp, err := client.doRequest(body, false)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
	resp.Body.Close()

	if client.httpClient == nil {
		t.Fatal("httpClient should be initialized after doRequest")
	}

	// 第二次调用应复用同一 httpClient
	firstClient := client.httpClient
	resp2, err := client.doRequest(body, false)
	if err != nil {
		t.Fatalf("second doRequest failed: %v", err)
	}
	resp2.Body.Close()

	if client.httpClient != firstClient {
		t.Error("httpClient should be reused (same instance)")
	}
}

// m4: token 用量聚合
func TestClient_TotalUsageAggregation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// content chunk
		evt := map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": "hello"},
			}},
		}
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		// done chunk with usage
		doneEvt := map[string]any{
			"choices": []map[string]any{{
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
			},
		}
		data, _ = json.Marshal(doneEvt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := &Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		Stream:         true,
		MaxTokens:      100,
		Temperature:    0.7,
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	// 第一次请求
	ch, err := client.ChatStream(ChatParams{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	for range ch {
	}

	usage := client.GetTotalUsage()
	if usage.InputTokens != 100 || usage.OutputTokens != 50 {
		t.Errorf("after 1st request: usage = %+v, want {100, 50}", usage)
	}

	// 第二次请求，应累加
	ch2, _ := client.ChatStream(ChatParams{
		Messages: []Message{{Role: "user", Content: "hi again"}},
	})
	for range ch2 {
	}

	usage = client.GetTotalUsage()
	if usage.InputTokens != 200 || usage.OutputTokens != 100 {
		t.Errorf("after 2nd request: usage = %+v, want {200, 100}", usage)
	}
}

// m4: reasoning_content 被捕获到 StreamChunk.Reasoning
func TestClient_ParseReasoningFromSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// reasoning chunk
		evt := map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"reasoning_content": "let me think..."},
			}},
		}
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		// content chunk
		evt = map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": "final answer"},
			}},
		}
		data, _ = json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := &Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		Stream:         true,
		MaxTokens:      100,
		Temperature:    0.7,
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	ch, err := client.ChatStream(ChatParams{
		Messages: []Message{{Role: "user", Content: "think"}},
	})
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}

	var reasoning, text string
	for chunk := range ch {
		if chunk.Reasoning != "" {
			reasoning += chunk.Reasoning
		}
		if chunk.Text != "" {
			text += chunk.Text
		}
	}

	if !strings.Contains(reasoning, "let me think") {
		t.Errorf("reasoning not captured: got %q", reasoning)
	}
	if !strings.Contains(text, "final answer") {
		t.Errorf("text not captured: got %q", text)
	}
}

// m4: 并发请求下 token 聚合线程安全
func TestClient_TotalUsageConcurrentSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		doneEvt := map[string]any{
			"choices": []map[string]any{{
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
			},
		}
		data, _ := json.Marshal(doneEvt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := &Client{
		APIBase:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		Stream:         true,
		MaxTokens:      100,
		Temperature:    0.7,
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    10 * time.Second,
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, _ := client.ChatStream(ChatParams{
				Messages: []Message{{Role: "user", Content: "hi"}},
			})
			for range ch {
			}
		}()
	}
	wg.Wait()

	usage := client.GetTotalUsage()
	// 10 个请求，每个 input=10, output=5
	if usage.InputTokens != 100 || usage.OutputTokens != 50 {
		t.Errorf("concurrent usage = %+v, want {100, 50}", usage)
	}
}
