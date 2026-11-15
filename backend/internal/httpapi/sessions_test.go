package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ws "github.com/coder/websocket"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
)

func TestSessionsHTTPWebsocketAndLogoutRace(t *testing.T) {
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{Banner: []byte("prompt> ")})
	db := testutil.DB(t)
	cfg := config.Defaults()
	cfg.Websocket.CloseTimeout = 1
	cfg.Auth.LoginRateLimit = 100
	cfg.Server.PublicOrigin = "http://localhost"
	admin, e := auth.New(t.Context(), db, cfg.Auth, "admin-secret")
	if e != nil {
		t.Fatal(e)
	}
	cipher, _ := secret.New(bytes.Repeat([]byte{4}, 32))
	servers, e := server.New(t.Context(), db, cipher, cfg)
	if e != nil {
		t.Fatal(e)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := session.New(context.Background(), db, servers, cfg, logger)
	sockets := browser.New(cfg, logger)
	admin.OnRevoke(func(id string) { servers.Revoke(id); manager.Revoke(id); sockets.Revoke(id) })
	management := &Management{Auth: admin, Servers: servers, Sessions: manager, Websocket: sockets, Config: cfg, Logger: logger}
	h := httptest.NewUnstartedServer(management.Handler(http.NotFoundHandler(), func() bool { return true }))
	h.Config.ConnContext = browser.ConnContext
	h.Start()
	t.Cleanup(func() {
		sockets.Stop(time.Now().Add(time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := sockets.Wait(ctx); e != nil {
			t.Error(e)
		}
		h.Close()
		manager.Stop()
		<-manager.Done()
	})
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path string, body any, cookie *http.Cookie) (int, json.RawMessage, *http.Response) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(data))
		r.Header.Set("Origin", cfg.Server.PublicOrigin)
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var result envelope[json.RawMessage]
		if e = json.NewDecoder(response.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
		return response.StatusCode, result.Data, response
	}
	login := func() *http.Cookie {
		status, _, r := request("POST", "/api/auth/login", map[string]any{"password": "admin-secret"}, nil)
		if status != 200 {
			t.Fatal(status)
		}
		return r.Cookies()[0]
	}
	cookie := login()
	port := remote.Port()
	status, data, _ := request("POST", "/api/servers", server.Input{Name: "terminal", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"}, cookie)
	if status != 201 {
		t.Fatal(status, string(data))
	}
	var sv server.View
	json.Unmarshal(data, &sv)
	path := "/api/servers/" + sv.ID
	status, data, _ = request("POST", path+"/sessions", map[string]int{"cols": 80, "rows": 24}, cookie)
	if status != 409 {
		t.Fatal(status)
	}
	var probe server.Confirmation
	json.Unmarshal(data, &probe)
	if status, _, _ = request("POST", path+"/host-key/confirm", map[string]string{"probe_id": probe.ProbeID}, cookie); status != 200 {
		t.Fatal(status)
	}
	status, data, _ = request("POST", path+"/sessions", map[string]int{"cols": 80, "rows": 24}, cookie)
	if status != 201 {
		t.Fatal(status, string(data))
	}
	var view session.View
	json.Unmarshal(data, &view)
	route := "/api/sessions/" + view.ID
	for _, invalid := range []map[string]any{{"mode": "attach"}, {"mode": "attach", "expected_generation": nil}, {"expected_generation": 0}} {
		if status, _, _ = request("POST", route+"/takeover", invalid, cookie); status != 400 {
			t.Fatal("必填字段未校验", invalid, status)
		}
	}

	if status, _, _ = request("POST", route+"/takeover", map[string]any{"mode": "takeover", "expected_generation": 0}, cookie); status != 400 {
		t.Fatal("开放 takeover", status)
	}
	for _, headers := range []http.Header{{"Origin": []string{cfg.Server.PublicOrigin}}, {"Origin": []string{"http://evil"}, "Cookie": []string{cookie.String()}}} {
		conn, response, e := ws.Dial(t.Context(), h.URL+"/ws/sessions/"+view.ID, &ws.DialOptions{HTTPHeader: headers})
		if conn != nil {
			conn.CloseNow()
		}
		if e == nil || (response.StatusCode != 401 && response.StatusCode != 403) {
			t.Fatal(response, e)
		}
	}
	for round := 0; round < 5; round++ {
		if round > 0 {
			cookie = login()
		}
		status, data, _ = request("GET", route, nil, cookie)
		if status != 200 {
			t.Fatal(status)
		}
		json.Unmarshal(data, &view)
		status, data, _ = request("POST", route+"/takeover", map[string]any{"mode": "attach", "expected_generation": view.Generation}, cookie)
		if status != 200 {
			t.Fatal(status, string(data))
		}
		var credential session.CredentialView
		json.Unmarshal(data, &credential)
		conn, _, e := ws.Dial(t.Context(), h.URL+"/ws/sessions/"+view.ID, &ws.DialOptions{HTTPHeader: http.Header{"Origin": []string{cfg.Server.PublicOrigin}, "Cookie": []string{cookie.String()}}})
		if e != nil {
			t.Fatal(e)
		}
		oldLogin, e := admin.Lookup(cookie.Value)
		if e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		sent := make(chan struct{})
		payload, _ := json.Marshal(map[string]any{"type": "bind", "credential": credential.Credential, "requested_start_offset": 0})
		go func() { defer close(sent); <-start; _ = conn.Write(t.Context(), ws.MessageText, payload) }()
		close(start)
		status, _, _ = request("POST", "/api/auth/logout", map[string]any{}, cookie)
		if status != 200 {
			t.Fatal(status)
		}
		<-sent
		conn.CloseNow()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		e = sockets.Wait(ctx)
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		current, e := manager.Get(view.ID)
		if e != nil || current.Status != "DISCONNECTED" {
			t.Fatal(current, e)
		}
		if _, e = manager.Prepare(oldLogin, view.ID, "attach", current.Generation); e != fault.Error("AUTH_REQUIRED") {
			t.Fatal("撤销后重新签发", e)
		}
	}
	cookie = login()
	if status, _, _ = request("DELETE", route, map[string]any{}, cookie); status != 200 {
		t.Fatal(status)
	}
	target, e := manager.Find(view.ID)
	if e == nil {
		<-target.Done()
	}
	status, _, _ = request("GET", route, nil, cookie)
	if status != 404 {
		t.Fatal(status)
	}
	if status, _, _ = request("DELETE", route, map[string]any{}, cookie); status != 200 {
		t.Fatal(status)
	}
	if remote.Shells.Load() != 1 {
		t.Fatal("重新绑定新建 Shell")
	}
}
