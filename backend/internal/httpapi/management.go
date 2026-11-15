package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/sshclient"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
)

type Management struct {
	Sessions  *session.Manager
	Websocket *browser.Service
	Auth      *auth.Service
	Servers   *servers.Service
	Config    config.Config
	Logger    *slog.Logger
}
type LoginInput struct {
	Password string `json:"password"`
}
type ConfirmInput struct {
	ProbeID string `json:"probe_id"`
}
type LoginView struct {
	ExpiresAt string `json:"expires_at"`
}
type Settings struct {
	CredentialTimeout     int64 `json:"credential_timeout"`
	MaxControlMessageSize int64 `json:"max_control_message_size"`
	MaxInputMessageSize   int64 `json:"max_input_message_size"`
	OutputChunkSize       int64 `json:"output_chunk_size"`
	BindTimeout           int64 `json:"bind_timeout"`
	OperationWaitTimeout  int64 `json:"operation_wait_timeout"`
	WriteTimeout          int64 `json:"write_timeout"`
	MaxCols               int64 `json:"max_cols"`
	MaxRows               int64 `json:"max_rows"`
	WriteQueueBytes       int64 `json:"write_queue_bytes"`
	WriteDrainTimeout     int64 `json:"write_drain_timeout"`
	ReconnectMaxAttempts  int64 `json:"reconnect_max_attempts"`
	ReconnectDelay        int64 `json:"reconnect_delay"`
	ReconnectStableTime   int64 `json:"reconnect_stable_time"`
}
type AuthView struct {
	ExpiresAt      string   `json:"expires_at"`
	ClientSettings Settings `json:"client_settings"`
}
type ServerList struct {
	Items []servers.View `json:"items"`
}

