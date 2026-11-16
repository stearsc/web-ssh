package websocket

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/session"
)

type takeoverPeer struct {
	raw    net.Conn
	reader *bufio.Reader
	server *Connection
}

func s3Wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("S3 同步点超时")
	}
	var zero T
	return zero
}
func s3Gate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return ch, release
}
func s3Harness(t *testing.T, configure func(*config.Config)) *harness {
	return newHarness(t, func(c *config.Config) {
		c.Websocket.PingInterval = 60
		c.Websocket.BindTimeout = 3
		if configure != nil {
			configure(c)
		}
	})
}
func s3Peer(t *testing.T, h *harness, hooks *connectionHooks) *takeoverPeer {
	t.Helper()
	raw, r := h.raw(t, false)
	h.service.mu.Lock()
	var c *Connection
	for candidate := range h.service.conns {
		if candidate.raw.RemoteAddr().String() == raw.LocalAddr().String() {
			c = candidate
			break
		}
	}
	h.service.mu.Unlock()
	if c == nil {
		t.Fatal("未找到真实 Upgrade 对应连接")
	}
	c.mu.Lock()
	c.hooks = hooks
	c.mu.Unlock()
	return &takeoverPeer{raw, r, c}
}
func (p *takeoverPeer) send(t *testing.T, op byte, data []byte) {
	t.Helper()
	if _, err := p.raw.Write(frame(op, data)); err != nil {
		t.Fatal(err)
	}
}
func (p *takeoverPeer) bind(t *testing.T, h *harness, mode string, offset uint64) {
	t.Helper()
	c, err := h.manager.Prepare(h.login, h.session.View().ID, mode, h.session.View().Generation)
	if err != nil {
		t.Fatal(err)
	}
	p.token(t, c.Credential, offset)
}
func (p *takeoverPeer) token(t *testing.T, token string, offset uint64) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"type": "bind", "credential": token, "requested_start_offset": offset})
	p.send(t, 1, data)
}
func (p *takeoverPeer) prefix(t *testing.T) bindSuccess {
	t.Helper()
	for {
		op, data, err := readFrame(p.reader)
		if err != nil {
			t.Fatal(err)
		}
		if op == 9 {
			p.send(t, 10, data)
			continue
		}
		var event bindSuccess
		if op != 1 || json.Unmarshal(data, &event) != nil || event.Type != "bind_success" {
			t.Fatalf("prefix: %d %s", op, data)
		}
		return event
	}
}
func (p *takeoverPeer) terminal(t *testing.T, want string, code int) []string {
	t.Helper()
	events := []string{}
	count := 0
	for {
		op, data, err := readFrame(p.reader)
		if err != nil {
			t.Fatal("等待终止消息", err, events)
		}
		if count > 0 && (op == 1 || op == 2) {
			t.Fatal("终止消息后又发送应用消息", op, string(data))
		}
		switch op {
		case 9:
			p.send(t, 10, data)
		case 1:
			var event map[string]json.RawMessage
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			var typ string
			json.Unmarshal(event["type"], &typ)
			events = append(events, typ)
			if typ == want {
				count++
				if want == "taken_over" && (len(event) != 2 || event["message"] == nil) {
					t.Fatal(string(data))
				}
			}
			if typ == "error" && want == "taken_over" {
				t.Fatal("接管被错误通知替代", string(data))
			}
		case 2:
			events = append(events, "binary")
		case 8:
			if len(data) < 2 || int(binary.BigEndian.Uint16(data)) != code || count != 1 {
				t.Fatalf("close %x terminal count=%d events=%v", data, count, events)
			}
			p.send(t, 8, data)
			return events
		}
	}
}
func s3Done(t *testing.T, c *Connection) {
	t.Helper()
	s3Wait(t, c.done)
	s3Wait(t, c.workersDone)
	s3Wait(t, c.supervisorDone)
	c.service.mu.Lock()
	_, exists := c.service.conns[c]
	c.service.mu.Unlock()
	if exists {
		t.Fatal("旧 slot 未归还")
	}
}
func s3Reason(c *Connection) string            { c.mu.Lock(); defer c.mu.Unlock(); return c.reason }
func s3Binding(c *Connection) *session.Binding { c.mu.Lock(); defer c.mu.Unlock(); return c.binding }

