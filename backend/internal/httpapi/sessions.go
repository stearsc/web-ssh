package httpapi

import (
	"net/http"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
)

type sessionInput struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}
type attachInput struct {
	Mode     string  `json:"mode"`
	Expected *uint64 `json:"expected_generation"`
}

func (m *Management) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/servers/{id}/sessions", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		var in sessionInput
		if e := m.decode(w, r, &in, false); e != nil {
			return e
		}
		view, probe, e := m.Sessions.Create(login, r.PathValue("id"), in.Cols, in.Rows)
		if e != nil {
			return e
		}
		if probe != nil {
			JSON(w, 409, ErrorResponse[*server.Confirmation]{"HOST_KEY_CONFIRMATION_REQUIRED", "请核对目标及指纹后确认信任", probe}, m.Logger)
			return nil
		}
		m.success(w, 201, view)
		return nil
	}))
	mux.HandleFunc("GET /api/sessions/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		v, e := m.Sessions.Get(r.PathValue("id"))
		if e != nil {
			return e
		}
		m.success(w, 200, v)
		return nil
	}))
	mux.HandleFunc("DELETE /api/sessions/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		if e := m.decode(w, r, &Empty{}, true); e != nil {
			return e
		}
		m.Sessions.Close(r.PathValue("id"), "user_closed")
		m.success(w, 200, Empty{})
		return nil
	}))
	mux.HandleFunc("POST /api/sessions/{id}/takeover", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		var in attachInput
		if e := m.decode(w, r, &in, false); e != nil {
			return e
		}
		if in.Expected == nil {
			return fault.Error("INVALID_REQUEST")
		}
		v, e := m.Sessions.Prepare(login, r.PathValue("id"), in.Mode, *in.Expected)
		if e != nil {
			return e
		}
		m.success(w, 200, v)
		return nil
	}))
	mux.HandleFunc("GET /ws/sessions/{id}", m.protected(func(w http.ResponseWriter, r *http.Request, login *auth.Session) error {
		s, e := m.Sessions.Find(r.PathValue("id"))
		if e != nil {
			return e
		}
		return m.Websocket.Upgrade(w, r, login, s)
	}))
}
