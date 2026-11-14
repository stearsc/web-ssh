package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
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
	s, err := servers.New(context.Background(), db, cipher, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.OnRevoke(s.Revoke)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	m := &Management{a, s, cfg, logger}
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
