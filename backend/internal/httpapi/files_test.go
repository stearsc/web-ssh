package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
	"golang.org/x/crypto/ssh"
)

func fileAPI(t *testing.T, opened ...chan struct{}) (http.Handler, *Management, *http.Cookie, string, string) {
	t.Helper()
	_, m, _ := api(t)
	m.Config.Server.PublicOrigin = "http://localhost"
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, _ <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for r := range requests {
			if r.Type != "subsystem" {
				_ = r.Reply(false, nil)
				continue
			}
			_ = r.Reply(true, nil)
			if len(opened) > 0 {
				select {
				case opened[0] <- struct{}{}:
				default:
				}
			}
			srv, err := sftp.NewServer(ch)
			if err != nil {
				t.Error(err)
				return
			}
			_ = srv.Serve()
			_ = srv.Close()
			return
		}
	}})
	// 与服务端真实入口相同，服务器和 Session 元数据共用数据库。
	db := testutil.DB(t)
	cipher, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	var setupErr error
	m.Servers, setupErr = server.New(t.Context(), db, cipher, m.Config)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	m.Sessions = session.New(context.Background(), db, m.Servers, m.Config, m.Logger)
	t.Cleanup(func() { m.Sessions.Stop(); <-m.Sessions.Done() })
	m.Websocket = browser.New(m.Config, m.Logger)
	h := m.Handler(http.NotFoundHandler(), func() bool { return true })
	cookie := loginCookie(t, h, m.Config.Server.PublicOrigin)
	login, err := m.Auth.Lookup(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	port := remote.Port()
	v, err := m.Servers.Create(t.Context(), login, server.Input{Name: "upload", Host: "127.0.0.1", Port: &port, Username: "tester", Password: "ssh-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, probe, err := m.Servers.Test(t.Context(), login, v.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Servers.Confirm(t.Context(), login, v.ID, probe.ProbeID); err != nil {
		t.Fatal(err)
	}
	view, _, err := m.Sessions.Create(login, v.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h, m, cookie, view.ID, directory
}
func multipartUpload(t *testing.T, directory, name, text string, overwrite bool) (*bytes.Buffer, string) {
	t.Helper()
	body := new(bytes.Buffer)
	w := multipart.NewWriter(body)
	if err := w.WriteField("directory", directory); err != nil {
		t.Fatal(err)
	}
	value := "false"
	if overwrite {
		value = "true"
	}
	if err := w.WriteField("overwrite", value); err != nil {
		t.Fatal(err)
	}
	file, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(file, text); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}
func TestFilesHTTPAuthenticationStreamingAndCommit(t *testing.T) {
	h, m, cookie, id, directory := fileAPI(t)
	endpoint := "/api/sessions/" + id + "/files"
	send := func(cookie *http.Cookie, name, text string, overwrite bool) *httptest.ResponseRecorder {
		body, content := multipartUpload(t, directory, name, text, overwrite)
		r := httptest.NewRequest("POST", endpoint, body)
		r.Header.Set("Content-Type", content)
		r.Header.Set("Origin", m.Config.Server.PublicOrigin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := send(nil, "f", "secret", false); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send(cookie, "f", "hello", false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(directory, "f"))
	if err != nil || string(got) != "hello" {
		t.Fatal(string(got), err)
	}
	input, _ := json.Marshal(map[string]string{"directory": directory, "filename": "f"})
	if w := call(t, h, "POST", endpoint+"/check", string(input), cookie, m.Config.Server.PublicOrigin); w.Code != 200 || !strings.Contains(w.Body.String(), `"exists":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send(cookie, "f", "wrong", false); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send(cookie, "f", "changed", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, _ = os.ReadFile(filepath.Join(directory, "f"))
	if string(got) != "changed" {
		t.Fatal(string(got))
	}
	if w := send(cookie, "../escape", "no", false); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	m.Sessions.Close(id, "user_closed")
	if w := send(cookie, "next", "no", false); w.Code != 503 && w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestMultipartRejectsMissingDuplicateExtraAndRawFilename(t *testing.T) {
	for _, scenario := range []string{"file-first", "duplicate", "extra-after-file", "encoded", "raw-path"} {
		t.Run(scenario, func(t *testing.T) {
			body := new(bytes.Buffer)
			w := multipart.NewWriter(body)
			if scenario != "file-first" {
				_ = w.WriteField("directory", "/remote")
			}
			if scenario == "duplicate" {
				_ = w.WriteField("directory", "/another")
			}
			if scenario == "encoded" {
				_, _ = w.CreatePart(map[string][]string{"Content-Disposition": {`form-data; name="file"; filename="f"`}, "Content-Transfer-Encoding": {"base64"}})
			} else {
				file, _ := w.CreateFormFile("file", map[bool]string{true: "../escape", false: "f"}[scenario == "raw-path"])
				_, _ = io.WriteString(file, "text")
			}
			if scenario == "extra-after-file" {
				_ = w.WriteField("extra", "x")
			}
			_ = w.Close()
			reader := multipart.NewReader(body, w.Boundary())
			_, name, _, part, err := uploadParts(reader)
			if scenario == "extra-after-file" {
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, part)
				if _, err = nextUploadPart(reader); err != nil {
					t.Fatal(err)
				}
				return
			}
			if scenario == "raw-path" {
				if err != nil || name != "../escape" {
					t.Fatal(name, err)
				}
				return
			}
			if err == nil {
				t.Fatal("接受非法 multipart")
			}
		})
	}
}
func TestUploadRouteDeadlineExceptionIsExact(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, item := range []struct {
		method, path string
		want         bool
	}{{"POST", "/api/sessions/" + id + "/files", true}, {"GET", "/api/sessions/" + id + "/files", false}, {"POST", "/api/sessions/" + id + "/files/check", false}, {"POST", "/api/sessions/bad/files", false}, {"POST", "/api/sessions/" + id + "/files/", false}} {
		r := httptest.NewRequest(item.method, item.path, nil)
		if IsFileUploadRequest(r) != item.want {
			t.Fatal(item)
		}
	}
}

func TestFilesRealHTTPRequestCancelAndIdleDeadline(t *testing.T) {
	opened := make(chan struct{}, 8)
	h, m, cookie, id, directory := fileAPI(t, opened)
	m.Config.Server.RequestTimeout = 2
	// 真实 TCP 的初始总读/写期限为 1 秒，上传 Handler 必须用可滚动读取期限替代。
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadTimeout = time.Second
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	defer srv.Close()
	endpoint := srv.URL + "/api/sessions/" + id + "/files"
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{true: "request-cancel", false: "progress-beyond-original-deadline"}[cancelRequest], func(t *testing.T) {
			pipe, writer := io.Pipe()
			multipartWriter := multipart.NewWriter(writer)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "POST", endpoint, pipe)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(cookie)
			req.Header.Set("Origin", m.Config.Server.PublicOrigin)
			req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
			name := "slow"
			if cancelRequest {
				name = "cancel"
			}
			producer := make(chan error, 1)
			go func() {
				if err := multipartWriter.WriteField("directory", directory); err != nil {
					producer <- err
					return
				}
				part, err := multipartWriter.CreateFormFile("file", name)
				if err != nil {
					producer <- err
					return
				}
				// 实际上传进展跨越原始 1 秒期限；计时仅用于模拟网络速率，不推断回收完成。
				ticker := time.NewTicker(200 * time.Millisecond)
				defer ticker.Stop()
				for range 8 {
					if _, err = part.Write(bytes.Repeat([]byte("x"), 8192)); err != nil {
						producer <- err
						return
					}
					select {
					case <-ticker.C:
					case <-ctx.Done():
						producer <- ctx.Err()
						return
					}
				}
				err = multipartWriter.Close()
				_ = writer.Close()
				producer <- err
			}()
			responseDone := make(chan error, 1)
			go func() {
				response, err := srv.Client().Do(req)
				if err == nil {
					defer response.Body.Close()
					data, e := io.ReadAll(response.Body)
					err = e
					if response.StatusCode != 200 {
						err = fmt.Errorf("%d %s", response.StatusCode, data)
					}
				}
				responseDone <- err
			}()
			select {
			case <-opened:
			case <-time.After(3 * time.Second):
				t.Fatal("未建立 SFTP channel")
			}
			if cancelRequest {
				cancel()
				_ = writer.CloseWithError(context.Canceled)
			}
			select {
			case err = <-responseDone:
			case <-time.After(6 * time.Second):
				t.Fatal("Handler/请求未结束")
			}
			if !cancelRequest && err != nil {
				t.Fatal(err)
			}
			<-producer
			s, err := m.Sessions.Find(id)
			if err != nil {
				t.Fatal(err)
			}
			wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			if err = s.WaitFileOperation(wait); err != nil {
				t.Fatal(err)
			}
			matches, _ := filepath.Glob(filepath.Join(directory, ".web-ssh-upload-*"))
			if len(matches) != 0 {
				t.Fatal("取消后临时文件残留", matches)
			}
			if cancelRequest {
				if _, err = os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
					t.Fatal("取消后出现最终文件", err)
				}
			} else {
				data, e := os.ReadFile(filepath.Join(directory, name))
				if e != nil || len(data) != 8*8192 {
					t.Fatal(len(data), e)
				}
			}
			if s.View().Status != "DISCONNECTED" {
				t.Fatal(s.View())
			}
		})
	}
}

func TestFileErrorHTTPStatusAndEnvelope(t *testing.T) {
	_, m, _ := api(t)
	for _, item := range []struct {
		code   string
		status int
	}{{"SFTP_INVALID_PATH", 400}, {"SFTP_FILE_EXISTS", 409}, {"SFTP_BUSY", 409}, {"SFTP_ATOMIC_UNAVAILABLE", 409}, {"SFTP_TOO_LARGE", 413}, {"SFTP_PERMISSION_DENIED", 403}, {"SFTP_NO_SPACE", 500}, {"SFTP_UNAVAILABLE", 503}, {"SFTP_CANCEL_FAILED", 503}} {
		w := httptest.NewRecorder()
		WriteError(w, fault.Error(item.code), m.Logger)
		var body envelope[json.RawMessage]
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != item.status || string(body.Code) != strconv.Quote(item.code) || string(body.Data) != "{}" {
			t.Fatal(item, w.Code, w.Body.String(), err)
		}
	}
}
func TestFilesTrailingMultipartFieldPreservesOldFile(t *testing.T) {
	h, m, cookie, id, directory := fileAPI(t)
	if err := os.WriteFile(filepath.Join(directory, "old"), []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	_ = form.WriteField("directory", directory)
	_ = form.WriteField("overwrite", "true")
	file, _ := form.CreateFormFile("file", "old")
	_, _ = io.WriteString(file, "REPLACEMENT")
	_ = form.WriteField("unexpected", "x")
	_ = form.Close()
	r := httptest.NewRequest("POST", "/api/sessions/"+id+"/files", body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Origin", m.Config.Server.PublicOrigin)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(directory, "old"))
	if err != nil || string(data) != "ORIGINAL" {
		t.Fatal(string(data), err)
	}
	temporary, _ := filepath.Glob(filepath.Join(directory, ".web-ssh-upload-*"))
	if len(temporary) != 0 {
		t.Fatal(temporary)
	}
}