// Changed 是单消费者提示而非广播；观察者必须复查实际偏移，不能抢走
// Writer 的通知后假定下一次通知一定到来。ticker 只兜底复查已发生的状态。
func s3BufferAt(t *testing.T, h *harness, end uint64) {
	t.Helper()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		// 只探测目标最后一个字节，避免每轮复制整个多 MiB 缓存拖慢 race。
		data, err := h.session.Buffer().Read(end-1, 1)
		if (err == nil && len(data) == 1) || err == session.ErrGap {
			return
		}
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatalf("缓存偏移未达到 %d，探测结果 %v", end, err)
		}
	}
}

func s3Echo(t *testing.T, p *takeoverPeer, text string) {
	t.Helper()
	p.send(t, 2, []byte(text))
	received := []byte{}
	for !bytes.Contains(received, []byte(text)) {
		op, data, err := readFrame(p.reader)
		if err != nil {
			t.Fatal(err)
		}
		if op == 9 {
			p.send(t, 10, data)
		} else if op == 2 {
			received = append(received, data...)
		} else if op == 8 {
			t.Fatal("新端提前关闭")
		}
	}
}

func TestS3T1W02HundredTakeovers(t *testing.T) {
	h := s3Harness(t, nil)
	stopLists := make(chan struct{})
	listsDone := make(chan struct{})
	listErrors := make(chan string, 1)
	go func() {
		defer close(listsDone)
		for {
			select {
			case <-stopLists:
				return
			default:
			}
			items := h.manager.List("")
			if len(items) > 1 || (len(items) == 1 && items[0].Status == "CLOSED") {
				select {
				case listErrors <- "非法 List 快照":
				default:
				}
				return
			}
		}
	}()
	defer func() {
		close(stopLists)
		s3Wait(t, listsDone)
		select {
		case err := <-listErrors:
			t.Error(err)
		default:
		}
	}()
	a := s3Peer(t, h, nil)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	for round := 0; round < 100; round++ {
		old := s3Binding(a.server)
		b := s3Peer(t, h, nil)
		b.bind(t, h, "takeover", 0)
		prefix := b.prefix(t)
		if prefix.Generation != uint64(round+2) {
			t.Fatal(prefix)
		}
		events := a.terminal(t, "taken_over", 4001)
		s3Done(t, a.server)
		a.server.Terminate("NETWORK_ERROR")
		h.session.Detach(old)
		if s3Reason(a.server) != "taken_over" || h.service.Count() != 1 || h.session.BindingDisposition(s3Binding(b.server)) != session.BindingCurrent {
			t.Fatal("首因/slot/身份错误")
		}
		s3Echo(t, b, fmt.Sprintf("round-%03d-end", round))
		if round == 0 || round == 99 {
			t.Logf("round=%d old network=%v generation=%d slots=%d", round, events, prefix.Generation, h.service.Count())
		}
		a.raw.Close()
		a = b
	}
	if h.remote.Shells.Load() != 1 {
		t.Fatal("接管重建了 SSH")
	}
	a.raw.Close()
	s3Done(t, a.server)
	if h.session.View().Status != "DISCONNECTED" || h.session.View().Generation != 101 {
		t.Fatal(h.session.View())
	}
	h.session.Close("user_closed")
	s3Wait(t, h.session.Done())
	if len(h.manager.List("")) != 0 {
		t.Fatal("索引未回收")
	}
}

