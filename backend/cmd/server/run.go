package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/httpapi"
	"github.com/stearsc/web-ssh/backend/web"
)

func run(ctx context.Context, c config.Config, logger *slog.Logger) error {
	key, err := config.LoadEncryptionKey()
	if err != nil {
		return err
	}
	// 本阶段不进行加解密；验证后释放临时密钥引用，后续由 crypto 模块接管。
	clear(key)
	startup, cancel := context.WithTimeout(ctx, time.Duration(c.Server.RequestTimeout)*time.Second)
	defer cancel()
	db, err := database.Open(startup, c.Database, time.Duration(c.Server.RequestTimeout)*time.Second)
	if err != nil {
		return err
	}
	fail := func(err error) error { return errors.Join(err, db.Close()) }
	if err := database.Migrate(startup, db); err != nil {
		return fail(err)
	}
	if err := database.EndPreviousSessions(startup, db, time.Now()); err != nil {
		return fail(err)
	}
	assets, err := web.Assets()
	if err != nil {
		return fail(err)
	}
	if err := startup.Err(); err != nil {
		return fail(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(c.Server.Host, strconv.FormatInt(c.Server.Port, 10)))
	if err != nil {
		return fail(err)
	}
	cancel() // 启动完成即释放启动计时器，不让它伴随长期服务。
	return serve(ctx, c, db, assets, listener, logger)
}

// serve 拥有本阶段的 HTTP 和 DB 退出顺序。后续资源在此应用入口接入，
// 不能由 HTTP 请求拥有 SSH 生命周期，也不能为每个清理步骤重置总预算。
func serve(ctx context.Context, c config.Config, db *sql.DB, assets fs.FS, listener net.Listener, logger *slog.Logger) error {
	var ready atomic.Bool
	ready.Store(true)
	handler := httpapi.NewHandler(assets, ready.Load, logger)
	budget := time.Duration(c.Server.RequestTimeout) * time.Second
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	server := &http.Server{
		ReadHeaderTimeout: time.Duration(c.Server.ReadHeaderTimeout) * time.Second,
		ReadTimeout:       budget, WriteTimeout: budget,
		BaseContext: func(net.Listener) context.Context { return workCtx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// HTTP 截止时间不应在后续阶段传给 Session 或升级后的 WebSocket。
			request, cancel := context.WithTimeout(r.Context(), budget)
			defer cancel()
			handler.ServeHTTP(w, r.WithContext(request))
		}),
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("HTTP 服务已启动", "address", listener.Addr().String())
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
		done = nil
	}
	ready.Store(false)
	// 清理上下文独立于已取消的应用/请求上下文，所有清理共享绝对截止时间。
	shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(c.Server.ShutdownTimeout)*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	cancelWork()
	if done != nil {
		select {
		case serveErr = <-done:
		case <-shutdown.Done():
			serveErr = shutdown.Err()
		}
	}
	// 本阶段所有数据库操作已在开放 HTTP 前完成，HTTP 不持有事务或 DB 连接。
	// 因此在停止 HTTP 后同步关闭空闲池，不能启动关库任务后提前返回。
	// 后续引入业务写入时，必须先在同一总期限内结束写入任务，再走这里。
	dbErr := db.Close()

	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	err := errors.Join(serveErr, shutdownErr, dbErr)
	if err == nil {
		logger.Info("HTTP 与 SQLite 已关闭")
	}
	return err
}
