package websocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	// ErrConnectionClosed 连接已关闭错误
	ErrConnectionClosed = errors.New("websocket: 连接已关闭")

	// ErrConnectionNotFound 连接未找到错误
	ErrConnectionNotFound = errors.New("websocket: 连接未找到")

	// ErrInvalidMessage 无效消息错误
	ErrInvalidMessage = errors.New("websocket: 无效的消息")

	// ErrChannelNotFound 频道未找到错误
	ErrChannelNotFound = errors.New("websocket: 频道未找到")

	// DefaultUpgrader 默认的WebSocket升级器
	DefaultUpgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有源，生产环境应该限制
		},
	}
)

// Message WebSocket消息结构
type Message struct {
	// Type 消息类型 (如"chat", "notification", "system"等)
	Type string `json:"type"`

	// Event 事件名称 (如"message.sent", "user.joined"等)
	Event string `json:"event"`

	// Channel 频道名称
	Channel string `json:"channel,omitempty"`

	// Data 消息数据
	Data interface{} `json:"data,omitempty"`

	// SenderID 发送者ID
	SenderID string `json:"sender_id,omitempty"`

	// Timestamp 时间戳
	Timestamp int64 `json:"timestamp"`
}

// Connection WebSocket连接封装
type Connection struct {
	// ID 连接唯一标识
	ID string

	// UserID 用户ID，可选
	UserID string

	// Socket 原始WebSocket连接
	Socket *websocket.Conn

	// Manager 所属的管理器
	Manager *Manager

	// Channels 已加入的频道列表
	Channels map[string]bool

	// SendChan 发送消息的通道
	SendChan chan *Message

	// Closed 连接是否已关闭
	Closed bool

	// Metadata 连接元数据，可存储任意信息
	Metadata map[string]interface{}

	// mu 互斥锁
	mu sync.RWMutex
}

// Manager WebSocket连接管理器
type Manager struct {
	// connections 所有活跃连接
	connections map[string]*Connection

	// channels 频道映射
	channels map[string]map[string]*Connection

	// authFunc 可选的认证函数
	authFunc func(r *http.Request) (string, map[string]interface{}, error)

	// beforeConnect 连接前钩子
	beforeConnect func(r *http.Request) bool

	// afterConnect 连接后钩子
	afterConnect func(conn *Connection)

	// beforeDisconnect 断开连接前钩子
	beforeDisconnect func(conn *Connection)

	// afterDisconnect 断开连接后钩子
	afterDisconnect func(conn *Connection)

	// messageHandler 消息处理函数
	messageHandler func(conn *Connection, msg *Message) error

	// upgrader WebSocket升级器
	upgrader websocket.Upgrader

	// mu 互斥锁
	mu sync.RWMutex
}

// NewManager 创建新的WebSocket管理器
func NewManager() *Manager {
	return &Manager{
		connections: make(map[string]*Connection),
		channels:    make(map[string]map[string]*Connection),
		upgrader:    DefaultUpgrader,
	}
}

// SetAuthFunc 设置认证函数
func (m *Manager) SetAuthFunc(fn func(r *http.Request) (string, map[string]interface{}, error)) {
	m.authFunc = fn
}

// SetBeforeConnect 设置连接前钩子
func (m *Manager) SetBeforeConnect(fn func(r *http.Request) bool) {
	m.beforeConnect = fn
}

// SetAfterConnect 设置连接后钩子
func (m *Manager) SetAfterConnect(fn func(conn *Connection)) {
	m.afterConnect = fn
}

// SetBeforeDisconnect 设置断开连接前钩子
func (m *Manager) SetBeforeDisconnect(fn func(conn *Connection)) {
	m.beforeDisconnect = fn
}

// SetAfterDisconnect 设置断开连接后钩子
func (m *Manager) SetAfterDisconnect(fn func(conn *Connection)) {
	m.afterDisconnect = fn
}

// SetMessageHandler 设置消息处理函数
func (m *Manager) SetMessageHandler(fn func(conn *Connection, msg *Message) error) {
	m.messageHandler = fn
}

// SetUpgrader 设置WebSocket升级器
func (m *Manager) SetUpgrader(upgrader websocket.Upgrader) {
	m.upgrader = upgrader
}

