package websocket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/coder/websocket"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

type harness struct {
	service *Service
	manager *session.Manager
	session *session.Session
	login   *auth.Session
	http    *httptest.Server
	reads   chan int
	remote  *testutil.TerminalSSH
}
type observedListener struct {
	net.Listener
	reads chan int
}

func (l observedListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return &observedConn{c, l.reads}, nil
}

type observedConn struct {
	net.Conn
	reads chan int
}

func (c *observedConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	if n > 0 {
		select {
		case c.reads <- n:
		default:
		}
	}
	return n, e
}
func newHarness(t *testing.T, configure func(*config.Config)) *harness {
	t.Helper()
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{Banner: []byte("hello\r\n")})
	db := testutil.DB(t)
	cipher, _ := secret.New(bytes.Repeat([]byte{9}, 32))
	cfg := config.Defaults()
	cfg.Websocket.PingInterval = 1
	cfg.Websocket.PongTimeout = 1
	cfg.Websocket.CloseTimeout = 1
	cfg.Websocket.BindTimeout = 1
	cfg.Websocket.MessageReadTimeout = 1
	cfg.Websocket.WriteTimeout = 1
	if configure != nil {
		configure(&cfg)
	}
	servers, e := server.New(t.Context(), db, cipher, cfg)
	if e != nil {
		t.Fatal(e)
	}
	login := &auth.Session{ID: strings.Repeat("a", 32), Expires: time.Now().Add(time.Hour)}
	port := remote.Port()
	sv, e := servers.Create(t.Context(), login, server.Input{Name: "test", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
	if e != nil {
		t.Fatal(e)
	}
	_, probe, e := servers.Test(t.Context(), login, sv.ID, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = servers.Confirm(t.Context(), login, sv.ID, probe.ProbeID); e != nil {
		t.Fatal(e)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := session.New(context.Background(), db, servers, cfg, logger)
	view, _, e := manager.Create(login, sv.ID, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	target, _ := manager.Find(view.ID)
	h := &harness{service: New(cfg, logger), manager: manager, session: target, login: login, reads: make(chan int, 128), remote: remote}
	h.http = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := h.service.Upgrade(w, r, login, target); e != nil {
			http.Error(w, e.Error(), 429)
		}
	}))
	h.http.Listener = observedListener{h.http.Listener, h.reads}
	h.http.Config.ConnContext = ConnContext
	h.http.Start()
	t.Cleanup(func() {
		h.service.Stop(time.Now().Add(time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := h.service.Wait(ctx); e != nil {
			t.Error(e)
		}
		h.http.Close()
		manager.Stop()
		select {
		case <-manager.Done():
		case <-ctx.Done():
			t.Error("Session 未回收")
		}
	})
	return h
}
func (h *harness) credential(t *testing.T) string {
	t.Helper()
	v, e := h.manager.Prepare(h.login, h.session.View().ID, "attach", h.session.View().Generation)
	if e != nil {
		t.Fatal(e)
	}
	return v.Credential
}
func (h *harness) current(t *testing.T) *Connection {
	t.Helper()
	h.service.mu.Lock()
	defer h.service.mu.Unlock()
	for c := range h.service.conns {
		return c
	}
	t.Fatal("没有登记连接")
	return nil
}
func frame(op byte, p []byte) []byte {
	data := []byte{0x80 | op}
	if len(p) < 126 {
		data = append(data, 0x80|byte(len(p)))
	} else {
		data = append(data, 0x80|126, byte(len(p)>>8), byte(len(p)))
	}
	mask := []byte{1, 2, 3, 4}
	data = append(data, mask...)
	for i, v := range p {
		data = append(data, v^mask[i%4])
	}
	return data
}
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, e := io.ReadFull(r, head); e != nil {
		return 0, nil, e
	}
	n := uint64(head[1] & 127)
	if n == 126 {
		b := make([]byte, 2)
		if _, e := io.ReadFull(r, b); e != nil {
			return 0, nil, e
		}
		n = uint64(binary.BigEndian.Uint16(b))
	} else if n == 127 {
		b := make([]byte, 8)
		if _, e := io.ReadFull(r, b); e != nil {
			return 0, nil, e
		}
		n = binary.BigEndian.Uint64(b)
	}
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("unexpected size")
	}
	p := make([]byte, int(n))
	_, e := io.ReadFull(r, p)
	return head[0] & 15, p, e
}
func (h *harness) raw(t *testing.T, bound bool) (net.Conn, *bufio.Reader) {
	t.Helper()
	token := ""
	if bound {
		token = h.credential(t)
	}
	c, e := net.Dial("tcp", strings.TrimPrefix(h.http.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	r := bufio.NewReader(c)
	response, e := http.ReadResponse(r, &http.Request{Method: "GET"})
	if e != nil || response.StatusCode != 101 {
		t.Fatal(response, e)
	}
	if bound {
		payload, _ := json.Marshal(map[string]any{"type": "bind", "credential": token, "requested_start_offset": 0})
		c.Write(frame(1, payload))
		op, p, e := readFrame(r)
		if e != nil || op != 1 || !bytes.Contains(p, []byte("bind_success")) {
			t.Fatal(op, string(p), e)
		}
	}
	return c, r
}
func awaitDone(t *testing.T, c *Connection, budget time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(budget):
		t.Fatal("WS 任务和名额未回收")
	}
	if c.service.Count() != 0 {
		t.Fatal("完成后名额未归还")
	}
	select {
	case <-c.supervisorDone:
	default:
		t.Fatal("监督器尚未退出")
	}
}
func TestPartialHeadersForceCloseBusinessSession(t *testing.T) {
	for _, partial := range [][]byte{{0x82}, {0x82, 0xfe, 0}, {0x82, 0xff, 0, 0}, {0x82, 0x81, 1, 2}} {
		t.Run(fmt.Sprintf("%x", partial), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, nil)
			raw, r := h.raw(t, true)
			c := h.current(t)
			for len(h.reads) > 0 {
				<-h.reads
			}
			raw.Write(partial)
			select {
			case <-h.reads:
			case <-time.After(time.Second):
				t.Fatal("未读取注入字节")
			}
			sawPing := false
			for !sawPing {
				op, _, e := readFrame(r)
				if e != nil {
					t.Fatal(e)
				}
				sawPing = op == 9
			}
			awaitDone(t, c, 4*time.Second)
			if h.session.View().Status != "DISCONNECTED" {
				t.Fatal(h.session.View())
			}
			if h.remote.Shells.Load() != 1 {
				t.Fatal("SSH 意外改变")
			}
		})
	}
}
func TestSlotsRemainUntilWorkersAndCancelPing(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Websocket.MaxConnections = 1; c.Websocket.PongTimeout = 10 })
	_, r := h.raw(t, true)
	c := h.current(t)
	for {
		op, _, e := readFrame(r)
		if e != nil {
			t.Fatal(e)
		}
		if op == 9 {
			break
		}
	}
	c.Terminate("INTERNAL_ERROR")
	if h.service.Count() != 1 {
		t.Fatal("提前释放")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	other, response, e := ws.Dial(ctx, h.http.URL, nil)
	if other != nil {
		other.CloseNow()
	}
	if e == nil || response.StatusCode != 429 {
		t.Fatal(response, e)
	}
	awaitDone(t, c, 3*time.Second)
	if _, e = h.manager.Prepare(h.login, h.session.View().ID, "attach", h.session.View().Generation); e != nil {
		t.Fatal("SSH 未保留", e)
	}
}
func TestBindTimeoutAndConcurrentTermination(t *testing.T) {
	for _, mode := range []string{"silent", "partial", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, nil)
			raw, _ := h.raw(t, false)
			c := h.current(t)
			if mode == "partial" {
				raw.Write([]byte{0x81})
			}
			if mode == "concurrent" {
				done := make(chan struct{}, 16)
				for i := 0; i < 16; i++ {
					go func() { c.Terminate("INTERNAL_ERROR"); done <- struct{}{} }()
				}
				for i := 0; i < 16; i++ {
					<-done
				}
			}
			awaitDone(t, c, 4*time.Second)
		})
	}
}
func TestIdleAndContinuousOutputHeartbeat(t *testing.T) {
	for _, flood := range []bool{false, true} {
		t.Run(fmt.Sprint(flood), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, nil)
			token := h.credential(t)
			client, _, e := ws.Dial(t.Context(), h.http.URL, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer client.CloseNow()
			data, _ := json.Marshal(map[string]any{"type": "bind", "credential": token, "requested_start_offset": 0})
			if e = client.Write(t.Context(), ws.MessageText, data); e != nil {
				t.Fatal(e)
			}
			_, p, e := client.Read(t.Context())
			if e != nil || !bytes.Contains(p, []byte("bind_success")) {
				t.Fatal(string(p), e)
			}
			c := h.current(t)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				for {
					if _, _, e := client.Read(context.Background()); e != nil {
						return
					}
				}
			}()
			floodDone := make(chan struct{})
			go func() {
				defer close(floodDone)
				if !flood {
					return
				}
				ch := <-h.remote.Ready
				for {
					select {
					case <-c.ctx.Done():
						return
					default:
					}
					if _, e := ch.Write(bytes.Repeat([]byte("x"), 1024)); e != nil {
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
			// 计时器只提供观察期限，不作为竞争顺序的同步方式。
			deadline := time.NewTimer(9 * time.Second)
			ticker := time.NewTicker(20 * time.Millisecond)
			for c.pings.Load() < 6 {
				select {
				case <-c.done:
					t.Fatal("正常连接被关闭", c.reason)
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("心跳被饿死", c.pings.Load())
				}
			}
			deadline.Stop()
			ticker.Stop()
			c.Terminate("INTERNAL_ERROR")
			client.CloseNow()
			awaitDone(t, c, 3*time.Second)
			<-readDone
			<-floodDone
		})
	}
}

// 在实际 Hijack 调用前暂停 Upgrade，用真实 socket 验证停止接入能覆盖预占对象。
type gatedHijacker struct {
	http.ResponseWriter
	entered chan struct{}
	proceed <-chan struct{}
}

func (w gatedHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.entered)
	<-w.proceed
	return w.ResponseWriter.(http.Hijacker).Hijack()
}
func TestShutdownDuringUpgradeAndFailedHandshake(t *testing.T) {
	h := newHarness(t, nil)
	response, e := http.Get(h.http.URL)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if h.service.Count() != 0 {
		t.Fatal("握手失败泄漏名额")
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	original := h.http.Config.Handler
	gated := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original.ServeHTTP(gatedHijacker{w, entered, proceed}, r)
	}))
	gated.Config.ConnContext = ConnContext
	gated.Start()
	defer gated.Close()
	// 先前 HTTP 请求已结束；新 HTTP 连接在下面测试期间独占该 Handler。
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, _, _ := ws.Dial(t.Context(), gated.URL, nil)
		if conn != nil {
			conn.CloseNow()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("未进入 Hijack")
	}
	if h.service.Count() != 1 {
		t.Fatal("Upgrade 未预占")
	}
	h.service.Stop(time.Now().Add(time.Second))
	close(proceed)
	<-done
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if e := h.service.Wait(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestApplicationDeadlineShortensExistingClose(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Websocket.CloseTimeout = 20 })
	_, r := h.raw(t, true)
	c := h.current(t)
	c.Terminate("INTERNAL_ERROR")
	for {
		op, _, e := readFrame(r)
		if e != nil {
			t.Fatal(e)
		}
		if op == 8 {
			break
		}
	}
	h.service.Stop(time.Now().Add(80 * time.Millisecond))
	awaitDone(t, c, time.Second)
}
func TestPongCannotBypassPartialHeader(t *testing.T) {
	h := newHarness(t, nil)
	raw, r := h.raw(t, true)
	c := h.current(t)
	raw.Write([]byte{0x82, 0xff, 0})
	for {
		op, p, e := readFrame(r)
		if e != nil {
			t.Fatal(e)
		}
		if op == 9 {
			raw.Write(frame(10, p))
			break
		}
	}
	awaitDone(t, c, 4*time.Second)
	if c.pings.Load() != 0 {
		t.Fatal("非法拼接伪造成功 Pong")
	}
}