func TestS3T2RealNetworkFirstReason(t *testing.T) {
	for _, first := range []string{"network", "prepared_then_network", "committed_then_network", "takeover"} {
		t.Run(first, func(t *testing.T) {
			h := s3Harness(t, nil)
			a := s3Peer(t, h, nil)
			a.bind(t, h, "attach", 0)
			a.prefix(t)
			old := s3Binding(a.server)
			var b *takeoverPeer
			var prepared string
			if first == "prepared_then_network" {
				credential, err := h.manager.Prepare(h.login, h.session.View().ID, "takeover", 1)
				if err != nil {
					t.Fatal(err)
				}
				prepared = credential.Credential
			}
			registerGate, releaseRegister := s3Gate(t)
			writerGate, releaseWriter := s3Gate(t)
			if first == "committed_then_network" {
				paused := make(chan struct{}, 1)
				var once sync.Once
				a.server.mu.Lock()
				a.server.hooks = &connectionHooks{beforeAdmission: func() { once.Do(func() { paused <- struct{}{}; <-writerGate }) }}
				a.server.mu.Unlock()
				a.server.signal()
				s3Wait(t, paused)
				committed := make(chan struct{}, 1)
				b = s3Peer(t, h, &connectionHooks{beforeRegister: func(*session.CommitResult) { committed <- struct{}{}; <-registerGate }})
				b.bind(t, h, "takeover", 0)
				s3Wait(t, committed)
				if h.session.BindingDisposition(old) != session.BindingSuperseded {
					t.Fatal("未捕获提交后 old.end 前的窗口")
				}
			}
			if first != "takeover" {
				a.raw.(*net.TCPConn).SetLinger(0)
				a.raw.Close()
				s3Wait(t, a.server.terminated)
				if s3Reason(a.server) != "NETWORK_ERROR" {
					t.Fatal(s3Reason(a.server))
				}
			}
			if b == nil {
				b = s3Peer(t, h, nil)
				if prepared != "" {
					b.token(t, prepared, 0)
				} else {
					b.bind(t, h, "takeover", 0)
				}
			}
			releaseRegister()
			releaseWriter()
			b.prefix(t)
			if first == "takeover" {
				s3Wait(t, a.server.terminated)
				a.raw.(*net.TCPConn).SetLinger(0)
				a.raw.Close()
			}
			s3Done(t, a.server)
			a.server.Terminate("taken_over")
			a.server.Terminate("NETWORK_ERROR")
			h.session.Detach(old)
			want := "NETWORK_ERROR"
			if first == "takeover" {
				want = "taken_over"
			}
			if s3Reason(a.server) != want {
				t.Fatal(s3Reason(a.server))
			}
			s3Echo(t, b, "same-shell-after-fault")
			if h.remote.Shells.Load() != 1 {
				t.Fatal("SSH 重建")
			}
		})
	}
}

func TestS3T1ReaderDoneAndCommitHandoff(t *testing.T) {
	for round := 0; round < 100; round++ {
		t.Run(fmt.Sprintf("round-%03d", round), func(t *testing.T) {
			h := s3Harness(t, nil)
			executeGate, releaseExecute := s3Gate(t)
			writerGate, releaseWriter := s3Gate(t)
			registerGate, releaseRegister := s3Gate(t)
			executing := make(chan struct{}, 1)
			waiting := make(chan struct{}, 1)
			writerPaused := make(chan struct{}, 1)
			committed := make(chan *session.CommitResult, 1)
			var pauseWriter atomic.Bool
			var operations atomic.Int64
			var executeOnce sync.Once
			var writerOnce sync.Once
			a := s3Peer(t, h, &connectionHooks{
				beforeExecute: func() { executeOnce.Do(func() { executing <- struct{}{}; <-executeGate }) },
				waitingOperation: func() {
					if operations.Add(1) == 3 {
						waiting <- struct{}{}
					}
				},
				beforeAdmission: func() {
					if pauseWriter.Load() {
						writerOnce.Do(func() { writerPaused <- struct{}{}; <-writerGate })
					}
				},
			})
			a.bind(t, h, "attach", 0)
			a.prefix(t)
			old := s3Binding(a.server)
			a.send(t, 2, []byte("forbidden-queued-1"))
			s3Wait(t, executing)
			a.send(t, 2, []byte("forbidden-queued-2"))
			a.send(t, 1, []byte(`{"type":"resize","cols":100,"rows":40,"binding_generation":1}`))
			s3Wait(t, waiting)
			pauseWriter.Store(true)
			a.server.signal()
			s3Wait(t, writerPaused)
			b := s3Peer(t, h, &connectionHooks{beforeRegister: func(r *session.CommitResult) { committed <- r; <-registerGate }})
			b.bind(t, h, "takeover", 0)
			result := s3Wait(t, committed)
			select {
			case <-old.Done():
				t.Fatal("提交提前取消旧 context")
			default:
			}
			if h.session.BindingDisposition(old) != session.BindingSuperseded || h.service.Count() != 2 {
				t.Fatal("逻辑撤权/slot 错误")
			}
			// 由旧执行器失效分流先进入终止，old.end 随后执行；所有网络发送仍由原 Writer 完成。
			if round%2 == 0 {
				// 独立的迟到 Detach 取消已 superseded 对象；Connection context 尚未取消，
				// 执行器仍暂停且操作槽满，Reader 此刻只能通过 Binding.Done 退出。
				h.session.Detach(old)
				s3Wait(t, a.server.terminated)
				releaseExecute()
			} else {
				// 对照：先让执行器完成失效准入分流，Reader 的迟到 defer 不得覆盖。
				releaseExecute()
				s3Wait(t, a.server.terminated)
			}
			if s3Reason(a.server) != "taken_over" {
				t.Fatal(s3Reason(a.server))
			}
			if h.service.Count() != 2 {
				t.Fatal("Writer 未退出却提前归还 slot")
			}
			releaseWriter()
			a.terminal(t, "taken_over", 4001)
			s3Done(t, a.server)
			releaseRegister()
			b.prefix(t)
			result.FinishDisplaced()
			if h.session.BindingDisposition(result.NewBinding) != session.BindingCurrent {
				t.Fatal("旧回调改变新端")
			}
			s3Echo(t, b, "after-done-race")
			replay, _ := h.session.Buffer().Snapshot(0)
			if bytes.Contains(replay.Data, []byte("forbidden-queued")) || bytes.Count(replay.Data, []byte("after-done-race")) != 1 || h.session.View().Cols != 80 {
				t.Fatal("旧输入产生效果或新输入不是恰一次回显")
			}
			resizeCount := 0
			for len(h.remote.Requests) > 0 {
				if <-h.remote.Requests == "window-change" {
					resizeCount++
				}
			}
			if resizeCount != 0 || h.remote.Shells.Load() != 1 {
				t.Fatal("旧 resize 到达真实 SSH 或重建 Shell", resizeCount)
			}
		})
	}
}