// HandleRequest 处理WebSocket连接请求
func (m *Manager) HandleRequest(w http.ResponseWriter, r *http.Request) {
	// 检查连接前钩子
	if m.beforeConnect != nil && !m.beforeConnect(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 升级HTTP连接为WebSocket
	socket, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Could not upgrade connection", http.StatusInternalServerError)
		return
	}

	var userID string
	var metadata map[string]interface{}

	// 验证用户身份（如果设置了authFunc）
	if m.authFunc != nil {
		userID, metadata, err = m.authFunc(r)
		if err != nil {
			socket.Close()
			return
		}
	}

	// 创建新连接
	conn := &Connection{
		ID:       uuid.New().String(),
		UserID:   userID,
		Socket:   socket,
		Manager:  m,
		Channels: make(map[string]bool),
		SendChan: make(chan *Message, 256),
		Closed:   false,
		Metadata: metadata,
	}

	// 注册连接
	m.mu.Lock()
	m.connections[conn.ID] = conn
	m.mu.Unlock()

	// 启动读写goroutine
	go conn.readPump()
	go conn.writePump()

	// 调用连接后钩子
	if m.afterConnect != nil {
		m.afterConnect(conn)
	}
}

// GetConnections 获取所有连接
func (m *Manager) GetConnections() []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conns := make([]*Connection, 0, len(m.connections))
	for _, conn := range m.connections {
		conns = append(conns, conn)
	}
	return conns
}

// GetConnection 获取指定ID的连接
func (m *Manager) GetConnection(id string) (*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, exists := m.connections[id]
	if !exists {
		return nil, ErrConnectionNotFound
	}
	return conn, nil
}

// GetConnectionsByUserID 获取指定用户ID的所有连接
func (m *Manager) GetConnectionsByUserID(userID string) []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var conns []*Connection
	for _, conn := range m.connections {
		if conn.UserID == userID {
			conns = append(conns, conn)
		}
	}
	return conns
}

// Broadcast 向所有连接广播消息
func (m *Manager) Broadcast(msg *Message) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, conn := range m.connections {
		select {
		case conn.SendChan <- msg:
		default:
			go conn.Close()
		}
	}
}

// BroadcastToChannel 向特定频道的所有连接广播消息
func (m *Manager) BroadcastToChannel(channel string, msg *Message) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	channelConns, exists := m.channels[channel]
	if !exists {
		return ErrChannelNotFound
	}

	for _, conn := range channelConns {
		select {
		case conn.SendChan <- msg:
		default:
			go conn.Close()
		}
	}
	return nil
}

// BroadcastToUser 向特定用户的所有连接广播消息
func (m *Manager) BroadcastToUser(userID string, msg *Message) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, conn := range m.connections {
		if conn.UserID == userID {
			select {
			case conn.SendChan <- msg:
			default:
				go conn.Close()
			}
		}
	}
}

// RemoveConnection 移除并关闭连接
func (m *Manager) RemoveConnection(conn *Connection) {
	// 调用断开连接前钩子
	if m.beforeDisconnect != nil {
		m.beforeDisconnect(conn)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 从所有频道中移除连接
	for channel := range conn.Channels {
		if channelConns, exists := m.channels[channel]; exists {
			delete(channelConns, conn.ID)
			// 如果频道为空，删除频道
			if len(channelConns) == 0 {
				delete(m.channels, channel)
			}
		}
	}

	// 从连接映射中移除
	delete(m.connections, conn.ID)

	// 关闭连接
	conn.mu.Lock()
	if !conn.Closed {
		close(conn.SendChan)
		conn.Closed = true
	}
	conn.mu.Unlock()

	// 调用断开连接后钩子
	if m.afterDisconnect != nil {
		m.afterDisconnect(conn)
	}
}

// AddConnectionToChannel 将连接添加到频道
func (m *Manager) AddConnectionToChannel(conn *Connection, channel string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 确保频道存在
	if _, exists := m.channels[channel]; !exists {
		m.channels[channel] = make(map[string]*Connection)
	}

	// 添加连接到频道
	m.channels[channel][conn.ID] = conn

	// 更新连接的频道列表
	conn.mu.Lock()
	conn.Channels[channel] = true
	conn.mu.Unlock()
}

// RemoveConnectionFromChannel 从频道中移除连接
func (m *Manager) RemoveConnectionFromChannel(conn *Connection, channel string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 从频道中移除连接
	if channelConns, exists := m.channels[channel]; exists {
		delete(channelConns, conn.ID)
		// 如果频道为空，删除频道
		if len(channelConns) == 0 {
			delete(m.channels, channel)
		}
	}

	// 更新连接的频道列表
	conn.mu.Lock()
	delete(conn.Channels, channel)
	conn.mu.Unlock()
}

// GetChannels 获取所有频道
func (m *Manager) GetChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	channels := make([]string, 0, len(m.channels))
	for channel := range m.channels {
		channels = append(channels, channel)
	}
	return channels
}

