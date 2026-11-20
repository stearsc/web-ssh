package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/stearsc/web-ssh/backend/internal/fault"
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
	var business fault.Error
	if errors.As(err, &business) {
		code = Code(business)
	}
	status := 500
	message := "服务暂时异常"
	switch code {
	case InvalidRequest, "INVALID_SIZE":
		status = 400
		message = "请求格式或参数不正确"
	case MessageTooLarge:
		status = 413
		message = "请求内容超过大小限制"
	case "PRIVATE_KEY_INVALID":
		status = 400
		message = "私钥格式或口令无效，或密钥超出安全限制"
	case "PRIVATE_KEY_UNAVAILABLE":
		status = 503
		message = "保存的私钥不可用，请重新上传修复"
	case "PRIVATE_KEY_STORAGE_UNAVAILABLE":
		status = 503
		message = "私钥存储暂不可用，请稍后重试"
	case ServerUnavailable:
		status = 503
		message = "服务尚未就绪"
	case ServerShuttingDown:
		status = 503
		message = "服务正在停止"
	case "AUTH_REQUIRED", "AUTH_FAILED":
		status = 401
		message = "登录已失效，请重新登录"
		if code == "AUTH_FAILED" {
			message = "管理密码不正确"
		}
	case "PERMISSION_DENIED":
		status = 403
		message = "请求来源或操作权限不符"
	case "PRIVATE_KEY_UPLOAD_BUSY", "PRIVATE_KEY_PARSE_BUSY", "LOGIN_RATE_LIMIT", "LOGIN_SESSION_LIMIT", "HOST_KEY_PROBE_LIMIT", "SESSION_LIMIT", "CREDENTIAL_LIMIT", "CONNECTION_LIMIT_REACHED":
		status = 429
		message = "请求或有效凭证数量已达上限，请稍后重试"
	case "SESSION_NOT_FOUND":
		status = 404
		message = "SSH 会话不存在或已关闭"
	case "SERVER_NOT_FOUND":
		status = 404
		message = "服务器不存在或已删除"
	case "SERVER_HAS_ACTIVE_SESSIONS":
		status = 409
		message = "服务器仍有活动会话，请先关闭相关会话"
	case "SESSION_NOT_READY":
		status = 409
		message = "SSH 会话当前不能绑定"
	case "BIND_BUSY":
		status = 409
		message = "SSH 会话已有在线操作端"
	case "BIND_CONFLICT":
		status = 409
		message = "SSH 会话绑定代次已变化，请查询状态"
	case "SERVER_CONFIG_CHANGED", "INVALID_PROBE":
		status = 409
		message = "服务器配置或探测凭证已变化，请重新测试"
	case "STORAGE_ERROR":
		status = 500
		message = "保存未完成，请重试；退出登录时请保留页面重试退出"
	case "SSH_CONNECT_TIMEOUT":
		status = 504
		message = "SSH 连接测试超时"
	case "SSH_CONNECT_FAILED", "SSH_PTY_FAILED", "SSH_SHELL_FAILED":
		status = 502
		message = "无法建立 SSH 连接"
	case "SSH_AUTH_FAILED":
		status = 502
		message = "SSH 用户认证失败，请检查用户名及认证凭据"
	default:
		code = InternalError
		logger.Error("请求处理失败")
	}
	JSON(w, status, ErrorResponse[Empty]{code, message, Empty{}}, logger)
}
