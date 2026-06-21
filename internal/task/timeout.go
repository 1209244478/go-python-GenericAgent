package task

import (
	"context"
	"sync"
	"time"
)

// IdleTimeoutMonitor 空闲超时监控
// 参考 cc-haha idleTimeout.ts: 长时间无活动自动暂停任务
type IdleTimeoutMonitor struct {
	mu          sync.Mutex
	idleTimeout time.Duration // 空闲超时时长 (0=禁用)
	lastActivity time.Time
	timer        *time.Timer
	onTimeout    func() // 超时回调
	stopped      bool
}

// NewIdleTimeoutMonitor 创建空闲超时监控
func NewIdleTimeoutMonitor(timeout time.Duration, onTimeout func()) *IdleTimeoutMonitor {
	return &IdleTimeoutMonitor{
		idleTimeout:  timeout,
		lastActivity: time.Now(),
		onTimeout:    onTimeout,
	}
}

// Start 启动监控
func (m *IdleTimeoutMonitor) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.idleTimeout <= 0 || m.stopped {
		return
	}

	m.lastActivity = time.Now()
	m.timer = time.AfterFunc(m.idleTimeout, func() {
		m.mu.Lock()
		if m.stopped {
			m.mu.Unlock()
			return
		}
		elapsed := time.Since(m.lastActivity)
		m.mu.Unlock()

		if elapsed >= m.idleTimeout {
			if m.onTimeout != nil {
				m.onTimeout()
			}
		}
	})
}

// Activity 注册活动 (重置计时器)
func (m *IdleTimeoutMonitor) Activity() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastActivity = time.Now()
	if m.timer != nil && !m.stopped {
		m.timer.Reset(m.idleTimeout)
	}
}

// Stop 停止监控
func (m *IdleTimeoutMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopped = true
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}

// CombinedAbortSignal 组合取消信号
// 参考 cc-haha combinedAbortSignal.ts: 合并多个取消源
// 取消源: context.Cancel / 任务超时 / 空闲超时 / 用户中断
type CombinedAbortSignal struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	sources  []string // 记录取消原因
}

// NewCombinedAbortSignal 创建组合取消信号
func NewCombinedAbortSignal(parent context.Context) *CombinedAbortSignal {
	ctx, cancel := context.WithCancel(parent)
	return &CombinedAbortSignal{
		ctx:     ctx,
		cancel:  cancel,
		sources: make([]string, 0),
	}
}

// Trigger 触发取消
func (c *CombinedAbortSignal) Trigger(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ctx.Err() != nil {
		return // 已取消
	}

	c.sources = append(c.sources, source)
	c.cancel()
}

// Done 返回取消通道
func (c *CombinedAbortSignal) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err 返回取消错误
func (c *CombinedAbortSignal) Err() error {
	return c.ctx.Err()
}

// Sources 返回取消原因列表
func (c *CombinedAbortSignal) Sources() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]string, len(c.sources))
	copy(result, c.sources)
	return result
}

// Context 返回底层 context
func (c *CombinedAbortSignal) Context() context.Context {
	return c.ctx
}

// GracefulShutdown 优雅关闭管理器
// 参考 cc-haha gracefulShutdown.ts: 给任务一段缓冲时间完成当前操作
type GracefulShutdown struct {
	mu              sync.Mutex
	shutdownTimeout time.Duration // 优雅关闭超时 (默认 30s)
	gracePeriod     time.Duration // 宽限期 (默认 5s)
	shutdownCh      chan struct{}
	completeCh      chan struct{}
	started         bool
}

// NewGracefulShutdown 创建优雅关闭管理器
func NewGracefulShutdown(shutdownTimeout, gracePeriod time.Duration) *GracefulShutdown {
	if shutdownTimeout <= 0 {
		shutdownTimeout = 30 * time.Second
	}
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Second
	}
	return &GracefulShutdown{
		shutdownTimeout: shutdownTimeout,
		gracePeriod:     gracePeriod,
		shutdownCh:      make(chan struct{}),
		completeCh:      make(chan struct{}),
	}
}

// InitiateShutdown 发起优雅关闭
// 返回通道: 关闭时表示可以强制终止
func (g *GracefulShutdown) InitiateShutdown() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.started {
		return g.shutdownCh
	}
	g.started = true

	go func() {
		// 宽限期: 允许当前操作继续
		select {
		case <-time.After(g.gracePeriod):
		case <-g.completeCh:
			close(g.shutdownCh)
			return
		}

		// 等待完成或超时
		select {
		case <-time.After(g.shutdownTimeout):
		case <-g.completeCh:
		}

		close(g.shutdownCh)
	}()

	return g.shutdownCh
}

// Complete 标记任务完成 (停止等待)
func (g *GracefulShutdown) Complete() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.started {
		select {
		case <-g.completeCh:
		default:
			close(g.completeCh)
		}
	}
}

// IsShuttingDown 检查是否正在关闭
func (g *GracefulShutdown) IsShuttingDown() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started
}
