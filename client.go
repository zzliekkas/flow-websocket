package websocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// Client WebSocket客户端
type Client struct {
	// URL WebSocket服务器URL
	URL string

	// Connection 当前连接
	Connection *websocket.Conn

	// AuthToken 认证令牌
	AuthToken string

	// Closed 客户端是否已关闭
	Closed bool

	// OnConnect 连接建立回调
	OnConnect func()

	// OnDisconnect 连接断开回调
	OnDisconnect func(err error)

	// OnMessage 接收消息回调
	OnMessage func(msg *Message)

	// OnError 错误回调
	OnError func(err error)

	// MessageHandlers 特定消息类型处理器
	MessageHandlers map[string]func(msg *Message)

	// EventHandlers 特定事件处理器
	EventHandlers map[string]func(msg *Message)

	// ReconnectInterval 重连间隔
	ReconnectInterval time.Duration

	// MaxReconnectAttempts 最大重连尝试次数
	MaxReconnectAttempts int

	// reconnectCount 当前重连次数
	reconnectCount int

	// sendChan 发送队列
	sendChan chan *Message

	// done 完成信号
	done chan struct{}
}

// NewClient 创建新的WebSocket客户端
func NewClient(url string) *Client {
	return &Client{
		URL:                  url,
		AuthToken:            "",
		Closed:               true,
		ReconnectInterval:    5 * time.Second,
		MaxReconnectAttempts: 10,
		MessageHandlers:      make(map[string]func(msg *Message)),
		EventHandlers:        make(map[string]func(msg *Message)),
		sendChan:             make(chan *Message, 100),
		done:                 make(chan struct{}),
	}
}

// Connect 连接到WebSocket服务器
func (c *Client) Connect() error {
	if !c.Closed {
		return errors.New("client already connected")
	}

	// 解析URL
	u, err := url.Parse(c.URL)
	if err != nil {
		return err
	}

	// 添加认证令牌到URL查询参数
	if c.AuthToken != "" {
		q := u.Query()
		q.Set("token", c.AuthToken)
		u.RawQuery = q.Encode()
	}

	// 建立连接
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}

	c.Connection = conn
	c.Closed = false
	c.reconnectCount = 0
	c.done = make(chan struct{})

	// 启动读写协程
	go c.readPump()
	go c.writePump()

	// 调用连接回调
	if c.OnConnect != nil {
		c.OnConnect()
	}

	return nil
}

// Close 关闭WebSocket连接
func (c *Client) Close() {
	if c.Closed {
		return
	}

	// 发送关闭信号
	close(c.done)

	// 关闭连接
	if c.Connection != nil {
		c.Connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.Connection.Close()
	}

	c.Closed = true
}

// Send 发送消息
func (c *Client) Send(msg *Message) error {
	if c.Closed {
		return errors.New("client is closed")
	}

	// 设置时间戳（如果未设置）
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixNano() / int64(time.Millisecond)
	}

	select {
	case c.sendChan <- msg:
		return nil
	default:
		return errors.New("send queue is full")
	}
}

// JoinChannel 加入频道
func (c *Client) JoinChannel(channel string) error {
	return c.Send(&Message{
		Type:    "join",
		Channel: channel,
	})
}

// LeaveChannel 离开频道
func (c *Client) LeaveChannel(channel string) error {
	return c.Send(&Message{
		Type:    "leave",
		Channel: channel,
	})
}

// SendToChannel 发送消息到频道
func (c *Client) SendToChannel(channel string, event string, data interface{}) error {
	return c.Send(&Message{
		Type:    "channel",
		Event:   event,
		Channel: channel,
		Data:    data,
	})
}

// SendEvent 发送事件消息
func (c *Client) SendEvent(event string, data interface{}) error {
	return c.Send(&Message{
		Type:  "event",
		Event: event,
		Data:  data,
	})
}

// OnMessageType 注册特定消息类型处理器
func (c *Client) OnMessageType(msgType string, handler func(msg *Message)) {
	c.MessageHandlers[msgType] = handler
}

// OnEventType 注册特定事件处理器
func (c *Client) OnEventType(event string, handler func(msg *Message)) {
	c.EventHandlers[event] = handler
}

// readPump 处理从WebSocket连接读取消息
func (c *Client) readPump() {
	defer func() {
		c.handleDisconnect(nil)
	}()

	for {
		_, data, err := c.Connection.ReadMessage()
		if err != nil {
			if c.OnError != nil {
				c.OnError(err)
			}
			return
		}

		// 解析消息
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			if c.OnError != nil {
				c.OnError(fmt.Errorf("解析消息错误: %v", err))
			}
			continue
		}

		// 处理消息
		c.handleMessage(&msg)
	}
}

// writePump 处理向WebSocket连接写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
	}()

	for {
		select {
		case <-c.done:
			return
		case msg := <-c.sendChan:
			c.Connection.SetWriteDeadline(time.Now().Add(10 * time.Second))

			// 序列化消息
			data, err := json.Marshal(msg)
			if err != nil {
				if c.OnError != nil {
					c.OnError(fmt.Errorf("序列化消息错误: %v", err))
				}
				continue
			}

			if err := c.Connection.WriteMessage(websocket.TextMessage, data); err != nil {
				if c.OnError != nil {
					c.OnError(err)
				}
				return
			}
		case <-ticker.C:
			c.Connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage 处理接收到的消息
func (c *Client) handleMessage(msg *Message) {
	// 调用通用消息回调
	if c.OnMessage != nil {
		c.OnMessage(msg)
	}

	// 调用特定消息类型处理器
	if handler, exists := c.MessageHandlers[msg.Type]; exists {
		handler(msg)
	}

	// 调用特定事件处理器
	if handler, exists := c.EventHandlers[msg.Event]; exists {
		handler(msg)
	}
}

// handleDisconnect 处理断开连接
func (c *Client) handleDisconnect(err error) {
	if c.Closed {
		return
	}

	c.Connection.Close()

	// 调用断开连接回调
	if c.OnDisconnect != nil {
		c.OnDisconnect(err)
	}

	// 自动重连
	if c.reconnectCount < c.MaxReconnectAttempts {
		c.reconnectCount++
		time.Sleep(c.ReconnectInterval)

		// 尝试重连
		if err := c.Connect(); err != nil && c.OnError != nil {
			c.OnError(fmt.Errorf("重连失败: %v", err))
		}
	} else {
		c.Closed = true
	}
}
