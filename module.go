package websocket

import (
	"github.com/zzliekkas/flow/v3"
)

// WebSocketModule implements flow.Module for easy registration into a Flow engine.
type WebSocketModule struct {
	manager *Manager
}

// NewModule creates a new WebSocketModule with the given Manager.
// If manager is nil, a new default Manager will be created.
func NewModule(manager *Manager) *WebSocketModule {
	if manager == nil {
		manager = NewManager()
	}
	return &WebSocketModule{manager: manager}
}

// Name returns the module name.
func (m *WebSocketModule) Name() string {
	return "websocket"
}

// Init registers the WebSocket manager into Flow's DI container.
func (m *WebSocketModule) Init(e *flow.Engine) error {
	return e.Provide(func() *Manager { return m.manager })
}
