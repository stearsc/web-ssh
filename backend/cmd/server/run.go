package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/httpapi"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/web"
)

func run(ctx context.Context, c config.Config, logger *slog.Logger) error {
	key, err := config.LoadEncryptionKey()
	if err != nil {
		return err
	}
	cipher, err := secret.New(key)
	clear(key)
	if err != nil {
		return err
	}
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
	admin, err := auth.New(startup, db, c.Auth, os.Getenv("APP_ADMIN_PASSWORD"))
	if err != nil {
		return fail(err)
	}
	managed, err := servers.New(startup, db, cipher, c)
	if err != nil {
		return fail(err)
	}
	admin.OnRevoke(managed.Revoke)
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
	return serve(ctx, c, db, assets, listener, logger, &httpapi.Management{Auth: admin, Servers: managed, Config: c, Logger: logger})
}

// serve 拥有本阶段的 HTTP 和 DB 退出顺序。后续资源在此应用入口接入，
// 不能由 HTTP 请求拥有 SSH 生命周期，也不能为每个清理步骤重置总预算。
func serve(ctx context.Context, c config.Config, db *sql.DB, assets fs.FS, listener net.Listener, logger *slog.Logger, management ...*httpapi.Management) error {
	var ready atomic.Bool
	ready.Store(true)
	handler := httpapi.NewHandler(assets, ready.Load, logger)
	if len(management) > 0 {
		handler = management[0].Handler(handler, ready.Load)
	}
	var admission sync.Mutex
	var workers sync.WaitGroup
	var active atomic.Int64
	stopping := false
	budget := time.Duration(c.Server.RequestTimeout) * time.Second
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	if len(management) > 0 {
		workers.Add(1)
		active.Add(1)
		go func() {
			defer func() { workers.Done(); active.Add(-1) }()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-workCtx.Done():
					return
				case <-ticker.C:
					cleanup, cancel := context.WithTimeout(workCtx, budget)
					if err := management[0].Auth.Cleanup(cleanup); err != nil && workCtx.Err() == nil {
						logger.Error("清理过期登录记录失败")
					}
					cancel()
					management[0].Servers.Cleanup()
				}
			}
		}()
	}
	server := &http.Server{
		ReadHeaderTimeout: time.Duration(c.Server.ReadHeaderTimeout) * time.Second,
		ReadTimeout:       budget, WriteTimeout: budget,
		BaseContext: func(net.Listener) context.Context { return workCtx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			admission.Lock()
			if stopping {
				admission.Unlock()
				httpapi.WriteError(w, &httpapi.Error{Code: httpapi.ServerShuttingDown}, logger)
				return
			}
			workers.Add(1)
			active.Add(1)
			admission.Unlock()
			defer func() { workers.Done(); active.Add(-1) }()
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
	admission.Lock()
	stopping = true
	ready.Store(false)
	admission.Unlock()
	cancelWork()
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
	// Shutdown 返回不保证超时后的 Handler 已退出；显式等待业务与清理任务。
	drained := make(chan struct{})
	go func() { workers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-shutdown.Done():
		if active.Load() != 0 {
			logger.Error("退出期限内仍有业务任务未结束，保留 SQLite 连接至进程退出")
			return errors.Join(serveErr, shutdownErr, shutdown.Err())
		}
	}
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
