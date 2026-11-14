package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func TestHealthAndRouting(t *testing.T) {
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>Web SSH</html>")}, "assets/app.js": &fstest.MapFile{Data: []byte("console.log('Web SSH')")}}
	ready := true
	h := NewHandler(assets, func() bool { return ready }, testLogger())
	for _, tc := range []struct {
		method, path string
		status       int
		content      string
	}{
		{"GET", "/api/health", 200, `"status":"ready"`}, {"GET", "/", 200, "<html>"}, {"GET", "/login", 200, "<html>"},
		{"GET", "/assets/app.js", 200, "console.log"}, {"GET", "/assets/missing.js", 404, "404"}, {"GET", "/missing", 404, "404"},
		{"GET", "/api/missing", 400, "INVALID_REQUEST"}, {"GET", "/ws/sessions/test", 400, "INVALID_REQUEST"}, {"POST", "/api/health", 400, "INVALID_REQUEST"},
	} {
		t.Run(tc.path+tc.method, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.content) {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("响应可被缓存")
			}
		})
	}
	ready = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/health", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"status":"not_ready"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestDecodeJSON(t *testing.T) {
	type request struct {
		Name     string `json:"name"`
		Count    int    `json:"count"`
		Enabled  bool   `json:"enabled"`
		Optional string `json:"optional,omitempty"`
	}
	good := `{"name":"hello","count":1,"enabled":true}`
	cases := []struct {
		name, body string
		limit      int64
		code       Code
	}{
		{"合法", good, 1024, ""}, {"未知", `{"name":"x","count":1,"enabled":true,"extra":0}`, 1024, InvalidRequest},
		{"重复", `{"name":"x","name":"y","count":1,"enabled":true}`, 1024, InvalidRequest},
		{"大小写", `{"Name":"x","count":1,"enabled":true}`, 1024, InvalidRequest},
		{"缺项", `{"name":"x"}`, 1024, InvalidRequest}, {"null", `{"name":null,"count":1,"enabled":true}`, 1024, InvalidRequest},
		{"类型", `{"name":"x","count":"1","enabled":true}`, 1024, InvalidRequest},
		{"浮点", `{"name":"x","count":1.0,"enabled":true}`, 1024, InvalidRequest},
		{"整数溢出", `{"name":"x","count":9223372036854775808,"enabled":true}`, 1024, InvalidRequest},
		{"尾随", good + " {}", 1024, InvalidRequest}, {"数组", "[]", 1024, InvalidRequest}, {"空", "", 1024, InvalidRequest},
		{"超限", good, 10, MessageTooLarge}, {"恰好上限", good, int64(len(good)), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst request
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			err := DecodeJSON(httptest.NewRecorder(), r, &dst, tc.limit, false)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var e *Error
				if !errors.As(err, &e) || e.Code != tc.code {
					t.Fatal(err)
				}
			}
		})
	}
	for _, body := range []string{"", "{}"} {
		r := httptest.NewRequest("DELETE", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if err := DecodeJSON(httptest.NewRecorder(), r, &Empty{}, 1024, true); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(good))
	if err := DecodeJSON(httptest.NewRecorder(), r, &request{}, 1024, false); err == nil {
		t.Fatal("非 JSON 类型未拒绝")
	}
}
func TestErrorConversionDoesNotExposeCause(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, errors.New("password=secret"), testLogger())
	if w.Code != 500 || bytes.Contains(w.Body.Bytes(), []byte("secret")) {
		t.Fatal(w.Body.String())
	}
	var body ErrorResponse[Empty]
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != InternalError {
		t.Fatal(body, err)
	}
	if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal("响应格式不符")
	}
	w = httptest.NewRecorder()
	JSON(w, http.StatusOK, make(chan int), testLogger())
	if w.Code != 500 {
		t.Fatal("编码错误未转换")
	}
}
