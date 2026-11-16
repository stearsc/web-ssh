package websocket

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/coder/websocket"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/session"
)

type operation struct {
	binding    *session.Binding
	data       []byte
	cols, rows int
}

// 测试同步点默认 nil；安装后不可修改，读取指针受 Connection.mu 保护。
type connectionHooks struct {
	beforeRegister   func(*session.CommitResult)
	beforeAdmission  func()
	beforeSession    func()
	admitted         func(string)
	beforeWrite      func(string)
	beforeRead       func()
	waitingOperation func()
	beforeExecute    func()
}

type Connection struct {
	hooks                             *connectionHooks
	deadlineStamp                     atomic.Int64
	writeFailures                     atomic.Int64
	deadlineChanged                   chan struct{}
	pings                             atomic.Int64
	bindReceived                      chan struct{}
	service                           *Service
	raw                               net.Conn
	conn                              *ws.Conn
	login                             *auth.Session
	target                            *session.Session
	ctx                               context.Context
	cancel                            context.CancelFunc
	readCtx                           context.Context
	readCancel                        context.CancelFunc
	mu                                sync.Mutex // 发送项准入、终止、在途 Ping 登记使用同一短锁。
	accepted                          bool
	upgraded                          time.Time
	binding                           *session.Binding
	reason                            string
	deadline                          time.Time
	pingCancel                        context.CancelFunc
	nextPing                          time.Time
	wake                              chan struct{}
	terminated                        chan struct{}
	workersDone, supervisorDone, done chan struct{}
	operations                        chan operation
	controls                          chan statusMessage
}

func (c *Connection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Connection) Terminate(reason string) { c.terminate(reason, time.Time{}) }
func (c *Connection) terminate(reason string, applicationDeadline time.Time) {
	c.mu.Lock()
	if c.reason != "" {
		if !applicationDeadline.IsZero() && applicationDeadline.Before(c.deadline) {
			c.deadline = applicationDeadline
			c.deadlineStamp.Store(c.deadline.UnixNano())
			select {
			case c.deadlineChanged <- struct{}{}:
			default:
			}
		}
		c.mu.Unlock()
		return
	}
	c.terminateLocked(reason, applicationDeadline)
	accepted := c.accepted
	c.mu.Unlock()
	c.signal()
	if !accepted {
		c.raw.Close()
	}
}

// 调用方已持 c.mu；首因必须先于任何 Binding/context 取消固定。
func (c *Connection) terminateLocked(reason string, applicationDeadline time.Time) {
	c.reason = reason
	c.deadline = time.Now().Add(time.Duration(c.service.cfg.Websocket.CloseTimeout) * time.Second)
	if !applicationDeadline.IsZero() && applicationDeadline.Before(c.deadline) {
		c.deadline = applicationDeadline
	}
	c.deadlineStamp.Store(c.deadline.UnixNano())
	if c.binding != nil {
		c.target.Detach(c.binding)
	}
	if c.pingCancel != nil {
		c.pingCancel()
	}
	c.cancel()
	close(c.terminated)
}

// 只用于 Done/旧权限准入失败。真实 Read/Write/Ping 故障不调用此分流。
func (c *Connection) finishBindingExit(b *session.Binding, fallback string) {
	c.mu.Lock()
	if c.reason == "" {
		state := c.target.BindingDisposition(b)
		if state == session.BindingSuperseded {
			fallback = "taken_over"
		}
		c.terminateLocked(fallback, time.Time{})
	}
	c.mu.Unlock()
	c.signal()
}

func (c *Connection) testHooks() *connectionHooks {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hooks
}

