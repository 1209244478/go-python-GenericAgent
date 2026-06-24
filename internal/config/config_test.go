package config

import (
	"sync"
	"testing"
)

// m6: ReloadIfChanged 并发调用不应 panic 或 data race
// 注意：此测试不依赖真实配置文件，仅验证并发安全性
func TestReloadIfChanged_ConcurrentNoPanic(t *testing.T) {
	// 创建一个空 Config，path 为空时 ReloadIfChanged 不会真正加载
	cfg := &Config{
		LLMs:   make(map[string]LLMConfig),
		Mixins: make(map[string]MixinConfig),
	}
	globalCfg = cfg

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// path 为空，changed 永远为 false，不会触发 globalCfg 更新
			// 但仍验证锁机制不 panic
			cfg.ReloadIfChanged()
		}()
	}
	wg.Wait()

	// 验证 Get() 在并发下也能安全返回
	var wg2 sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			_ = Get()
		}()
	}
	wg2.Wait()
}

// m6: Config 读写锁基本功能
func TestConfig_RWMutex(t *testing.T) {
	cfg := &Config{
		LLMs: map[string]LLMConfig{
			"default": {APIBase: "http://test", Model: "test"},
		},
	}

	cfg.mu.RLock()
	llm := cfg.LLMs["default"]
	cfg.mu.RUnlock()

	if llm.Model != "test" {
		t.Errorf("expected model 'test', got %s", llm.Model)
	}

	// 写锁更新
	cfg.mu.Lock()
	cfg.LLMs["default"] = LLMConfig{Model: "updated"}
	cfg.mu.Unlock()

	cfg.mu.RLock()
	updated := cfg.LLMs["default"]
	cfg.mu.RUnlock()

	if updated.Model != "updated" {
		t.Errorf("expected updated model, got %s", updated.Model)
	}
}
