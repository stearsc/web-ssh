package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/session"
)

// IsFileUploadRequest 精确限定期限例外；check 和其他 API 仍使用原有总期限。
func IsFileUploadRequest(r *http.Request) bool {
	if r.Method != "POST" {
		return false
	}
	parts := strings.Split(r.URL.Path, "/")
	return len(parts) == 5 && parts[1] == "api" && parts[2] == "sessions" && idPattern.MatchString(parts[3]) && parts[4] == "files"
}

// PrepareFileUploadHTTP 只在上传路由取消固定写入总期限；读取期限随实际读取推进。
func PrepareFileUploadHTTP(w http.ResponseWriter, r *http.Request, budget time.Duration) error {
	control := http.NewResponseController(w)
	for _, err := range []error{control.EnableFullDuplex(), control.SetReadDeadline(time.Now().Add(budget)), control.SetWriteDeadline(time.Time{})} {
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	return nil
}

type uploadReader struct {
	reader  io.Reader
	control *http.ResponseController
	budget  time.Duration
}

func (r uploadReader) Read(data []byte) (int, error) {
	if err := r.control.SetReadDeadline(time.Now().Add(r.budget)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return r.reader.Read(data)
}

func nextUploadPart(reader *multipart.Reader) (*multipart.Part, error) {
	part, err := reader.NextRawPart()
	if err != nil {
		return nil, err
	}
	// 不能接受会对原始内容做隐式解码的传输编码。
	if part.Header.Get("Content-Transfer-Encoding") != "" {
		return nil, fault.Error("INVALID_REQUEST")
	}
	return part, nil
}

func uploadParts(reader *multipart.Reader) (directory, filename string, overwrite bool, file *multipart.Part, err error) {
	seen := map[string]bool{}
	for {
		var part *multipart.Part
		part, err = nextUploadPart(reader)
		if err != nil {
			return "", "", false, nil, fault.Error("INVALID_REQUEST")
		}
		disposition, params, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		name := params["name"]
		if parseErr != nil || disposition != "form-data" || seen[name] {
			return "", "", false, nil, fault.Error("INVALID_REQUEST")
		}
		seen[name] = true
		if name == "file" {
			// 元数据必须位于 file 之前，才能直接流向远端，不缓存完整文件。
			if !seen["directory"] || params["filename"] == "" {
				return "", "", false, nil, fault.Error("INVALID_REQUEST")
			}
			return directory, params["filename"], overwrite, part, nil
		}
		if name != "directory" && name != "overwrite" || params["filename"] != "" {
			return "", "", false, nil, fault.Error("INVALID_REQUEST")
		}
		value, e := io.ReadAll(io.LimitReader(part, 4097))
		if e != nil || len(value) > 4096 {
			return "", "", false, nil, fault.Error("INVALID_REQUEST")
		}
		if name == "directory" {
			directory = string(value)
		} else {
			if string(value) != "true" && string(value) != "false" {
				return "", "", false, nil, fault.Error("INVALID_REQUEST")
			}
			overwrite = string(value) == "true"
		}
	}
}

func (m *Management) fileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/files/check", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		var input struct {
			Directory string `json:"directory"`
			Filename  string `json:"filename"`
		}
		if err := m.decode(w, r, &input, false); err != nil {
			return err
		}
		s, err := m.Sessions.FindForFiles(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		result, err := s.CheckFile(r.Context(), input.Directory, input.Filename)
		if err != nil {
			return err
		}
		m.success(w, 200, result)
		return nil
	}))
	mux.HandleFunc("POST /api/sessions/{id}/files", m.protected(func(w http.ResponseWriter, r *http.Request, _ *auth.Session) error {
		budget := time.Duration(m.Config.Server.RequestTimeout) * time.Second
		control := http.NewResponseController(w)
		defer func() { _ = control.SetWriteDeadline(time.Now().Add(budget)) }()
		if err := PrepareFileUploadHTTP(w, r, budget); err != nil {
			return fault.Error("SFTP_UNAVAILABLE")
		}
		// 先解除 Read 的网络阻塞，再 Close；回调必须在 Handler 返回前退出。
		body := r.Body
		interrupt := func() { _ = control.SetReadDeadline(time.Now()); _ = body.Close() }
		interruptDone := make(chan struct{})
		stop := context.AfterFunc(r.Context(), func() { interrupt(); close(interruptDone) })
		defer func() {
			if !stop() {
				<-interruptDone
			}
		}()
		s, err := m.Sessions.FindForFiles(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		r.Body = struct {
			io.Reader
			io.Closer
		}{uploadReader{body, control, budget}, body}
		reader, err := r.MultipartReader()
		if err != nil {
			return fault.Error("INVALID_REQUEST")
		}
		directory, filename, overwrite, file, err := uploadParts(reader)
		if err != nil {
			return err
		}
		result, err := s.UploadFile(r.Context(), directory, filename, overwrite, session.FileSource{
			Reader: file, Interrupt: interrupt,
			Finish: func() error {
				_, err := nextUploadPart(reader)
				if err != io.EOF {
					return fault.Error("INVALID_REQUEST")
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
		_ = control.SetWriteDeadline(time.Now().Add(budget))
		m.success(w, 200, result)
		return nil
	}))
}
