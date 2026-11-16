package httpapi

import (
	"net/http"
	"net/url"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
)

type SessionList struct {
	Items []session.View `json:"items"`
}

type sessionInput struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}
type attachInput struct {
	Mode     string  `json:"mode"`
	Expected *uint64 `json:"expected_generation"`
}

func (m *Management) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return fault.Error("INVALID_REQUEST")
		}
		for key, values := range query {
			if key != "server_id" || len(values) != 1 || !idPattern.MatchString(values[0]) {
				return fault.Error("INVALID_REQUEST")
			}
		}
		m.success(w, http.StatusOK, SessionList{Items: m.Sessions.List(query.Get("server_id"))})
		return nil
	}))
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