func TestBlockedRealTCPWriterAndBoundFailureRelease(t *testing.T) {
	t.Run("writer", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Session.OutputBufferSize = 8 << 20; c.Session.HistoryLimit = 100000 })
		_, _ = h.raw(t, true)
		c := h.current(t)
		underlying := c.raw.(*observedConn).Conn.(*net.TCPConn)
		if e := underlying.SetWriteBuffer(1024); e != nil {
			t.Fatal(e)
		}
		ch := <-h.remote.Ready
		flood := make(chan struct{})
		go func() {
			defer close(flood)
			data := bytes.Repeat([]byte("x"), 64<<10)
			for i := 0; i < 64; i++ {
				if _, e := ch.Write(data); e != nil {
					return
				}
			}
		}()
		awaitDone(t, c, 5*time.Second)
		<-flood
		if c.writeFailures.Load() == 0 || (c.reason != "SLOW_CLIENT" && c.reason != "NETWORK_ERROR") {
			t.Fatal("未验证真实写超时", c.reason)
		}
		if h.session.View().Status != "DISCONNECTED" {
			t.Fatal("WS 慢写关闭 SSH")
		}
		snapshot, e := h.session.Buffer().Snapshot(0)
		if e != nil || snapshot.End < 4<<20 {
			t.Fatal("SSH 输出未继续缓存", snapshot.End, e)
		}
	})
	t.Run("invalid-bind", func(t *testing.T) {
		h := newHarness(t, nil)
		raw, r := h.raw(t, false)
		c := h.current(t)
		raw.Write(frame(1, []byte(`{"type":"bind","credential":"invalid","requested_start_offset":0}`)))
		for {
			op, p, e := readFrame(r)
			if e != nil {
				break
			}
			if op == 1 && !bytes.Contains(p, []byte("INVALID_CREDENTIAL")) {
				t.Fatal(string(p))
			}
			if op == 8 {
				raw.Write(frame(8, p))
				break
			}
		}
		awaitDone(t, c, 3*time.Second)
		if h.session.View().Generation != 0 {
			t.Fatal("非法绑定改变代次")
		}
	})
}
func TestNormalSplitHeaderAndRepeatClose(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 5; i++ {
		raw, r := h.raw(t, true)
		c := h.current(t)
		marker := fmt.Sprintf("split-input-%d", i)
		payload := frame(2, []byte(marker))
		raw.Write(payload[:1])
		for len(h.reads) > 0 {
			<-h.reads
		}
		raw.Write(payload[1:])
		found := false
		for !found {
			op, p, e := readFrame(r)
			if e != nil {
				t.Fatal(e)
			}
			if op == 2 && bytes.Contains(p, []byte(marker)) {
				found = true
			}
			if op == 9 {
				raw.Write(frame(10, p))
			}
		}
		raw.Write(frame(8, []byte{3, 232}))
		awaitDone(t, c, 3*time.Second)
		raw.Close()
	}
	if h.remote.Shells.Load() != 1 {
		t.Fatal("重复绑定重建 SSH")
	}
}

