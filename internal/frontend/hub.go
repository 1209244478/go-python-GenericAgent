package frontend

import (
	"fmt"
	"sync"

	"github.com/genericagent/ga/internal/agent"
)

type DisplayItem = agent.DisplayItem

type Frontend interface {
	Name() string
	Send(item DisplayItem)
	Start(agent *agent.Agent) error
}

type Hub struct {
	frontends map[string]Frontend
	mu        sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		frontends: make(map[string]Frontend),
	}
}

func (h *Hub) Register(f Frontend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frontends[f.Name()] = f
}

func (h *Hub) Unregister(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.frontends, name)
}

func (h *Hub) Broadcast(item DisplayItem) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, f := range h.frontends {
		go f.Send(item)
	}
}

func (h *Hub) StartAll(a *agent.Agent) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, f := range h.frontends {
		if err := f.Start(a); err != nil {
			return fmt.Errorf("frontend %s start error: %w", f.Name(), err)
		}
	}
	return nil
}

type CLIFrontend struct {
	verbose bool
}

func NewCLIFrontend(verbose bool) *CLIFrontend {
	return &CLIFrontend{verbose: verbose}
}

func (f *CLIFrontend) Name() string { return "cli" }

func (f *CLIFrontend) Send(item DisplayItem) {
	fmt.Print(item.Content)
	if item.Done {
		fmt.Println()
	}
}

func (f *CLIFrontend) Start(a *agent.Agent) error {
	return nil
}

type QueueFrontend struct {
	Name_    string
	Queue    chan DisplayItem
}

func NewQueueFrontend(name string, bufSize int) *QueueFrontend {
	if bufSize == 0 {
		bufSize = 128
	}
	return &QueueFrontend{
		Name_: name,
		Queue: make(chan DisplayItem, bufSize),
	}
}

func (f *QueueFrontend) Name() string { return f.Name_ }

func (f *QueueFrontend) Send(item DisplayItem) {
	select {
	case f.Queue <- item:
	default:
	}
}

func (f *QueueFrontend) Start(a *agent.Agent) error {
	return nil
}