func (c *Connection) run() {
	go c.supervise()
	var workers sync.WaitGroup
	workers.Add(5)
	go func() { defer workers.Done(); c.reader() }()
	go func() { defer workers.Done(); c.writer() }()
	go func() { defer workers.Done(); c.execute() }()
	go func() {
		defer workers.Done()
		timer := time.NewTimer(time.Until(c.upgraded.Add(time.Duration(c.service.cfg.Websocket.BindTimeout) * time.Second)))
		defer timer.Stop()
		select {
		case <-c.bindReceived:
		case <-c.ctx.Done():
		case <-timer.C:
			c.Terminate("BIND_TIMEOUT")
		}
	}()
	go func() {
		defer workers.Done()
		timer := time.NewTimer(time.Until(c.login.Expires))
		defer timer.Stop()
		select {
		case <-c.ctx.Done():
		case <-timer.C:
			c.Terminate("AUTH_REQUIRED")
		}
	}()
	workers.Wait()
	c.cancel()
	c.readCancel()
	close(c.workersDone)
	<-c.supervisorDone
	c.mu.Lock()
	if c.binding != nil {
		c.binding.Replay.Data = nil
	}
	c.mu.Unlock()
	c.service.log.Info("WebSocket 已回收", "session_id", c.target.View().ID, "reason", c.reason)
	c.service.remove(c)
	close(c.done)
}
func (c *Connection) supervise() {
	select {
	case <-c.terminated:
	case <-c.workersDone:
		close(c.supervisorDone)
		return
	}
	for {
		deadline := time.Unix(0, c.deadlineStamp.Load()) // 监督器不获取 Writer 调度锁。
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-c.workersDone:
			timer.Stop()
			close(c.supervisorDone)
			return
		case <-c.deadlineChanged:
			timer.Stop()
		case <-timer.C:
			c.raw.Close()
			<-c.workersDone
			close(c.supervisorDone)
			return
		}
	}
}

func (c *Connection) readMessage() (ws.MessageType, []byte, error) {
	ctx, cancel := context.WithCancel(c.readCtx)
	defer cancel()
	kind, reader, err := c.conn.Reader(ctx)
	if err != nil {
		return kind, nil, err
	}
	limit := c.service.cfg.Websocket.MaxControlMessageSize
	if kind == ws.MessageBinary {
		limit = c.service.cfg.Websocket.MaxInputMessageSize
	}
	finished := make(chan struct{})
	timer := time.AfterFunc(time.Duration(c.service.cfg.Websocket.MessageReadTimeout)*time.Second, func() { cancel(); c.Terminate("INVALID_MESSAGE"); close(finished) })
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if !timer.Stop() {
		<-finished
		return kind, nil, errStopped
	}
	if int64(len(data)) > limit {
		c.Terminate("MESSAGE_TOO_LARGE")
		return kind, nil, errStopped
	}
	return kind, data, err
}
func (c *Connection) reader() {
	defer c.Terminate("NETWORK_ERROR")
	for {
		if hooks := c.testHooks(); hooks != nil && hooks.beforeRead != nil {
			hooks.beforeRead()
		}
		kind, data, err := c.readMessage()
		if err != nil {
			return
		}
		c.mu.Lock()
		b, reason := c.binding, c.reason
		c.mu.Unlock()
		if reason != "" {
			return
		}
		if b == nil {
			if kind != ws.MessageText {
				c.Terminate("INVALID_MESSAGE")
				return
			}
			var msg bindMessage
			if decode(data, &msg) != nil || msg.Type != "bind" || msg.Credential == "" || msg.Offset == nil {
				c.Terminate("INVALID_MESSAGE")
				return
			}
			close(c.bindReceived) // 首条完整 bind 已收到；提交等待使用独立操作预算。
			result, err := c.target.Attach(c.ctx, c.login, msg.Credential, *msg.Offset, c.Terminate)
			if err != nil {
				c.Terminate(err.Error())
				return
			}
			if hooks := c.testHooks(); hooks != nil && hooks.beforeRegister != nil {
				hooks.beforeRegister(result)
			}
			if !c.registerBinding(result) {
				return
			}
			continue
		}
		op := operation{binding: b}
		if kind == ws.MessageBinary {
			op.data = data
			if len(data) == 0 {
				continue
			}
		} else {
			var msg resizeMessage
			if decode(data, &msg) != nil {
				var duplicate bindMessage
				if decode(data, &duplicate) == nil && duplicate.Type == "bind" {
					c.Terminate("ALREADY_BOUND")
				} else {
					c.Terminate("INVALID_MESSAGE")
				}
				return
			}
			if msg.Type != "resize" || msg.Cols == nil || msg.Rows == nil || msg.Generation == nil {
				c.Terminate("INVALID_MESSAGE")
				return
			}
			if *msg.Generation != b.Generation {
				c.Terminate("BIND_CONFLICT")
				return
			}
			op.cols, op.rows = *msg.Cols, *msg.Rows
		}
		if hooks := c.testHooks(); hooks != nil && hooks.waitingOperation != nil {
			hooks.waitingOperation()
		}
		timer := time.NewTimer(time.Duration(c.service.cfg.Session.OperationWaitTimeout) * time.Second)
		select {
		case c.operations <- op:
			timer.Stop()
		case <-b.Done():
			timer.Stop()
			c.finishBindingExit(b, "NETWORK_ERROR")
			return
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			c.Terminate("INPUT_BACKPRESSURE")
			return
		}
	}
}