func TestLargePasteOrderedAndMessageLimits(t *testing.T) {
	t.Run("paste", func(t *testing.T) {
		h := newHarness(t, nil)
		token := h.credential(t)
		client, _, e := ws.Dial(t.Context(), h.http.URL, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer client.CloseNow()
		payload, _ := json.Marshal(map[string]any{"type": "bind", "credential": token, "requested_start_offset": 0})
		if e = client.Write(t.Context(), ws.MessageText, payload); e != nil {
			t.Fatal(e)
		}
		_, _, e = client.Read(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		wanted := make([]byte, 0, 65536)
		for i := 0; i < 64; i++ {
			wanted = append(wanted, bytes.Repeat([]byte{byte('A' + i%26)}, 1024)...)
		}
		received := make(chan []byte, 1)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		go func() {
			var all []byte
			for {
				kind, p, e := client.Read(ctx)
				if e != nil {
					received <- all
					return
				}
				if kind == ws.MessageBinary {
					all = append(all, p...)
					if bytes.Contains(all, wanted) {
						received <- all
						return
					}
				} else {
					received <- all
					return
				}
			}
		}()
		for i := 0; i < len(wanted); i += 1024 {
			if e = client.Write(ctx, ws.MessageBinary, wanted[i:i+1024]); e != nil {
				t.Fatal(e)
			}
		}
		if all := <-received; !bytes.Contains(all, wanted) {
			t.Fatal("粘贴乱序或正常分块被拒绝", len(all))
		}
		c := h.current(t)
		client.CloseNow()
		awaitDone(t, c, 3*time.Second)
	})
	for _, payload := range []string{`{"type":"resize","cols":80,"cols":90,"rows":24,"binding_generation":1}`, `{"Type":"resize","cols":80,"rows":24,"binding_generation":1}`, `{"type":"resize","cols":0,"rows":24,"binding_generation":1}`} {
		t.Run("invalid-message", func(t *testing.T) {
			h := newHarness(t, nil)
			raw, r := h.raw(t, true)
			c := h.current(t)
			raw.Write(frame(1, []byte(payload)))
			for {
				op, p, e := readFrame(r)
				if e != nil {
					break
				}
				if op == 8 {
					raw.Write(frame(8, p))
					break
				}
			}
			awaitDone(t, c, 3*time.Second)
			if h.session.View().Status != "DISCONNECTED" {
				t.Fatal("协议错误关闭 SSH")
			}
		})
	}
	t.Run("incomplete-body", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Websocket.PingInterval = 10 })
		raw, _ := h.raw(t, true)
		c := h.current(t)
		full := frame(2, []byte("incomplete"))
		raw.Write(full[:len(full)-4])
		awaitDone(t, c, 3*time.Second)
	})
}

