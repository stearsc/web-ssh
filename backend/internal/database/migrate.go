package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stearsc/web-ssh/backend/migrations"
)

func Migrate(ctx context.Context, db *sql.DB) error { return migrate(ctx, db, migrations.Files) }

func migrate(ctx context.Context, db *sql.DB, source fs.FS) error {
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		return err
	}
	type migration struct {
		version int64
		name    string
	}
	ordered := make([]migration, 0, len(names))
	known := map[int64]bool{}
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		v, e := strconv.ParseInt(prefix, 10, 64)
		if !ok || e != nil || v <= 0 || known[v] {
			return fmt.Errorf("迁移文件版本非法: %s", name)
		}
		known[v] = true
		ordered = append(ordered, migration{v, name})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].version < ordered[j].version })
	return WithTx(ctx, db, func(tx *sql.Tx) error {
		// 版本表创建与全部待执行迁移共用短事务，失败时保留原有数据库。
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
   version INTEGER NOT NULL PRIMARY KEY, -- 已应用的迁移版本
   applied_at INTEGER NOT NULL -- UTC Unix 毫秒
  )`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
		if err != nil {
			return err
		}
		applied := map[int64]bool{}
		for rows.Next() {
			var version int64
			if err := rows.Scan(&version); err != nil {
				rows.Close()
				return err
			}
			applied[version] = true
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		for v := range applied {
			if !known[v] {
				return fmt.Errorf("数据库迁移版本 %d 不受当前程序支持", v)
			}
		}
		for _, m := range ordered {
			if applied[m.version] {
				continue
			}
			data, err := fs.ReadFile(source, m.name)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, string(data)); err != nil {
				return fmt.Errorf("迁移 %s 失败: %w", m.name, err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,applied_at) VALUES (?,?)", m.version, time.Now().UTC().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}