// Reader 是提交结果唯一收尾所有者。独立短函数保证所有登记分支及时执行
// 锁外收尾，不把 defer 延迟到 Reader 整个生命周期结束，也不嵌套新旧连接锁。
func (c *Connection) registerBinding(result *session.CommitResult) bool {
	defer result.FinishDisplaced()
	b := result.NewBinding
	c.mu.Lock()
	if c.reason != "" {
		c.mu.Unlock()
		c.target.Detach(b)
		return false
	}
	c.binding = b // 即使已经失效，也登记清理归属；Writer 始终重新做锁内准入。
	state := c.target.BindingDisposition(b)
	if state == session.BindingSuperseded {
		c.terminateLocked("taken_over", time.Time{})
		c.mu.Unlock()
		c.signal()
		return false
	}
	if state != session.BindingCurrent {
		c.mu.Unlock()
		// Close/Revoke 已撤权但可能仍在回收 SSH；等待原生命周期发布终止，
		// 不让 Reader defer 抢成 NETWORK_ERROR，也不提前伪造 closed。
		result.FinishDisplaced()
		<-c.terminated
		c.target.Detach(b)
		return false
	}
	c.nextPing = time.Now().Add(time.Duration(c.service.cfg.Websocket.PingInterval) * time.Second)
	c.mu.Unlock()
	c.signal()
	return true
}

func (c *Connection) execute() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case op := <-c.operations:
			if hooks := c.testHooks(); hooks != nil && hooks.beforeExecute != nil {
				hooks.beforeExecute()
			}
			if err := c.target.Execute(op.binding, op.data, op.cols, op.rows); err != nil {
				if err.Error() == "BIND_CONFLICT" || err.Error() == "INPUT_BACKPRESSURE" {
					c.finishBindingExit(op.binding, err.Error())
				} else {
					c.Terminate(err.Error())
				}
				return
			}
		}
	}
}

