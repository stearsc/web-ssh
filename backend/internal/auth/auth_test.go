package auth

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func setup(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := testutil.DB(t)
	cfg := config.Defaults().Auth
	cfg.LoginRateLimit = 100
	a, err := New(context.Background(), db, cfg, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	return a, db
}
func TestPasswordAndInitialization(t *testing.T) {
	a, db := setup(t)
	if !VerifyPassword(a.hash, "admin-secret") || VerifyPassword(a.hash, "wrong") {
		t.Fatal("密码验证错误")
	}
	b, err := New(context.Background(), db, a.cfg, "new-secret")
	if err != nil || a.hash != b.hash {
		t.Fatal("重复初始化覆盖密码")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("短密码")
	}
	for _, bad := range []string{"plain", "$argon2id$v=19$m=999999999,t=3,p=1$bad$bad", "$argon2id$v=19$m=65536,t=3,p=0$bad$bad"} {
		if VerifyPassword(bad, "anything") {
			t.Fatal("损坏 PHC")
		}
	}
	db2 := testutil.DB(t)
	if _, err := New(context.Background(), db2, a.cfg, ""); err == nil {
		t.Fatal("缺少初始密码")
	}
}
func TestLoginPersistenceExpiryLogout(t *testing.T) {
	a, db := setup(t)
	ctx := context.Background()
	token, s, _, err := a.Login(ctx, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || len(s.ID) != 32 {
		t.Fatal("随机标识格式")
	}
	for _, bad := range []string{"", token + "x", token[:42] + "!"} {
		if _, err := a.Lookup(bad); err != fault.Error("AUTH_REQUIRED") {
			t.Fatal(err)
		}
	}
	b, err := New(ctx, db, a.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lookup(token); err != nil {
		t.Fatal("重启丢失登录", err)
	}
	second, _, _, err := a.Login(ctx, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Lookup(token); err == nil {
		t.Fatal("旧 Cookie 有效")
	}
	if _, err := a.Lookup(second); err != nil {
		t.Fatal("错误退出其他登录")
	}
	b, err = New(ctx, db, a.cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lookup(token); err == nil {
		t.Fatal("重启后旧 Cookie 有效")
	}
	a.now = func() time.Time { return s.Expires }
	if _, err := a.Lookup(second); err != nil && err != fault.Error("AUTH_REQUIRED") {
		t.Fatal(err)
	}
	a.now = func() time.Time { return s.Expires.Add(time.Hour) }
	if _, err := a.Lookup(second); err != fault.Error("AUTH_REQUIRED") {
		t.Fatal(err)
	}
	if err := a.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.sessions) != 0 {
		t.Fatal("过期索引未清理")
	}
}
func TestRateAndCapacityConcurrent(t *testing.T) {
	a, _ := setup(t)
	a.cfg.LoginRateLimit = 3
	a.cfg.MaxSessions = 1
	now := time.Now()
	a.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, _, _, err := a.Login(context.Background(), "wrong"); err != fault.Error("AUTH_FAILED") {
			t.Fatal(err)
		}
	}
	if _, _, retry, err := a.Login(context.Background(), "wrong"); err != fault.Error("LOGIN_RATE_LIMIT") || retry < 1 {
		t.Fatal(err, retry)
	}
	now = now.Add(time.Minute)
	a.cfg.LoginRateLimit = 100
	var wg sync.WaitGroup
	results := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _, err := a.Login(context.Background(), "admin-secret"); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if err != fault.Error("LOGIN_SESSION_LIMIT") {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	now = now.Add(8 * 24 * time.Hour)
	if _, _, _, err := a.Login(context.Background(), "admin-secret"); err != nil {
		t.Fatal("到期容量未回收", err)
	}
}
func TestLogoutTransactionFailure(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	token, s, _, err := a.Login(ctx, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	original := a.transact
	revoked := 0
	a.OnRevoke(func(id string) {
		if id != s.ID {
			t.Error(id)
		}
		revoked++
	})
	a.transact = func(context.Context, func(*sql.Tx) error) database.Outcome { return database.RolledBack }
	if err := a.Logout(ctx, token); err != fault.Error("STORAGE_ERROR") {
		t.Fatal(err)
	}
	if _, err := a.Lookup(token); err != nil || revoked != 0 {
		t.Fatal("回滚改变权限")
	}
	a.transact = func(context.Context, func(*sql.Tx) error) database.Outcome { return database.Uncertain }
	if err := a.Logout(ctx, token); err != fault.Error("SERVER_UNAVAILABLE") {
		t.Fatal(err)
	}
	if _, err := a.Lookup(token); err == nil || revoked != 1 {
		t.Fatal("不确定退出没有冻结")
	}
	a.transact = original
	if err := a.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := a.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func TestRealDatabaseLogoutRollback(t *testing.T) {
	a, db := setup(t)
	ctx := context.Background()
	token, _, _, err := a.Login(ctx, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_logout BEFORE DELETE ON web_auth_sessions BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Logout(ctx, token); err != fault.Error("STORAGE_ERROR") {
		t.Fatal(err)
	}
	if _, err := a.Lookup(token); err != nil {
		t.Fatal("确定回滚导致注销", err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_logout"); err != nil {
		t.Fatal(err)
	}
	if err := a.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func TestExpiryAtExactDeadline(t *testing.T) {
	a, _ := setup(t)
	token, session, _, err := a.Login(context.Background(), "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return session.Expires }
	if _, err := a.Lookup(token); err != fault.Error("AUTH_REQUIRED") {
		t.Fatal("到期边界仍有效", err)
	}
}