func TestS3R06T4RegistrationWindow(t *testing.T) {
	for _, event := range []string{"close", "revoke", "takeover", "terminated"} {
		t.Run(event, func(t *testing.T) {
			h := s3Harness(t, nil)
			a := s3Peer(t, h, nil)
			a.bind(t, h, "attach", 0)
			a.prefix(t)
			old := s3Binding(a.server)
			gate, release := s3Gate(t)
			committed := make(chan *session.CommitResult, 1)
			b := s3Peer(t, h, &connectionHooks{beforeRegister: func(r *session.CommitResult) { committed <- r; <-gate }})
			b.bind(t, h, "takeover", 0)
			result := s3Wait(t, committed)
			var c *takeoverPeer
			switch event {
			case "close":
				h.session.Close("user_closed")
				s3Wait(t, h.session.Done())
			case "revoke":
				h.manager.Revoke(h.login.ID)
				h.service.Revoke(h.login.ID)
			case "terminated":
				b.server.Terminate("NETWORK_ERROR")
			case "takeover":
				c = s3Peer(t, h, nil)
				c.bind(t, h, "takeover", 0)
				c.prefix(t)
			}
			release()
			// 消费真实终止消息；不能用客户端先断开掩盖登记前的通知遗漏。
			if event == "revoke" {
				a.terminal(t, "error", 1008)
				b.terminal(t, "error", 1008)
			} else {
				a.terminal(t, "taken_over", 4001)
				switch event {
				case "close":
					b.terminal(t, "closed", 1000)
				case "takeover":
					b.terminal(t, "taken_over", 4001)
				case "terminated":
					// 独立首因已固定，不要求 NETWORK_ERROR 路径再发送通知。
					b.raw.Close()
				}
			}
			s3Done(t, a.server)
			s3Done(t, b.server)
			h.session.Detach(old)
			h.session.Detach(result.NewBinding)
			result.FinishDisplaced()
			if h.session.BindingDisposition(result.NewBinding) == session.BindingCurrent {
				t.Fatal("登记失败遗留有效权限")
			}
			if c != nil {
				if h.session.View().Generation != 3 {
					t.Fatal(h.session.View())
				}
				s3Echo(t, c, "third-binding-kept")
			} else if event == "close" {
				if h.session.View().Status != "CLOSED" {
					t.Fatal(h.session.View())
				}
			} else if h.session.View().Status != "DISCONNECTED" {
				t.Fatal(h.session.View())
			}
			if h.remote.Shells.Load() != 1 {
				t.Fatal("SSH 重建")
			}
		})
	}
}

