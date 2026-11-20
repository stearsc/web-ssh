package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

type envelope[T any] struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Data    T               `json:"data"`
}

func api(t *testing.T) (http.Handler, *Management, *bytes.Buffer) {
	t.Helper()
	db := testutil.DB(t)
	cfg := config.Defaults()
	cfg.Auth.LoginRateLimit = 100
	a, err := auth.New(context.Background(), db, cfg.Auth, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := servers.New(context.Background(), db, cipher, cfg, servers.WithPrivateKeyStore(privatekey.NewStore(db, dir, time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	a.OnRevoke(s.Revoke)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	m := &Management{Auth: a, Servers: s, Config: cfg, Logger: logger}
	return m.Handler(NewHandler(fstest.MapFS{"index.html": {Data: []byte("Web SSH")}}, func() bool { return true }, logger), func() bool { return true }), m, logs
}
func call(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("缺少 no-store")
	}
	return w
}
func loginCookie(t *testing.T, h http.Handler, origin string) *http.Cookie {
	t.Helper()
	w := call(t, h, "POST", "/api/auth/login", `{"password":"admin-secret"}`, nil, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	return cookies[0]
}
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	data := envelope[T]{}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return data.Data
}
func TestManagementEndToEnd(t *testing.T) {
	h, m, logs := api(t)
	origin := m.Config.Server.PublicOrigin
	cookie := loginCookie(t, h, origin)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge < 604790 {
		t.Fatal("Cookie 属性", cookie)
	}
	me := call(t, h, "GET", "/api/auth/me", "", cookie, "")
	if me.Code != 200 {
		t.Fatal(me.Code)
	}
	view := decode[AuthView](t, me)
	if view.ClientSettings.MaxCols != m.Config.Terminal.MaxCols {
		t.Fatal(view)
	}
	remote := testutil.StartSSH(t)
	port := remote.Port()
	in := servers.Input{Name: "test-server", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret", GroupName: "production"}
	body, _ := json.Marshal(in)
	created := call(t, h, "POST", "/api/servers", string(body), cookie, origin)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	v := decode[servers.View](t, created)
	path := "/api/servers/" + v.ID
	first := call(t, h, "POST", path+"/test", "{}", cookie, origin)
	if first.Code != 409 {
		t.Fatal(first.Code, first.Body.String())
	}
	p := decode[servers.Confirmation](t, first)
	if remote.AuthCalls.Load() != 0 {
		t.Fatal("探测发送密码")
	}
	forged := `{"probe_id":"` + p.ProbeID + `","public_key":"fake"}`
	if w := call(t, h, "POST", path+"/host-key/confirm", forged, cookie, origin); w.Code != 400 {
		t.Fatal(w.Code)
	}
	payload := `{"probe_id":"` + p.ProbeID + `"}`
	if w := call(t, h, "POST", path+"/host-key/confirm", payload, cookie, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(t, h, "POST", path+"/test", "{}", cookie, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	in.Password = ""
	in.Name = "renamed"
	body, _ = json.Marshal(in)
	if w := call(t, h, "PUT", path, string(body), cookie, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(t, h, "POST", path+"/test", "{}", cookie, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	listing := call(t, h, "GET", "/api/servers?search=renamed&group_name=production", "", cookie, "")
	if len(decode[ServerList](t, listing).Items) != 1 {
		t.Fatal(listing.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{created, listing, me} {
		for _, forbidden := range []string{"password_encrypted", "password_hash", "token_hash", cookie.Value, "ssh-secret", "config_version"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatal("响应泄漏", forbidden)
			}
		}
	}
	remote.Rotate(t)
	pending := decode[servers.Confirmation](t, call(t, h, "POST", path+"/test", "{}", cookie, origin))
	exit := call(t, h, "POST", "/api/auth/logout", "{}", cookie, origin)
	if exit.Code != 200 || exit.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(exit.Code)
	}
	if w := call(t, h, "GET", path, "", cookie, ""); w.Code != 401 {
		t.Fatal("退出后 Cookie 仍有效")
	}
	newCookie := loginCookie(t, h, origin)
	if w := call(t, h, "POST", path+"/host-key/confirm", `{"probe_id":"`+pending.ProbeID+`"}`, newCookie, origin); w.Code != 409 {
		t.Fatal("跨登录 probe 仍有效")
	}
	if w := call(t, h, "DELETE", path, "{}", newCookie, origin); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(t, h, "DELETE", path, "{}", newCookie, origin); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, forbidden := range []string{cookie.Value, p.ProbeID, "admin-secret", "ssh-secret"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatal("日志泄漏")
		}
	}
	remote.WaitIdle(t)
}
func TestOriginAuthenticationAndStrictRequests(t *testing.T) {
	h, m, _ := api(t)
	origin := m.Config.Server.PublicOrigin
	for _, bad := range []string{"", "https://evil.example", origin + "/"} {
		if w := call(t, h, "POST", "/api/auth/login", `{"password":"admin-secret"}`, nil, bad); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"/api/auth/me", "/api/servers", "/api/servers/" + strings.Repeat("a", 32)} {
		if w := call(t, h, "GET", path, "", nil, ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	cookie := loginCookie(t, h, origin)
	for _, body := range []string{`{"password":"x","password":"y"}`, `{"password":"x","extra":1}`, `{"password":null}`, `{"password":3}`, `{"password":"x"} {}`, `{}`} {
		if w := call(t, h, "POST", "/api/auth/login", body, nil, origin); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	oversized := `{"password":"` + strings.Repeat("x", int(m.Config.Server.MaxRequestBodySize)) + `"}`
	if w := call(t, h, "POST", "/api/auth/login", oversized, nil, origin); w.Code != 413 {
		t.Fatal(w.Code)
	}
	for _, query := range []string{"?bad=1", "?search=a&search=b"} {
		if w := call(t, h, "GET", "/api/servers"+query, "", cookie, ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/auth/logout", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-Forwarded-Host", "localhost:8080")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("代理头绕过 Origin")
	}
	if w := call(t, h, "POST", "/api/auth/logout", "{}", nil, origin); w.Code != 200 {
		t.Fatal("缺失 Cookie 退出不幂等")
	}
}

func TestSSHDiagnosticHTTPIsolationProtocolAndRedaction(t *testing.T) {
	h, m, logs := api(t)
	m.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	origin := m.Config.Server.PublicOrigin
	cookie := loginCookie(t, h, origin)
	type target struct {
		id, oldFingerprint, fingerprint string
		remote                          *testutil.SSH
	}
	targets := make([]target, 2)
	forbidden := []string{cookie.Value, "admin-secret", "ssh-secret"}
	for i := range targets {
		remote := testutil.StartSSH(t)
		port := remote.Port()
		payload, _ := json.Marshal(servers.Input{Name: "test", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
		created := call(t, h, "POST", "/api/servers", string(payload), cookie, origin)
		if created.Code != 201 {
			t.Fatal(created.Body.String())
		}
		view := decode[servers.View](t, created)
		path := "/api/servers/" + view.ID
		probe := decode[servers.Confirmation](t, call(t, h, "POST", path+"/test", "{}", cookie, origin))
		forbidden = append(forbidden, probe.ProbeID, base64.StdEncoding.EncodeToString(remote.PublicKey().Marshal()))
		w := call(t, h, "POST", path+"/host-key/confirm", `{"probe_id":"`+probe.ProbeID+`"}`, cookie, origin)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		targets[i] = target{view.ID, probe.Fingerprint, probe.Fingerprint, remote}
	}
	// 同时请求两个不同服务器：一个变化、一个仍可信，检查诊断互不污染。
	targets[0].remote.Rotate(t)
	targets[0].fingerprint = ssh.FingerprintSHA256(targets[0].remote.PublicKey())
	forbidden = append(forbidden, base64.StdEncoding.EncodeToString(targets[0].remote.PublicKey().Marshal()))
	logs.Reset()
	responses := make([]*httptest.ResponseRecorder, len(targets))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[i] = call(t, h, "POST", "/api/servers/"+target.id+"/test", "{}", cookie, origin)
		}()
	}
	close(start)
	wg.Wait()
	if responses[0].Code != 409 || responses[1].Code != 200 {
		t.Fatal(responses[0].Body.String(), responses[1].Body.String())
	}
	changed := envelope[servers.Confirmation]{}
	if err := json.Unmarshal(responses[0].Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if string(changed.Code) != `"HOST_KEY_CONFIRMATION_REQUIRED"` || changed.Data.Reason != "key_changed" || changed.Data.Fingerprint != targets[0].fingerprint || changed.Data.PreviousFingerprint == nil || *changed.Data.PreviousFingerprint != targets[0].oldFingerprint {
		t.Fatal(changed)
	}
	forbidden = append(forbidden, changed.Data.ProbeID)
	if targets[0].remote.AuthCalls.Load() != 0 || targets[1].remote.AuthCalls.Load() != 1 {
		t.Fatal("变化密钥继续认证，或可信服务器行为改变")
	}
	// 未确认前可信记录不能被覆盖；有效确认凭证必须绑定本次实际观察到的新密钥。
	path := "/api/servers/" + targets[0].id
	before := decode[servers.View](t, call(t, h, "GET", path, "", cookie, ""))
	if before.HostKey == nil || before.HostKey.Fingerprint != targets[0].oldFingerprint {
		t.Fatal("测试擅自更新信任", before)
	}
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	records := map[string]sshclient.DiagnosticRecord{}
	ids := map[string]bool{}
	for {
		var entry struct {
			Message    string                      `json:"msg"`
			Diagnostic *sshclient.DiagnosticRecord `json:"diagnostic"`
		}
		err := decoder.Decode(&entry)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if entry.Message != "SSH 测试诊断" {
			continue
		}
		if entry.Diagnostic == nil {
			t.Fatal("缺少结构化诊断")
		}
		r := *entry.Diagnostic
		if ids[r.AttemptID] || r.AttemptID == "" {
			t.Fatal("请求 ID 重复")
		}
		ids[r.AttemptID] = true
		if _, exists := records[r.ServerID]; exists {
			t.Fatal("重复诊断")
		}
		records[r.ServerID] = r
	}
	if len(records) != 2 {
		t.Fatal(records)
	}
	for i, target := range targets {
		r := records[target.id]
		if r.ObservedFingerprint != target.fingerprint || r.TrustedFingerprint != target.oldFingerprint || !r.TCPConnected {
			t.Fatal(r)
		}
		if i == 0 {
			if r.HostKeyDecision != "changed" || !r.HostKeyRejectionPreserved || r.PasswordCallbackEntered || r.PasswordProvided || r.AuthCallbackCount != 0 || r.ProbePublishResult != "published" || r.BusinessCode != "HOST_KEY_CONFIRMATION_REQUIRED" {
				t.Fatal(r)
			}
		} else {
			if r.HostKeyDecision != "match" || !r.PasswordProvided || r.ProbePublishResult != "not_attempted" || r.BusinessCode != "0" {
				t.Fatal(r)
			}
		}
	}
	w := call(t, h, "POST", path+"/host-key/confirm", `{"probe_id":"`+changed.Data.ProbeID+`"}`, cookie, origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after := decode[servers.View](t, call(t, h, "GET", path, "", cookie, ""))
	if after.HostKey == nil || after.HostKey.Fingerprint != targets[0].fingerprint {
		t.Fatal("确认凭证绑定了错误密钥")
	}
	for _, value := range forbidden {
		if strings.Contains(logs.String(), value) {
			t.Fatal("诊断日志泄露敏感数据")
		}
	}
}

func TestMeIndependentTimeoutSources(t *testing.T) {
	h, m, _ := api(t)
	m.Config.HTTP.CredentialTimeout = 2
	m.Config.Websocket.BindTimeout = 7
	cookie := loginCookie(t, h, m.Config.Server.PublicOrigin)
	w := call(t, h, "GET", "/api/auth/me", "", cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := decode[AuthView](t, w)
	if v.ClientSettings.CredentialTimeout != 2 || v.ClientSettings.BindTimeout != 7 {
		t.Fatal(v)
	}
}
