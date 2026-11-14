package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

type Health struct {
	Status string `json:"status"`
}

func NewHandler(assets fs.FS, ready func() bool, logger *slog.Logger) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/api/health" && r.Method == http.MethodGet {
			if !ready() {
				JSON(w, 503, ErrorResponse[Health]{ServerUnavailable, "服务尚未就绪", Health{"not_ready"}}, logger)
				return
			}
			JSON(w, 200, Response[Health]{0, "success", Health{"ready"}}, logger)
			return
		}
		// 未实现的 API/WS 必须返回错误，不能被 SPA 回退伪装成成功页面。
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" || strings.HasPrefix(r.URL.Path, "/ws/") {
			WriteError(w, &Error{InvalidRequest}, logger)
			return
		}
		// 缓存头封装仅作用于静态资源，不遮蔽 API 接入的可选 HTTP 接口。
		w = noStoreWriter{w}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", 405)
			return
		}
		// 只对本版明确的前端路由回退；丢失资源及未知路径保持 404。
		if r.URL.Path == "/" || r.URL.Path == "/login" {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			files.ServeHTTP(w, clone)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// 标准库静态文件错误路径会清除缓存头，因此在最终写响应头时统一补回。
type noStoreWriter struct{ http.ResponseWriter }

func (w noStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}
func (w noStoreWriter) Write(data []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(data)
}
func (w noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