func TestS3T4LateCallbacksAcrossGenerations(t *testing.T) {
	h := s3Harness(t, nil)
	a := s3Peer(t, h, nil)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	ab := s3Binding(a.server)
	b := s3Peer(t, h, nil)
	b.bind(t, h, "takeover", 0)
	b.prefix(t)
	bb := s3Binding(b.server)
	a.terminal(t, "taken_over", 4001)
	s3Done(t, a.server)
	late := func() {
		for _, p := range []*takeoverPeer{a, b} {
			p.server.Terminate("NETWORK_ERROR")
			p.server.NotifyStatus()
		}
		h.session.Detach(ab)
		h.session.Detach(bb)
	}
	h.session.Detach(ab)
	a.server.finishBindingExit(ab, "BIND_CONFLICT")
	a.server.NotifyStatus()
	if err := h.session.Execute(ab, []byte("forbidden"), 0, 0); err == nil {
		t.Fatal("旧输入获准")
	}
	b.raw.Close()
	s3Done(t, b.server)
	before := h.session.View()
	late()
	after := h.session.View()
	if before.Generation != after.Generation || *before.OfflineExpiresAt != *after.OfflineExpiresAt {
		t.Fatal("旧回调重置期限")
	}
	c := s3Peer(t, h, nil)
	c.bind(t, h, "attach", 0)
	c.prefix(t)
	late()
	if err := h.session.Execute(bb, nil, 99, 30); err == nil {
		t.Fatal("旧 resize 获准")
	}
	s3Echo(t, c, "third-generation-live")
	if h.session.View().Generation != 3 || h.session.View().Cols != 80 || h.service.Count() != 1 {
		t.Fatal(h.session.View(), h.service.Count())
	}
}

func TestS3T3AdmissionOrdering(t *testing.T) {
	for _, label := range []string{"prefix", "history", "live", "status"} {
		for _, order := range []string{"writer_first", "commit_first", "commit_first_connection_locked"} {
			t.Run(label+"/"+order, func(t *testing.T) {
				h := s3Harness(t, nil)
				s3Wait(t, h.remote.Ready)
				// 保证绑定前历史非空；读取真实 SSH Banner 到缓存的完成通知。
				for {
					r, _ := h.session.Buffer().Snapshot(0)
					if r.End > 0 {
						break
					}
					s3Wait(t, h.session.Buffer().Changed())
				}
				gate, release := s3Gate(t)
				paused := make(chan struct{}, 1)
				var once sync.Once
				var sequence atomic.Int64
				var admits atomic.Int64
				var armed atomic.Bool
				type admission struct {
					seq  int64
					kind string
				}
				records := make(chan admission, 64)
				var serverConnection *Connection
				hooks := &connectionHooks{
					admitted: func(kind string) { admits.Add(1); records <- admission{sequence.Add(1), kind} },
				}
				if order == "writer_first" {
					hooks.beforeWrite = func(kind string) {
						serverConnection.mu.Lock()
						serverConnection.mu.Unlock()
						h.session.WithSendAdmission(s3Binding(serverConnection), func() { _, _ = h.session.Buffer().Snapshot(0) })
						if kind == label {
							once.Do(func() { paused <- struct{}{}; <-gate })
						}
					}
				} else {
					hooks.beforeAdmission = func() {
						if armed.Load() {
							once.Do(func() { paused <- struct{}{}; <-gate })
						}
					}
					if label == "prefix" {
						armed.Store(true)
					} else {
						hooks.admitted = func(kind string) {
							admits.Add(1)
							records <- admission{sequence.Add(1), kind}
							if kind == "prefix" {
								armed.Store(true)
							}
						}
					}
				}
				if order == "commit_first_connection_locked" {
					hooks.beforeSession = hooks.beforeAdmission
					hooks.beforeAdmission = nil
				}
				a := s3Peer(t, h, nil)
				serverConnection = a.server
				a.server.mu.Lock()
				a.server.hooks = hooks
				a.server.mu.Unlock()
				a.bind(t, h, "attach", 0)
				if label != "prefix" {
					a.prefix(t)
				}
				if order != "writer_first" {
					s3Wait(t, paused)
				}
				if label == "status" {
					if order == "commit_first_connection_locked" {
						// Writer 正持 c.mu 的测试屏障内；channel 本身并发安全，不能
						// 在测试线程再取 c.mu 等它，制造与业务无关的自死锁。
						a.server.controls <- statusMessage{"status", "CONNECTED"}
					} else {
						a.server.NotifyStatus()
					}
				}
				if label == "live" {
					if _, err := h.session.Write([]byte("live-admission-block")); err != nil {
						t.Fatal(err)
					}
				}
				if order == "writer_first" {
					s3Wait(t, paused)
				}
				before := admits.Load()
				commits := make(chan int64, 1)
				b := s3Peer(t, h, &connectionHooks{beforeRegister: func(*session.CommitResult) { commits <- sequence.Add(1) }})
				b.bind(t, h, "takeover", 0)
				commitSeq := s3Wait(t, commits)
				b.prefix(t)
				if h.service.Count() != 2 {
					t.Fatal("旧 Writer 暂停时 slot 提前释放")
				}
				// 普通 FIFO 填满也不会阻止独立终止槽；不多调用一次 NotifyStatus 触发另一个终止。
				for len(a.server.controls) < cap(a.server.controls) {
					a.server.controls <- statusMessage{"status", "CONNECTED"}
				}
				release()
				network := a.terminal(t, "taken_over", 4001)
				s3Done(t, a.server)
				if admits.Load() != before {
					t.Fatal("commit 后产生普通准入", before, admits.Load())
				}
				close(records)
				events := []admission{}
				for record := range records {
					if record.seq >= commitSeq {
						t.Fatal("第三种交错", record, commitSeq)
					}
					events = append(events, record)
				}
				if order == "writer_first" && (len(events) == 0 || events[len(events)-1].kind != label) {
					t.Fatal("未捕获指定真实准入", events)
				}
				t.Logf("admission=%v commit_observed=%d network=%v admitted=%d", events, commitSeq, network, admits.Load())
				s3Echo(t, b, "new-writer-works")
			})
		}
	}
}