func TestControlQueueFullAndOutputGap(t *testing.T) {
	t.Run("control-queue", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Websocket.ControlQueueLimit = 2; c.Websocket.PongTimeout = 10 })
		_, r := h.raw(t, true)
		c := h.current(t)
		for {
			op, _, e := readFrame(r)
			if e != nil {
				t.Fatal(e)
			}
			if op == 9 {
				break
			}
		}
		c.NotifyStatus()
		c.NotifyStatus()
		c.NotifyStatus()
		awaitDone(t, c, 3*time.Second)
		if c.reason != "SLOW_CLIENT" {
			t.Fatal(c.reason)
		}
	})
	t.Run("output-gap", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Session.OutputBufferSize = 1024; c.Websocket.OutputChunkSize = 256 })
		raw, r := h.raw(t, true)
		c := h.current(t)
		ch := <-h.remote.Ready
		c.mu.Lock() // 只暂停 WS 调度，真实 SSH 输出读取必须继续。
		produced := make(chan struct{})
		go func() { defer close(produced); _, _ = ch.Write(bytes.Repeat([]byte("z"), 4096)) }()
		<-produced
		deadline := time.NewTimer(time.Second)
		poll := time.NewTicker(5 * time.Millisecond)
		defer poll.Stop()
		for {
			snapshot, e := h.session.Buffer().Snapshot(0)
			if e != nil {
				c.mu.Unlock()
				t.Fatal(e)
			}
			if snapshot.End >= 4096 {
				break
			}
			select {
			case <-poll.C:
			case <-deadline.C:
				c.mu.Unlock()
				t.Fatal("SSH 读取被 WS 调度阻塞")
			}
		}
		deadline.Stop()
		c.mu.Unlock()
		gap := false
		for {
			op, p, e := readFrame(r)
			if e != nil {
				break
			}
			if op == 1 {
				if bytes.Contains(p, []byte("history_truncated")) {
					t.Fatal("同一连接出现新快照")
				}
				if bytes.Contains(p, []byte("OUTPUT_GAP")) {
					gap = true
				}
			}
			if op == 8 {
				raw.Write(frame(8, p))
				break
			}
		}
		awaitDone(t, c, 3*time.Second)
		if !gap || h.session.View().Status != "DISCONNECTED" {
			t.Fatal("缺口未按协议终止", gap, h.session.View())
		}
	})
}

