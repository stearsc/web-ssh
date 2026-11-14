package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

type Code string

const (
	InvalidRequest     Code = "INVALID_REQUEST"
	MessageTooLarge    Code = "MESSAGE_TOO_LARGE"
	InternalError      Code = "INTERNAL_ERROR"
	ServerUnavailable  Code = "SERVER_UNAVAILABLE"
	ServerShuttingDown Code = "SERVER_SHUTTING_DOWN"
)

type Error struct{ Code Code }

func (e *Error) Error() string { return string(e.Code) }

type Empty struct{}
type Response[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}
type ErrorResponse[T any] struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func JSON(w http.ResponseWriter, status int, value any, logger *slog.Logger) {
	data, err := json.Marshal(value)
	if err != nil {
		logger.Error("响应编码失败")
		status = http.StatusInternalServerError
		data = []byte(`{"code":"INTERNAL_ERROR","message":"服务暂时异常","data":{}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		logger.Warn("响应发送失败")
	}
}
func WriteError(w http.ResponseWriter, err error, logger *slog.Logger) {
	code := InternalError
	var apiErr *Error
	if errors.As(err, &apiErr) {
		code = apiErr.Code
	}
	status := 500
	message := "服务暂时异常"
	switch code {
	case InvalidRequest:
		status = 400
		message = "请求格式或参数不正确"
	case MessageTooLarge:
		status = 413
		message = "请求内容超过大小限制"
	case ServerUnavailable:
		status = 503
		message = "服务尚未就绪"
	case ServerShuttingDown:
		status = 503
		message = "服务正在停止"
	default:
		code = InternalError
		logger.Error("请求处理失败")
	}
	JSON(w, status, ErrorResponse[Empty]{code, message, Empty{}}, logger)
}