func TestS3T3SessionLockCompetition(t *testing.T) {
	h := s3Harness(t, nil)
	a := s3Peer(t, h, nil)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	for round := 0; round < 20; round++ {
		var seq atomic.Int64
		var admissionSeq atomic.Int64
		attempted := make(chan struct{}, 1)
		var once sync.Once
		a.server.mu.Lock()
		a.server.hooks = &connectionHooks{beforeAdmission: func() { once.Do(func() { attempted <- struct{}{} }) }, admitted: func(string) { admissionSeq.Store(seq.Add(1)) }}
		a.server.mu.Unlock()
		token, err := h.manager.Prepare(h.login, h.session.View().ID, "takeover", h.session.View().Generation)
		if err != nil {
			t.Fatal(err)
		}
		held := make(chan struct{})
		gate, release := s3Gate(t)
		unlocked := make(chan struct{})
		go func(old *session.Binding) {
			h.session.WithSendAdmission(old, func() { close(held); <-gate })
			close(unlocked)
		}(s3Binding(a.server))
		s3Wait(t, held)
		a.server.NotifyStatus()
		s3Wait(t, attempted)
		committed := make(chan int64, 1)
		b := s3Peer(t, h, &connectionHooks{beforeRegister: func(*session.CommitResult) { committed <- seq.Add(1) }})
		b.token(t, token.Credential, 0)
		// bindReceived 在真实 Reader 进入 Attach 之前关闭；旧 Writer 已尝试准入，
		// 两者都不能越过当前 Session 锁，放行后自由竞争同一个同步边界。
		s3Wait(t, b.server.bindReceived)
		release()
		s3Wait(t, unlocked)
		commitSeq := s3Wait(t, committed)
		b.prefix(t)
		a.terminal(t, "taken_over", 4001)
		s3Done(t, a.server)
		if n := admissionSeq.Load(); n >= commitSeq {
			t.Fatal("commit 后旧准入", n, commitSeq)
		}
		a.raw.Close()
		a = b
	}
}

func TestS3W01CandidateFailuresKeepOld(t *testing.T) {
	for _, failure := range []string{"no_connect", "timeout", "offset", "expired", "ownership", "invalid"} {
		t.Run(failure, func(t *testing.T) {
			h := s3Harness(t, func(c *config.Config) {
				c.Session.BindingCredentialTtl = 1
				if failure == "timeout" {
					c.Websocket.BindTimeout = 1
				}
			})
			a := s3Peer(t, h, nil)
			a.bind(t, h, "attach", 0)
			a.prefix(t)
			old := s3Binding(a.server)
			login := h.login
			if failure == "ownership" {
				login = &auth.Session{ID: strings.Repeat("b", 32), Expires: time.Now().Add(time.Hour)}
			}
			credential, err := h.manager.Prepare(login, h.session.View().ID, "takeover", 1)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "no_connect" {
				s3Echo(t, a, "prepare-only")
				return
			}
			b := s3Peer(t, h, nil)
			if failure != "timeout" {
				offset := uint64(0)
				token := credential.Credential
				if failure == "offset" {
					offset = session.MaxCounter
				}
				if failure == "invalid" {
					token = "invalid"
				}
				if failure == "expired" {
					deadline, err := time.Parse("2006-01-02T15:04:05.000Z", credential.ExpiresAt)
					if err != nil {
						t.Fatal(err)
					}
					timer := time.NewTimer(time.Until(deadline.Add(time.Millisecond)))
					<-timer.C
				}
				b.token(t, token, offset)
			}
			b.terminal(t, "error", 1008)
			s3Done(t, b.server)
			want := "INVALID_CREDENTIAL"
			if failure == "timeout" {
				want = "BIND_TIMEOUT"
			} else if failure == "offset" {
				want = "INVALID_OFFSET"
			} else if failure == "ownership" {
				want = "PERMISSION_DENIED"
			}
			if s3Reason(b.server) != want {
				t.Fatalf("候选失败原因=%s want=%s", s3Reason(b.server), want)
			}
			if h.session.BindingDisposition(old) != session.BindingCurrent || h.session.View().Generation != 1 {
				t.Fatal("候选失败撤销旧权限")
			}
			s3Echo(t, a, "old-remains-live")
		})
	}
}