func TestLoginExpiryAndInputLimit(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("login-expiry-%v", bound), func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Websocket.BindTimeout = 5 })
			h.login.Expires = time.Now().Add(300 * time.Millisecond)
			raw, r := h.raw(t, bound)
			c := h.current(t)
			for {
				op, p, e := readFrame(r)
				if e != nil {
					break
				}
				if op == 8 {
					raw.Write(frame(8, p))
					break
				}
			}
			awaitDone(t, c, 3*time.Second)
			if c.reason != "AUTH_REQUIRED" || h.session.View().Status != "DISCONNECTED" {
				t.Fatal(c.reason, h.session.View())
			}
		})
	}
	t.Run("oversized-binary", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Websocket.MaxInputMessageSize = 64 })
		raw, r := h.raw(t, true)
		c := h.current(t)
		raw.Write(frame(2, bytes.Repeat([]byte("x"), 65)))
		for {
			op, p, e := readFrame(r)
			if e != nil {
				break
			}
			if op == 8 {
				raw.Write(frame(8, p))
				break
			}
		}
		awaitDone(t, c, 3*time.Second)
		if c.reason != "MESSAGE_TOO_LARGE" || h.session.View().Status != "DISCONNECTED" {
			t.Fatal(c.reason, h.session.View())
		}
		snapshot, _ := h.session.Buffer().Snapshot(0)
		if bytes.Contains(snapshot.Data, bytes.Repeat([]byte("x"), 65)) {
			t.Fatal("超限输入进入 SSH")
		}
	})
}
