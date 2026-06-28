package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/genericagent/ga/internal/dotenv"
)

type LLMConfig struct {
	APIBase      string  `json:"api_base"`
	APIKey       string  `json:"api_key"`
	Model        string  `json:"model"`
	APIMode      string  `json:"api_mode"`
	MaxTokens    int     `json:"max_tokens"`
	Temperature  float64 `json:"temperature"`
	ContextWin   int     `json:"context_win"`
	Name         string  `json:"name"`
	Stream       bool    `json:"stream"`
	ConnectTimeout int   `json:"connect_timeout"`
	ReadTimeout    int   `json:"read_timeout"`
	MaxRetries     int   `json:"max_retries"`
	ExtraSysPrompt string `json:"extra_sys_prompt"`
}

type Config struct {
	LLMs map[string]LLMConfig `json:"-"`

	mu     sync.RWMutex
	path   string
	mtime  int64
}

var (
	globalCfg *Config
	once      sync.Once
)

func RootDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	dir, _ := os.Getwd()
	return dir
}

func Load() (*Config, error) {
	cfg := &Config{}
	root := RootDir()

	env := dotenv.FindAndLoad(root)
	if llmCfg := llmFromEnv(env); llmCfg != nil {
		cfg.LLMs = map[string]LLMConfig{"default": *llmCfg}
		globalCfg = cfg
		return cfg, nil
	}

	pyPath := filepath.Join(root, "mykey.py")
	jsonPath := filepath.Join(root, "mykey.json")

	if _, err := os.Stat(pyPath); err == nil {
		return nil, fmt.Errorf("mykey.py detected: please convert to mykey.json or use .env for Go engine")
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("no LLM config found: create .env with LLM_API_KEY or mykey.json")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mykey.json parse error: %w", err)
	}

	cfg.LLMs = make(map[string]LLMConfig)

	for key, val := range raw {
		if containsAny(key, "api", "config") {
			var lc LLMConfig
			if err := json.Unmarshal(val, &lc); err == nil && lc.APIBase != "" {
				if lc.APIMode == "" {
					lc.APIMode = "chat_completions"
				}
				if lc.MaxTokens == 0 {
					lc.MaxTokens = 8192
				}
				if lc.ContextWin == 0 {
					lc.ContextWin = 128000
				}
				if lc.ConnectTimeout == 0 {
					lc.ConnectTimeout = 30
				}
				if lc.ReadTimeout == 0 {
					lc.ReadTimeout = 300
				}
				if lc.MaxRetries == 0 {
					lc.MaxRetries = 3
				}
				cfg.LLMs[key] = lc
			}
		}
	}

	cfg.path = jsonPath
	if fi, err := os.Stat(jsonPath); err == nil {
		cfg.mtime = fi.ModTime().UnixNano()
	}

	globalCfg = cfg
	return cfg, nil
}

func llmFromEnv(env map[string]string) *LLMConfig {
	apiKey := dotenv.Get(env, "LLM_API_KEY", "")
	if apiKey == "" {
		return nil
	}
	cfg := &LLMConfig{
		APIBase: dotenv.Get(env, "LLM_API_BASE", "https://api.openai.com/v1"),
		APIKey:  apiKey,
		Model:   dotenv.Get(env, "LLM_MODEL", "gpt-4o"),
		APIMode: "chat_completions",
		Name:    "default",
		Stream:  true,
	}
	if v := dotenv.Get(env, "LLM_MAX_TOKENS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxTokens = n
		}
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if v := dotenv.Get(env, "LLM_TEMPERATURE", ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Temperature = f
		}
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.7
	}
	if v := dotenv.Get(env, "LLM_STREAM", ""); v != "" {
		cfg.Stream = strings.ToLower(v) == "true"
	}
	if v := dotenv.Get(env, "LLM_CONNECT_TIMEOUT", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ConnectTimeout = n
		}
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 30
	}
	if v := dotenv.Get(env, "LLM_READ_TIMEOUT", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ReadTimeout = n
		}
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 300
	}
	if v := dotenv.Get(env, "LLM_MAX_RETRIES", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxRetries = n
		}
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	cfg.ContextWin = 128000
	return cfg
}

func (c *Config) ReloadIfChanged() bool {
	c.mu.RLock()
	changed := false
	if c.path != "" {
		if fi, err := os.Stat(c.path); err == nil {
			if fi.ModTime().UnixNano() != c.mtime {
				changed = true
			}
		}
	}
	c.mu.RUnlock()

	if changed {
		newCfg, err := Load()
		if err == nil && globalCfg != nil {
			// 加锁保护 globalCfg 的并发更新
			globalCfg.mu.Lock()
			globalCfg.LLMs = newCfg.LLMs
			globalCfg.path = newCfg.path
			globalCfg.mtime = newCfg.mtime
			globalCfg.mu.Unlock()
		}
	}
	return changed
}

func Get() *Config {
	if globalCfg == nil {
		cfg, err := Load()
		if err != nil {
			panic(err)
		}
		return cfg
	}
	return globalCfg
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
