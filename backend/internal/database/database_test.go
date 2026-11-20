package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/migrations"
)

func openTest(t *testing.T, path string, busy int64) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Open(ctx, config.Database{Path: path, BusyTimeout: busy}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
func execTest(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

const serverInsert = `INSERT INTO servers (id,name,host,port,username,auth_type,password_encrypted,created_at,updated_at) VALUES ('server','name','localhost',22,'user','password',x'01',1,1)`
const sessionInsert = `INSERT INTO sessions(id,server_id,name,host,port,username,status,created_at) VALUES ('session',?,'name','localhost',22,'user','CONNECTED',1)`

func verifyPragmas(t *testing.T, db *sql.DB, busy int64) {
	t.Helper()
	var wal string
	var fk, wait int64
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&wal); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wal != "wal" || fk != 1 || wait != busy {
		t.Fatalf("PRAGMA: %s %d %d", wal, fk, wait)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("连接池上限不是 1")
	}
	t.Logf("PRAGMA journal_mode=%s foreign_keys=%d busy_timeout=%d", wal, fk, wait)
}
func TestMigrationReopenForeignKeysAndBackup(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "web-ssh.db")
	db := openTest(t, filename, 120)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	var names string
	if err := db.QueryRow("SELECT group_concat(name, ',') FROM (SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name)").Scan(&names); err != nil {
		t.Fatal(err)
	}
	if names != "admin_credentials,private_key_objects,schema_migrations,servers,sessions,web_auth_sessions" {
		t.Fatal(names)
	}
	verifyPragmas(t, db, 120)
	execTest(t, db, serverInsert)
	if _, err := db.Exec(sessionInsert, "absent"); err == nil {
		t.Fatal("外键没有拒绝非法写入")
	}
	execTest(t, db, sessionInsert, "server")
	for _, q := range []string{
		"UPDATE servers SET trusted_host='localhost'",
		"UPDATE servers SET port=0",
		"UPDATE sessions SET status='UNKNOWN'",
		"UPDATE sessions SET close_reason='UNKNOWN'",
		"INSERT INTO admin_credentials VALUES (2,'hash',1,1)",
		"INSERT INTO web_auth_sessions VALUES ('login',x'00',1,2)",
		"INSERT INTO web_auth_sessions VALUES ('login',zeroblob(32),2,1)",
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("约束未生效: %s", q)
		}
	}
	// 驱逐真实物理连接；下一次查询必须重新设置并回读 PRAGMA。
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	conn.Close()
	verifyPragmas(t, db, 120)
	if _, err := db.Exec(strings.Replace(sessionInsert, "'session'", "'after-reopen'", 1), "absent"); err == nil {
		t.Fatal("重开后外键未生效")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(filename + suffix); err != nil {
			t.Fatal("持久化旁路文件缺失", suffix, err)
		}
	}
	// 运行中使用 SQLite 自己的一致性快照，不能直接复制正在使用的 .db。
	backup := filepath.Join(t.TempDir(), "backup.db")
	execTest(t, db, "VACUUM INTO ?", backup)
	snapshot := openTest(t, backup, 120)
	if err := snapshot.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 1 {
		t.Fatal("备份缺少已提交记录", count, err)
	}
	if err := EndPreviousSessions(ctx, db, time.UnixMilli(1234)); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	var closed int64
	if err := db.QueryRow("SELECT status,close_reason,closed_at FROM sessions").Scan(&status, &reason, &closed); err != nil || status != "CLOSED" || reason != "process_restarted" || closed != 1234 {
		t.Fatal(status, reason, closed, err)
	}
	execTest(t, db, "DELETE FROM servers")
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatal("级联删除失败", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err == nil {
		t.Fatal("关闭后仍可使用数据库")
	}
	reopened := openTest(t, filename, 120)
	if err := Migrate(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	verifyPragmas(t, reopened, 120)
	t.Log("首次/重复迁移、驱逐重开、外键拒绝/级联、WAL 同目录、VACUUM INTO 备份、启动清理及关闭已验证")
}
func TestMigrationFailureRollback(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "test.db"), 100)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	execTest(t, db, serverInsert)
	initial, err := migrations.Files.ReadFile("001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	keyMigration, err := migrations.Files.ReadFile("002_private_key_auth.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"002_private_key_auth.sql": &fstest.MapFile{Data: keyMigration}, "001_initial.sql": &fstest.MapFile{Data: initial}, "003_failure.sql": &fstest.MapFile{Data: []byte("CREATE TABLE should_rollback(id INTEGER); INVALID SQL;")}}
	if err := migrate(context.Background(), db, source); err == nil {
		t.Fatal("迁移失败未报告")
	}
	var count int
	for q, want := range map[string]int{"SELECT count(*) FROM servers": 1, "SELECT count(*) FROM schema_migrations": 2, "SELECT count(*) FROM sqlite_schema WHERE name='should_rollback'": 0} {
		if err := db.QueryRow(q).Scan(&count); err != nil || count != want {
			t.Fatal(q, count, err)
		}
	}
	execTest(t, db, "INSERT INTO schema_migrations VALUES(999,1)")
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("未知版本未拒绝")
	}
}
func TestBusyWaitAndContext(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "busy.db")
	a := openTest(t, filename, 150)
	b := openTest(t, filename, 150)
	if err := Migrate(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	tx, err := a.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(serverInsert); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = b.Exec("INSERT INTO admin_credentials VALUES(1,'hash',1,1)")
	elapsed := time.Since(start)
	if err == nil || elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("锁等待不符合预算: %v %s", err, elapsed)
	}
	t.Logf("真实写锁竞争等待 %s 后返回错误", elapsed)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = b.ExecContext(ctx, "INSERT INTO admin_credentials VALUES(1,'hash',1,1)")
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("调用截止未生效", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	execTest(t, b, "INSERT INTO admin_credentials VALUES(1,'hash',1,1)")
}
func TestUnwritableAndInvalidPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	db, err := Open(context.Background(), config.Database{Path: filepath.Join(dir, "no.db"), BusyTimeout: 100}, time.Second)
	if err == nil {
		db.Close()
		t.Fatal("不可写目录未拒绝")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), config.Database{Path: filepath.Join(file, "bad.db"), BusyTimeout: 100}, time.Second); err == nil {
		t.Fatal("非目录未拒绝")
	}
	literal := filepath.Join(t.TempDir(), "name?mode=memory.db")
	db = openTest(t, literal, 100)
	verifyPragmas(t, db, 100)
	if _, err := os.Stat(literal); err != nil {
		t.Fatal("路径被误当 DSN", err)
	}
	if !strings.Contains(literal, "?") {
		t.Fatal("测试路径错误")
	}
}