// GetChannelConnections 获取频道中的所有连接
func (m *Manager) GetChannelConnections(channel string) ([]*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	channelConns, exists := m.channels[channel]
	if !exists {
		return nil, ErrChannelNotFound
	}

	conns := make([]*Connection, 0, len(channelConns))
	for _, conn := range channelConns {
		conns = append(conns, conn)
	}
	return conns, nil
}

// readPump 处理从WebSocket连接读取消息
func (c *Connection) readPump() {
	defer func() {
		c.Manager.RemoveConnection(c)
		c.Socket.Close()
	}()

	c.Socket.SetReadLimit(4096) // 设置最大消息大小
	c.Socket.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Socket.SetPongHandler(func(string) error {
		c.Socket.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.Socket.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				fmt.Printf("WebSocket错误: %v\n", err)
			}
			break
		}

		// 解析消息
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			fmt.Printf("解析消息错误: %v\n", err)
			continue
		}

		// 设置发送者ID和时间戳
		msg.SenderID = c.UserID
		if msg.Timestamp == 0 {
			msg.Timestamp = time.Now().UnixNano() / int64(time.Millisecond)
		}

		// 调用消息处理函数（如果设置了）
		if c.Manager.messageHandler != nil {
			if err := c.Manager.messageHandler(c, &msg); err != nil {
				fmt.Printf("处理消息错误: %v\n", err)
				continue
			}
		}

		// 特殊消息类型处理
		switch msg.Type {
		case "join":
			// 加入频道
			if msg.Channel != "" {
				c.Manager.AddConnectionToChannel(c, msg.Channel)
			}
		case "leave":
			// 离开频道
			if msg.Channel != "" {
				c.Manager.RemoveConnectionFromChannel(c, msg.Channel)
			}
		case "channel":
			// 频道消息
			if msg.Channel != "" {
				c.Manager.BroadcastToChannel(msg.Channel, &msg)
			}
		}
	}
}

// writePump 处理向WebSocket连接写入消息
func (c *Connection) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.Socket.Close()
	}()

	for {
		select {
		case msg, ok := <-c.SendChan:
			c.Socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// 通道已关闭
				c.Socket.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Socket.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}

			// 序列化消息
			data, err := json.Marshal(msg)
			if err != nil {
				fmt.Printf("序列化消息错误: %v\n", err)
				continue
			}

			w.Write(data)

			// 添加队列中的所有消息
			n := len(c.SendChan)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				nextMsg := <-c.SendChan
				nextData, err := json.Marshal(nextMsg)
				if err != nil {
					fmt.Printf("序列化消息错误: %v\n", err)
					continue
				}
				w.Write(nextData)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.Socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Socket.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// SendMessage 发送消息到连接
func (c *Connection) SendMessage(msg *Message) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.Closed {
		return ErrConnectionClosed
	}

	// 设置时间戳（如果未设置）
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixNano() / int64(time.Millisecond)
	}

	select {
	case c.SendChan <- msg:
		return nil
	default:
		go c.Close()
		return ErrConnectionClosed
	}
}

// Close 关闭连接
func (c *Connection) Close() {
	c.Manager.RemoveConnection(c)
}

// IsInChannel 检查连接是否在指定频道中
func (c *Connection) IsInChannel(channel string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, exists := c.Channels[channel]
	return exists
}

// JoinChannel 加入频道
func (c *Connection) JoinChannel(channel string) {
	c.Manager.AddConnectionToChannel(c, channel)
}

// LeaveChannel 离开频道
func (c *Connection) LeaveChannel(channel string) {
	c.Manager.RemoveConnectionFromChannel(c, channel)
}

// GetChannels 获取连接已加入的所有频道
func (c *Connection) GetChannels() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	channels := make([]string, 0, len(c.Channels))
	for channel := range c.Channels {
		channels = append(channels, channel)
	}
	return channels
}

// SetMetadata 设置连接元数据
func (c *Connection) SetMetadata(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Metadata == nil {
		c.Metadata = make(map[string]interface{})
	}
	c.Metadata[key] = value
}

// GetMetadata 获取连接元数据
func (c *Connection) GetMetadata(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.Metadata == nil {
		return nil, false
	}
	value, exists := c.Metadata[key]
	return value, exists
}
