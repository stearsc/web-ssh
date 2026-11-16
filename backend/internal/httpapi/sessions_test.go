package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
	list := func(t *testing.T, path string, cookie *http.Cookie, wantStatus int, wantCode string) []session.View {
		t.Helper()
		// Read-only GET follows existing authentication without requiring Origin.
		w := call(t, h.Config.Handler, "GET", path, "", cookie, "")
		if w.Code != wantStatus || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("list response: %d %v %s", w.Code, w.Header(), w.Body.String())
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil || len(top) != 3 || top["message"] == nil || top["data"] == nil || string(top["code"]) != wantCode {
			t.Fatalf("list envelope: %s (%v)", w.Body.String(), err)
		}
		if wantStatus != http.StatusOK {
			if string(top["data"]) != "{}" {
				t.Fatalf("error data: %s", top["data"])
			}
			return nil
		}
		if string(top["message"]) != `"success"` {
			t.Fatalf("success message: %s", top["message"])
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(top["data"], &data); err != nil || len(data) != 1 || data["items"] == nil || string(data["items"]) == "null" {
			t.Fatalf("list data: %s (%v)", top["data"], err)
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(data["items"], &rows); err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields("id server_id name host port username status created_at disconnected_at last_attached_at offline_expires_at remaining_seconds binding_generation current_cols current_rows")
		for _, row := range rows {
			if len(row) != len(fields) {
				t.Fatalf("unexpected/sensitive fields: %v", row)
			}
			for _, field := range fields {
				if row[field] == nil {
					t.Fatalf("missing field %s", field)
				}
			}
		}
		var items []session.View
		if err := json.Unmarshal(data["items"], &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	t.Run("list empty", func(t *testing.T) {
		if items := list(t, "/api/sessions", cookie, 200, "0"); len(items) != 0 {
			t.Fatal(items)
		}
	})
	t.Run("list unauthorized", func(t *testing.T) {
		list(t, "/api/sessions", nil, 401, `"AUTH_REQUIRED"`)
		list(t, "/api/sessions", &http.Cookie{Name: "web_ssh_auth", Value: "invalid"}, 401, `"AUTH_REQUIRED"`)
		list(t, "/api/sessions?server_id=invalid", nil, 401, `"AUTH_REQUIRED"`)
		// This independent login has never been used by another request or Binding.
		value, expired, _, err := admin.Login(t.Context(), "admin-secret")
		if err != nil {
			t.Fatal(err)
		}
		expired.Expires = time.Now().Add(-time.Second)
		list(t, "/api/sessions", &http.Cookie{Name: "web_ssh_auth", Value: value}, 401, `"AUTH_REQUIRED"`)
	})
	t.Run("list strict query", func(t *testing.T) {
		for _, query := range []string{
			"server_id=", "server_id", "server_id=bad", "server_id=" + strings.Repeat("A", 32),
			"server_id=" + strings.Repeat("a", 31), "server_id=" + strings.Repeat("a", 33),
			"server_id=" + strings.Repeat("a", 32) + "&server_id=" + strings.Repeat("a", 32),
			"status=CONNECTED", "page=1", "limit=5", "sort=id", "server_id=%ZZ", "server_id=a;b",
		} {
			t.Run(query, func(t *testing.T) { list(t, "/api/sessions?"+query, cookie, 400, `"INVALID_REQUEST"`) })
		}
	})
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
	t.Run("list active and filtered", func(t *testing.T) {
		for _, path := range []string{"/api/sessions", "/api/sessions?server_id=" + sv.ID} {
			items := list(t, path, cookie, 200, "0")
			if len(items) != 1 || items[0].ID != view.ID || items[0].Status != "DISCONNECTED" || items[0].ServerID != sv.ID || items[0].Generation != 0 {
				t.Fatalf("list: %+v", items)
			}
			// Detail and list share the same safe DTO; remaining seconds is sampled independently.
			expected := view
			expected.RemainingSeconds = items[0].RemainingSeconds
			if !reflect.DeepEqual(items[0], expected) {
				t.Fatalf("list/detail mismatch: %+v / %+v", items[0], expected)
			}
		}
		if items := list(t, "/api/sessions?server_id="+strings.Repeat("f", 32), cookie, 200, "0"); len(items) != 0 {
			t.Fatal(items)
		}
		other := login()
		items := list(t, "/api/sessions", other, 200, "0")
		if len(items) != 1 || items[0].ID != view.ID {
			t.Fatal("independent admin login cannot list", items)
		}
		if status, _, _ := request("POST", "/api/auth/logout", map[string]any{}, other); status != 200 {
			t.Fatal(status)
		}
		list(t, "/api/sessions", other, 401, `"AUTH_REQUIRED"`)
	})
	route := "/api/sessions/" + view.ID
	for _, invalid := range []map[string]any{{"mode": "attach"}, {"mode": "attach", "expected_generation": nil}, {"expected_generation": 0}} {
		if status, _, _ = request("POST", route+"/takeover", invalid, cookie); status != 400 {
			t.Fatal("必填字段未校验", invalid, status)
		}
	}

	if status, data, _ = request("POST", route+"/takeover", map[string]any{"mode": "takeover", "expected_generation": 0}, cookie); status != 200 {
		t.Fatal("takeover 凭证签发失败", status)
	}
	var takeoverCredential session.CredentialView
	if err := json.Unmarshal(data, &takeoverCredential); err != nil || takeoverCredential.Credential == "" || takeoverCredential.ExpiresAt == "" {
		t.Fatal("凭证响应不完整", err)
	}
	current, err := manager.Get(view.ID)
	if err != nil || current.Status != "DISCONNECTED" || current.Generation != 0 || !reflect.DeepEqual(current.OfflineExpiresAt, view.OfflineExpiresAt) {
		t.Fatal("Prepare 改变 Session", current, err)
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
	t.Run("list excludes closed", func(t *testing.T) {
		if items := list(t, "/api/sessions", cookie, 200, "0"); len(items) != 0 {
			t.Fatal(items)
		}
		if items := list(t, "/api/sessions?server_id="+sv.ID, cookie, 200, "0"); len(items) != 0 {
			t.Fatal(items)
		}
	})
	if remote.Shells.Load() != 1 {
		t.Fatal("重新绑定新建 Shell")
	}
}