func (m *Management) settings() Settings {
	c := m.Config
	return Settings{c.HTTP.CredentialTimeout, c.Websocket.MaxControlMessageSize, c.Websocket.MaxInputMessageSize, c.Websocket.OutputChunkSize, c.Websocket.BindTimeout, c.Session.OperationWaitTimeout, c.Websocket.WriteTimeout, c.Terminal.MaxCols, c.Terminal.MaxRows, c.Terminal.WriteQueueBytes, c.Terminal.WriteDrainTimeout, c.Terminal.ReconnectMaxAttempts, c.Terminal.ReconnectDelay, c.Terminal.ReconnectStableTime}
}
func token(r *http.Request) string {
	cookie, err := r.Cookie("web_ssh_auth")
	if err != nil {
		return ""
	}
	return cookie.Value
}
func (m *Management) cookie(w http.ResponseWriter, value string, expires time.Time, remove bool) {
	age := int(time.Until(expires).Seconds())
	if age < 1 {
		age = 1
	}
	if remove {
		age = -1
		expires = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: "web_ssh_auth", Value: value, Path: "/", HttpOnly: true, Secure: m.Config.Auth.CookieSecure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: age})
}
func (m *Management) decode(w http.ResponseWriter, r *http.Request, dst any, empty bool) error {
	return DecodeJSON(w, r, dst, m.Config.Server.MaxRequestBodySize, empty)
}
func (m *Management) success(w http.ResponseWriter, status int, data any) {
	JSON(w, status, Response[any]{0, "success", data}, m.Logger)
}
func (m *Management) Handler(fallback http.Handler, ready func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	if m.Sessions != nil && m.Websocket != nil {
		m.sessionRoutes(mux)
	}
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		in := LoginInput{}
		if err := m.decode(w, r, &in, false); err != nil {
			WriteError(w, err, m.Logger)
			return
		}
		if len(in.Password) > 1024 {
			WriteError(w, fault.Error("INVALID_REQUEST"), m.Logger)
			return
		}
		value, session, retry, err := m.Auth.Login(r.Context(), in.Password)
		in.Password = ""
		if err != nil {
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
			}
			m.Logger.Info("管理员登录失败")
			WriteError(w, err, m.Logger)
			return
		}
		m.cookie(w, value, session.Expires, false)
		m.Logger.Info("管理员登录成功")
		m.success(w, 200, LoginView{servers.Timestamp(session.Expires)})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := m.decode(w, r, &Empty{}, true); err != nil {
			WriteError(w, err, m.Logger)
			return
		}
		if err := m.Auth.Logout(r.Context(), token(r)); err != nil {
			WriteError(w, err, m.Logger)
			return
		}
		m.cookie(w, "", time.Time{}, true)
		m.success(w, 200, Empty{})
	})
	mux.HandleFunc("GET /api/auth/me", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		m.success(w, 200, AuthView{servers.Timestamp(login.Expires), m.settings()})
		return nil
	}))
	mux.HandleFunc("GET /api/servers", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		query := r.URL.Query()
		for key, values := range query {
			if (key != "search" && key != "group_name") || len(values) != 1 {
				return fault.Error("INVALID_REQUEST")
			}
		}
		search := query.Get("search")
		if len(search) > 128 || len(query.Get("group_name")) > 128 {
			return fault.Error("INVALID_REQUEST")
		}
		var group *string
		if query.Has("group_name") {
			v := query.Get("group_name")
			group = &v
		}
		items, err := m.Servers.List(r.Context(), search, group)
		if err != nil {
			return err
		}
		m.success(w, 200, ServerList{items})
		return nil
	}))
	mux.HandleFunc("POST /api/servers", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		in := servers.Input{}
		if err := m.decode(w, r, &in, false); err != nil {
			return err
		}
		view, err := m.Servers.Create(r.Context(), login, in)
		if err != nil {
			return err
		}
		m.Logger.Info("服务器已创建", "server_id", view.ID)
		m.success(w, 201, view)
		return nil
	}))
	mux.HandleFunc("GET /api/servers/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		view, err := m.Servers.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		m.success(w, 200, view)
		return nil
	}))
	mux.HandleFunc("PUT /api/servers/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		in := servers.Input{}
		if err := m.decode(w, r, &in, false); err != nil {
			return err
		}
		view, err := m.Servers.Update(r.Context(), login, r.PathValue("id"), in)
		if err != nil {
			return err
		}
		m.Logger.Info("服务器已修改", "server_id", view.ID)
		m.success(w, 200, view)
		return nil
	}))
	mux.HandleFunc("DELETE /api/servers/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		if err := m.decode(w, r, &Empty{}, true); err != nil {
			return err
		}
		if err := m.Servers.Delete(r.Context(), login, r.PathValue("id")); err != nil {
			return err
		}
		m.Logger.Info("服务器已删除", "server_id", r.PathValue("id"))
		m.success(w, 200, Empty{})
		return nil
	}))
	mux.HandleFunc("POST /api/servers/{id}/test", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		if err := m.decode(w, r, &Empty{}, true); err != nil {
			return err
		}
		ctx, diagnostic := sshclient.WithDiagnostic(r.Context(), r.PathValue("id"))
		defer func() { m.Logger.Info("SSH 测试诊断", "diagnostic", diagnostic.Snapshot()) }()
		result, probe, err := m.Servers.Test(ctx, login, r.PathValue("id"), time.Duration(m.Config.Ssh.ConnectTimeout)*time.Second)
		if err != nil {
			code := "INTERNAL_ERROR"
			var business fault.Error
			if errors.As(err, &business) {
				code = string(business)
			}
			diagnostic.Complete(code)
			m.Logger.Info("SSH 测试失败", "server_id", r.PathValue("id"))
			return err
		}
		if probe != nil {
			diagnostic.Complete("HOST_KEY_CONFIRMATION_REQUIRED")
			m.Logger.Info("SSH Host Key 待确认", "server_id", r.PathValue("id"))
			JSON(w, 409, ErrorResponse[*servers.Confirmation]{"HOST_KEY_CONFIRMATION_REQUIRED", "请核对目标及指纹后确认信任", probe}, m.Logger)
			return nil
		}
		diagnostic.Complete("0")
		m.Logger.Info("SSH 测试成功", "server_id", r.PathValue("id"))
		m.success(w, 200, result)
		return nil
	}))
	mux.HandleFunc("POST /api/servers/{id}/host-key/confirm", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		in := ConfirmInput{}
		if err := m.decode(w, r, &in, false); err != nil {
			return err
		}
		if len(in.ProbeID) != 43 {
			return fault.Error("INVALID_PROBE")
		}
		view, err := m.Servers.Confirm(r.Context(), login, r.PathValue("id"), in.ProbeID)
		if err != nil {
			return err
		}
		m.success(w, 200, view)
		return nil
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if (r.Method != "GET" && r.Method != "HEAD") || strings.HasPrefix(r.URL.Path, "/ws/") {
			// Origin 仅来自固定配置，不受 Host 或代理转发首部影响。
			if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != m.Config.Server.PublicOrigin {
				WriteError(w, fault.Error("PERMISSION_DENIED"), m.Logger)
				return
			}
			if !ready() {
				WriteError(w, fault.Error("SERVER_SHUTTING_DOWN"), m.Logger)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (m *Management) protected(fn func(http.ResponseWriter, *http.Request, *auth.Session) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login, err := m.Auth.Lookup(token(r))
		if err != nil {
			WriteError(w, err, m.Logger)
			return
		}
		if id := r.PathValue("id"); id != "" && !idPattern.MatchString(id) {
			WriteError(w, fault.Error("INVALID_REQUEST"), m.Logger)
			return
		}
		if err := fn(w, r, login); err != nil {
			WriteError(w, err, m.Logger)
		}
	}
}
