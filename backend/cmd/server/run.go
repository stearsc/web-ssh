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
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/httpapi"
	"github.com/stearsc/web-ssh/backend/internal/maintenance"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
	"github.com/stearsc/web-ssh/backend/web"
)

func run(ctx context.Context, c config.Config, logger *slog.Logger) error {
	lock, err := maintenance.Acquire(c.Database.Path)
	if err != nil {
		return err
	}
	// 启动阶段由 run 持锁；进入 serve 后由它在全部资源关闭后释放。
	canReleaseLock := true
	defer func() {
		if canReleaseLock {
			lock.Close()
		}
	}()
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
	fail := func(err error) error {
		closeErr := db.Close()
		// 数据库关闭失败时也保守持锁，由进程退出回收描述符。
		canReleaseLock = closeErr == nil
		return errors.Join(err, closeErr)
	}
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
	if err := database.EndPreviousSessions(startup, db, time.Now()); err != nil {
		return fail(err)
	}
	if !managed.KeyVerificationAvailable() {
		logger.Warn("主密钥格式正确，无法验证历史匹配性")
	}
	if err := managed.ReconcilePrivateKeys(startup); err != nil {
		var business fault.Error
		if errors.As(err, &business) && business == "PRIVATE_KEY_STORAGE_UNAVAILABLE" {
			logger.Warn("私钥卷不可用，暂停私钥写入和回收")
		} else {
			return fail(errors.New("私钥对象数据库对账失败"))
		}
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
	sessions := session.New(ctx, db, managed, c, logger)
	sockets := browser.New(c, logger)
	admin.OnRevoke(func(id string) { managed.Revoke(id); sessions.Revoke(id); sockets.Revoke(id) })
	cancel() // 启动完成即释放启动计时器，不让它伴随长期服务。
	canReleaseLock = false
	return serve(ctx, c, db, lock, assets, listener, logger, &httpapi.Management{Auth: admin, Servers: managed, Sessions: sessions, Websocket: sockets, Config: c, Logger: logger})
}

// serve 拥有 HTTP、DB 和维护锁的退出顺序。后续资源在此应用入口接入，
// 不能由 HTTP 请求拥有 SSH 生命周期，也不能为每个清理步骤重置总预算。
func serve(ctx context.Context, c config.Config, db *sql.DB, lock *maintenance.Lock, assets fs.FS, listener net.Listener, logger *slog.Logger, management ...*httpapi.Management) error {
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
			gcTicker := time.NewTicker(10 * time.Minute)
			defer gcTicker.Stop()
			defer ticker.Stop()
			for {
				select {
				case <-workCtx.Done():
					return
				case <-gcTicker.C:
					cleanup, stop := context.WithTimeout(workCtx, budget)
					if err := management[0].Servers.SweepPrivateKeys(cleanup, logger); err != nil && workCtx.Err() == nil {
						logger.Warn("私钥回收已暂停")
					}
					stop()
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
		ConnContext:       browser.ConnContext,
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
	if len(management) > 0 {
		if management[0].Sessions != nil {
			deadline, _ := shutdown.Deadline()
			management[0].Sessions.StopAt(deadline)
		}
		if management[0].Websocket != nil {
			deadline, _ := shutdown.Deadline()
			management[0].Websocket.Stop(deadline)
		}
	}
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
	if len(management) > 0 {
		if management[0].Sessions != nil {
			select {
			case <-management[0].Sessions.Done():
			case <-shutdown.Done():
				return errors.Join(serveErr, shutdownErr, shutdown.Err())
			}
		}
		if management[0].Websocket != nil {
			if err := management[0].Websocket.Wait(shutdown); err != nil {
				return errors.Join(serveErr, shutdownErr, err)
			}
		}
	}
	dbErr := db.Close()
	var lockErr error
	if dbErr == nil && lock != nil {
		lockErr = lock.Close()
	}

	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	err := errors.Join(serveErr, shutdownErr, dbErr, lockErr)
	if err == nil {
		logger.Info("HTTP 与 SQLite 已关闭")
	}
	return err
}