// NotifyStatus 不等待网络，满队列通过同一终止入口回收通道。
func (c *Connection) NotifyStatus() {
	event := statusMessage{"status", "CONNECTED"}
	c.mu.Lock()
	if c.reason != "" {
		c.mu.Unlock()
		return
	}
	select {
	case c.controls <- event:
		c.mu.Unlock()
		c.signal()
	default:
		c.mu.Unlock()
		c.Terminate("SLOW_CLIENT")
	}
}
func (c *Connection) writer() {
	prefix, replayed := 0, 0
	var cursor uint64
	for {
		hooks := c.testHooks()
		if hooks != nil && hooks.beforeAdmission != nil {
			hooks.beforeAdmission()
		}
		c.mu.Lock()
		if c.reason != "" {
			reason, deadline := c.reason, c.deadline
			c.mu.Unlock()
			c.finish(reason, deadline)
			return
		}
		b := c.binding
		if b == nil {
			c.mu.Unlock()
			select {
			case <-c.wake:
			case <-c.terminated:
			}
			continue
		}
		var data []byte
		var event any
		var selectionError error
		var pingCtx context.Context
		var pingCancel context.CancelFunc
		label := ""
		kind := ws.MessageText
		if hooks != nil && hooks.beforeSession != nil {
			hooks.beforeSession()
		}
		state := c.target.WithSendAdmission(b, func() {
			// c.mu → Session.mu；Buffer.Read 内部仅短取 Buffer.mu。
			if !time.Now().Before(c.nextPing) {
				pingCtx, pingCancel = context.WithTimeout(context.Background(), time.Duration(c.service.cfg.Websocket.PongTimeout)*time.Second)
				c.pingCancel = pingCancel
				return
			}
			if prefix == 0 {
				event = bindSuccess{"bind_success", b.Generation, b.Replay.Start, b.Replay.End, b.Cols, b.Rows}
				prefix = 1
				cursor = b.Replay.End
				label = "prefix"
			} else if prefix == 1 && b.Replay.Start > b.Replay.Requested {
				event = historyTruncated{"history_truncated", b.Replay.Requested, b.Replay.Start, b.Replay.End, b.Replay.Start - b.Replay.Requested}
				prefix = 2
				label = "prefix"
			} else {
				prefix = 2
				select {
				case event = <-c.controls:
					label = "status"
				default:
				}
				if event == nil {
					kind = ws.MessageBinary
					if replayed < len(b.Replay.Data) {
						end := min(len(b.Replay.Data), replayed+int(c.service.cfg.Websocket.OutputChunkSize))
						data = b.Replay.Data[replayed:end]
						replayed = end
						label = "history"
					} else {
						b.Replay.Data = nil
						data, selectionError = c.target.Buffer().Read(cursor, int(c.service.cfg.Websocket.OutputChunkSize))
						cursor += uint64(len(data))
						if len(data) > 0 {
							label = "live"
						}
					}
				}
			}
			// 唯一 Writer 的局部不可变项即唯一在途项；此处仍同时持有两把锁。
			if label != "" && hooks != nil && hooks.admitted != nil {
				hooks.admitted(label)
			}
		})
		nextPing := c.nextPing
		c.mu.Unlock()
		if state != session.BindingCurrent {
			if state == session.BindingSuperseded {
				c.finishBindingExit(b, "taken_over")
				continue
			}
			// 无效但非接管：原 Close/Revoke 负责终止发布，不能抢先发 closed。
			select {
			case <-c.wake:
			case <-c.terminated:
			}
			continue
		}
		if selectionError != nil {
			c.Terminate(selectionError.Error())
			continue
		}
		if pingCtx != nil {
			err := c.conn.Ping(pingCtx)
			if err == nil {
				c.pings.Add(1)
			}
			pingCancel()
			c.mu.Lock()
			c.pingCancel = nil
			c.nextPing = time.Now().Add(time.Duration(c.service.cfg.Websocket.PingInterval) * time.Second)
			c.mu.Unlock()
			if err != nil {
				c.Terminate("NETWORK_ERROR")
			}
			continue
		}
		if event != nil {
			var err error
			data, err = json.Marshal(event)
			if err != nil {
				c.Terminate("INTERNAL_ERROR")
				continue
			}
		}
		if len(data) > 0 {
			if hooks != nil && hooks.beforeWrite != nil {
				hooks.beforeWrite(label)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.service.cfg.Websocket.WriteTimeout)*time.Second)
			err := c.conn.Write(ctx, kind, data)
			cancel()
			if err != nil {
				c.writeFailures.Add(1)
				c.Terminate("SLOW_CLIENT")
			}
			continue
		}
		timer := time.NewTimer(time.Until(nextPing))
		select {
		case <-c.wake:
		case <-c.target.Buffer().Changed():
		case <-timer.C:
		case <-c.terminated:
		}
		timer.Stop()
	}
}
func (c *Connection) finish(reason string, deadline time.Time) {
	code := closeCode(reason)
	if reason != "NETWORK_ERROR" && time.Now().Before(deadline) {
		var event any = errorMessage{"error", reason, "终端连接已结束：" + reason}
		if reason == "taken_over" {
			event = takenOverMessage{"taken_over", "此会话已由其他连接接管"}
		} else if strings.HasPrefix(reason, "closed:") {
			event = closedMessage{"closed", strings.TrimPrefix(reason, "closed:"), "SSH 会话已关闭"}
			code = 1000
		}
		data, _ := json.Marshal(event)
		ctx, cancel := context.WithDeadline(context.Background(), minTime(deadline, time.Now().Add(time.Duration(c.service.cfg.Websocket.WriteTimeout)*time.Second)))
		_ = c.conn.Write(ctx, ws.MessageText, data)
		cancel()
	}
	if !time.Now().Before(deadline) {
		c.raw.Close()
		_ = c.conn.CloseNow()
		return
	}
	_ = c.conn.Close(ws.StatusCode(code), "")
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
