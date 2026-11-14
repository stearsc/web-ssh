package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"modernc.org/sqlite"
)

// connector 只包装物理连接的创建，不包装返回的连接，保留驱动的取消等可选接口。
type connector struct {
	driver.Connector
	busy    int64
	timeout time.Duration
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (driver.Conn, error) { return nil, errors.Join(err, conn.Close()) }
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		return fail(errors.New("SQLite 驱动不支持带期限的执行"))
	}
	query, ok := conn.(driver.QueryerContext)
	if !ok {
		return fail(errors.New("SQLite 驱动不支持带期限的查询"))
	}
	for _, stmt := range []string{"PRAGMA busy_timeout = " + strconv.FormatInt(c.busy, 10), "PRAGMA foreign_keys = ON"} {
		if _, err := exec.ExecContext(ctx, stmt, nil); err != nil {
			return fail(err)
		}
	}
	// WAL 是数据库级设置，仍在每次物理连接建立时确认实际返回值。
	for _, item := range []struct {
		sql  string
		want driver.Value
	}{
		{"PRAGMA journal_mode = WAL", "wal"}, {"PRAGMA foreign_keys", int64(1)}, {"PRAGMA busy_timeout", c.busy},
	} {
		rows, err := query.QueryContext(ctx, item.sql, nil)
		if err != nil {
			return fail(err)
		}
		values := make([]driver.Value, 1)
		readErr := rows.Next(values)
		closeErr := rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return fail(err)
		}
		if values[0] != item.want {
			return fail(fmt.Errorf("SQLite %s 回读不符合要求", item.sql))
		}
	}
	return conn, nil
}

func Open(ctx context.Context, c config.Database, timeout time.Duration) (*sql.DB, error) {
	filename, err := filepath.Abs(c.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	// URI 编码保证配置中的 ?、# 等字符作为文件名，不成为驱动参数。
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(filename)}
	base, err := sqlite.NewConnector(uri.String())
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector{Connector: base, busy: c.BusyTimeout, timeout: timeout})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("初始化 SQLite 失败: %w", err), db.Close())
	}
	return db, nil
}

// WithTx 只用于短持久化操作；调用方传入期限，不在事务里等待网络或用户。
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		e := tx.Rollback()
		if e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	// 提交失败可能存在不确定结果，向上原样报告，不能宣称确定回滚或重试成功。
	return tx.Commit()
}

// EndPreviousSessions 仅清理历史元数据，不创建 Session 或任何运行时会话对象。
func EndPreviousSessions(ctx context.Context, db *sql.DB, now time.Time) error {
	return WithTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET status='CLOSED',closed_at=?,close_reason='process_restarted' WHERE status<>'CLOSED'`, now.UTC().UnixMilli())
		return err
	})
}