func TestS3W03PrefixFailureNoRollback(t *testing.T) {
	h := s3Harness(t, nil)
	a := s3Peer(t, h, nil)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	gate, release := s3Gate(t)
	admitted := make(chan struct{}, 1)
	b := s3Peer(t, h, &connectionHooks{beforeWrite: func(label string) {
		if label == "prefix" {
			admitted <- struct{}{}
			<-gate
		}
	}})
	b.bind(t, h, "takeover", 0)
	s3Wait(t, admitted)
	a.terminal(t, "taken_over", 4001)
	s3Done(t, a.server)
	if h.session.View().Generation != 2 {
		t.Fatal("服务端未提交")
	}
	b.raw.(*net.TCPConn).SetLinger(0)
	b.raw.Close()
	s3Wait(t, b.server.terminated)
	release()
	s3Done(t, b.server)
	if h.session.View().Generation != 2 || h.session.View().Status != "DISCONNECTED" {
		t.Fatal("前缀失败回滚", h.session.View())
	}
	c := s3Peer(t, h, nil)
	c.bind(t, h, "attach", 0)
	c.prefix(t)
	s3Echo(t, c, "recover-known-generation")
}

func TestS3W04W05PhysicalReclamation(t *testing.T) {
	for _, mode := range []string{"partial_basic", "partial_16", "partial_64", "partial_mask", "close_stall", "ping"} {
		t.Run(mode, func(t *testing.T) {
			h := s3Harness(t, func(c *config.Config) { c.Websocket.MaxConnections = 2; c.Websocket.PongTimeout = 20 })
			a := s3Peer(t, h, nil)
			a.bind(t, h, "attach", 0)
			a.prefix(t)
			if strings.HasPrefix(mode, "partial") {
				for len(h.reads) > 0 {
					<-h.reads
				}
				partial := map[string][]byte{"partial_basic": {0x82}, "partial_16": {0x82, 0xfe, 0}, "partial_64": {0x82, 0xff, 0, 0}, "partial_mask": {0x82, 0x81, 1, 2}}[mode]
				a.raw.Write(partial)
				s3Wait(t, h.reads)
			}
			if mode == "ping" {
				a.server.mu.Lock()
				a.server.nextPing = time.Now()
				a.server.mu.Unlock()
				a.server.signal()
				for {
					op, _, err := readFrame(a.reader)
					if err != nil {
						t.Fatal(err)
					}
					if op == 9 {
						break
					}
				}
			}
			b := s3Peer(t, h, nil)
			b.bind(t, h, "takeover", 0)
			b.prefix(t)
			s3Wait(t, a.server.terminated)
			if h.service.Count() != 2 {
				t.Fatal("旧端未完成 Close 却释放 slot")
			}
			response, err := http.Get(h.http.URL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 429 {
				t.Fatal("第三连接未拒绝", response.StatusCode)
			}
			// 旧端不回 Close/Pong；Supervisor 必须在预算内关闭真正 TCP。
			s3Done(t, a.server)
			if h.service.Count() != 1 || s3Reason(a.server) != "taken_over" {
				t.Fatal("物理回收/原因错误")
			}
			a.server.Terminate("NETWORK_ERROR")
			if h.service.Count() != 1 {
				t.Fatal("重复归还 slot")
			}
			s3Echo(t, b, "new-before-old-close")
		})
	}
	t.Run("limit_one", func(t *testing.T) {
		h := s3Harness(t, func(c *config.Config) { c.Websocket.MaxConnections = 1 })
		a := s3Peer(t, h, nil)
		a.bind(t, h, "attach", 0)
		a.prefix(t)
		_, err := h.manager.Prepare(h.login, h.session.View().ID, "takeover", 1)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Get(h.http.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 429 {
			t.Fatal(response.StatusCode)
		}
		s3Echo(t, a, "capacity-keeps-old")
	})
}

func TestS3W04RealBlockedWriterTakeover(t *testing.T) {
	h := s3Harness(t, func(c *config.Config) {
		c.Session.OutputBufferSize = 8 << 20
		c.Session.HistoryLimit = 100000
		c.Websocket.WriteTimeout = 10
		c.Websocket.OutputChunkSize = 4 << 20
	})
	ch := s3Wait(t, h.remote.Ready)
	if _, err := ch.Write(bytes.Repeat([]byte("x"), 4<<20)); err != nil {
		t.Fatal(err)
	}
	s3BufferAt(t, h, 4<<20)
	writing := make(chan struct{}, 1)
	var once sync.Once
	a := s3Peer(t, h, &connectionHooks{beforeWrite: func(kind string) {
		if kind == "history" {
			once.Do(func() { writing <- struct{}{} })
		}
	}})
	a.server.raw.(*observedConn).Conn.(*net.TCPConn).SetWriteBuffer(1024)
	a.raw.(*net.TCPConn).SetReadBuffer(1024)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	s3Wait(t, writing)
	// 唯一历史块为 4 MiB，真实 TCP 两端缓冲仅数 KiB，客户端不再读取。
	// 不把 hook 当作堵写证据：最终必须观察 Write 错误、强关和 worker 完成。
	snapshot, _ := h.session.Buffer().Snapshot(0)
	b := s3Peer(t, h, nil)
	b.bind(t, h, "takeover", snapshot.End)
	b.prefix(t)
	s3Wait(t, a.server.terminated)
	if h.service.Count() != 2 {
		t.Fatal("堵写未结束时提前归还 slot")
	}
	s3Done(t, a.server)
	if a.server.writeFailures.Load() == 0 {
		t.Fatal("未观察真实堵写失败")
	}
	if h.session.View().Generation != 2 || h.session.View().Status != "CONNECTED" || h.remote.Shells.Load() != 1 {
		t.Fatal("旧堵写阻断新端")
	}
	s3Echo(t, b, "live-after-blocked-old")
}

func TestS3W06SmallBufferAndShutdown(t *testing.T) {
	h := s3Harness(t, func(c *config.Config) { c.Session.OutputBufferSize = 1024; c.Websocket.OutputChunkSize = 128 })
	a := s3Peer(t, h, nil)
	a.bind(t, h, "attach", 0)
	a.prefix(t)
	ch := s3Wait(t, h.remote.Ready)
	ch.Write(bytes.Repeat([]byte("z"), 4096))
	s3BufferAt(t, h, 4096)
	gate, release := s3Gate(t)
	pause := make(chan struct{}, 1)
	var once sync.Once
	b := s3Peer(t, h, &connectionHooks{beforeWrite: func(label string) {
		if label == "history" {
			once.Do(func() { pause <- struct{}{}; <-gate })
		}
	}})
	b.bind(t, h, "takeover", 0)
	prefix := b.prefix(t)
	if prefix.Start == 0 {
		t.Fatal("未截断")
	}
	s3Wait(t, pause)
	ch.Write(bytes.Repeat([]byte("y"), 4096))
	s3BufferAt(t, h, 8192)
	release()
	b.terminal(t, "error", 4002)
	s3Done(t, b.server)
	// A 可能已经 OUTPUT_GAP，也可能被 takeover；其独立首因不可强制覆盖。
	a.raw.Close()
	s3Done(t, a.server)
	if h.session.View().Status != "DISCONNECTED" {
		t.Fatal("缺口关闭 SSH")
	}
	c := s3Peer(t, h, nil)
	c.bind(t, h, "attach", 0)
	c.prefix(t)
	h.service.Stop(time.Now().Add(time.Second))
	h.manager.Stop()
	c.raw.Close()
	s3Done(t, c.server)
	s3Wait(t, h.manager.Done())
	if h.service.Count() != 0 {
		t.Fatal("退出残留 slot")
	}
}
