package privatekey

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func storeFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(testutil.DB(t), path, time.Hour)
	now := time.Now()
	store.now = func() time.Time { return now }
	return store, path
}

func saveObject(t *testing.T, s *Store) (*Lease, []byte) {
	t.Helper()
	raw := []byte("原始文件\r\n unchanged bytes \n")
	lease, err := s.Save(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return lease, raw
}

func expire(s *Store) { now := s.now().Add(2 * time.Hour); s.now = func() time.Time { return now } }

func reference(t *testing.T, s *Store, id string) {
	t.Helper()
	result := s.Change(t.Context(), "server", []string{id}, database.TransactionFor(s.db), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO servers(id,name,host,port,username,auth_type,private_key_object_id,created_at,updated_at) VALUES('server','server','localhost',22,'user','private_key',?,1,1)`, id)
		return err
	})
	if result != database.Committed {
		t.Fatal(result)
	}
}

func TestRawBytesPermissionsLeaseReferenceAndGrace(t *testing.T) {
	s, path := storeFixture(t)
	upload, raw := saveObject(t, s)
	got, err := s.Read(t.Context(), upload.ID)
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatal("原始字节变化", err)
	}
	info, err := os.Stat(filepath.Join(path, upload.ID+".key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("文件权限", err)
	}
	expire(s)
	result, err := s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 0 {
		t.Fatal("提升 lease 被 GC 穿过", result, err)
	}
	upload.Release()
	upload.Release()
	lease, err := s.Acquire(t.Context(), upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 0 {
		t.Fatal("短任务 lease 被 GC 穿过", result, err)
	}
	reference(t, s, upload.ID)
	lease.Release()
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 0 {
		t.Fatal("DB 引用被删除", result, err)
	}
	outcome := s.Change(t.Context(), "server", []string{upload.ID}, database.TransactionFor(s.db), func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM servers WHERE id='server'`); return err })
	if outcome != database.Committed {
		t.Fatal(outcome)
	}
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 0 {
		t.Fatal("新候选宽限被绕过", result, err)
	}
	expire(s)
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 1 {
		t.Fatal(result, err)
	}
	if _, err = os.Lstat(filepath.Join(path, upload.ID+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("未物理删除", err)
	}
	if _, err = s.Acquire(t.Context(), upload.ID); err != fault.Error("PRIVATE_KEY_UNAVAILABLE") {
		t.Fatal(err)
	}
}

func TestDeleteClaimRejectsConcurrentLeaseAndReference(t *testing.T) {
	s, _ := storeFixture(t)
	upload, _ := saveObject(t, s)
	upload.Release()
	expire(s)
	entered, finish := make(chan struct{}), make(chan struct{})
	s.remove = func(dir *os.File, object Object) error { close(entered); <-finish; return removeObject(dir, object) }
	done := make(chan error, 1)
	go func() { _, err := s.Sweep(context.Background(), nil); done <- err }()
	<-entered
	if _, err := s.Acquire(t.Context(), upload.ID); err != fault.Error("PRIVATE_KEY_UNAVAILABLE") {
		t.Fatal("claim 后准入 lease", err)
	}
	called := false
	outcome := s.Change(t.Context(), "new-server", []string{upload.ID}, database.TransactionFor(s.db), func(*sql.Tx) error { called = true; return nil })
	if outcome != database.RolledBack || called {
		t.Fatal("claim 后准入引用")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationUnknownFilesAndGraceSurviveRestart(t *testing.T) {
	s, path := storeFixture(t)
	upload, _ := saveObject(t, s)
	upload.Release()
	before, err := s.object(t.Context(), upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(path, strings.Repeat("f", 64)+".key")
	if err = os.WriteFile(unknown, []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(s.db, path, time.Hour)
	restarted.now = s.now
	if err = restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ := restarted.object(t.Context(), upload.ID)
	if before.Candidate != after.Candidate {
		t.Fatal("重启重置了宽限")
	}
	expire(restarted)
	result, err := restarted.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 1 {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(unknown); err != nil {
		t.Fatal("未知文件被误删", err)
	}
}

func TestDeleteFailurePersistsAttemptsAndRetries(t *testing.T) {
	s, path := storeFixture(t)
	upload, _ := saveObject(t, s)
	upload.Release()
	expire(s)
	s.remove = func(*os.File, Object) error { return errors.New("injected") }
	result, err := s.Sweep(t.Context(), nil)
	if err != nil || result.Attempted != 1 || result.Deleted != 0 {
		t.Fatal(result, err)
	}
	var attempts int
	var category string
	if err = s.db.QueryRow(`SELECT delete_attempts,last_delete_error FROM private_key_objects WHERE id=?`, upload.ID).Scan(&attempts, &category); err != nil || attempts != 1 || category != "filesystem_error" {
		t.Fatal(attempts, category, err)
	}
	s.remove = removeObject
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 1 {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(filepath.Join(path, upload.ID+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestUnavailableVolumeDatabaseAndReplacedFilePreventDeletion(t *testing.T) {
	for _, scenario := range []string{"volume", "database", "replacement", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, path := storeFixture(t)
			upload, raw := saveObject(t, s)
			upload.Release()
			expire(s)
			file := filepath.Join(path, upload.ID+".key")
			switch scenario {
			case "volume":
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0700)
			case "database":
				s.db.Close()
			case "replacement":
				if err := os.Rename(file, file+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(file, file+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".original", file); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Sweep(t.Context(), nil)
			if scenario == "volume" || scenario == "database" {
				if err == nil || !result.Paused {
					t.Fatal(result, err)
				}
			} else {
				if result.Deleted != 0 {
					t.Fatal(result, err)
				}
			}
			if scenario == "volume" {
				os.Chmod(path, 0700)
			}
			if _, err := os.Lstat(file); err != nil {
				t.Fatal("安全检查失败时仍删除文件", err)
			}
		})
	}
}

func TestUncertainTransactionProtectsNewAndOldObjectsUntilReload(t *testing.T) {
	s, _ := storeFixture(t)
	old, _ := saveObject(t, s)
	newKey, _ := saveObject(t, s)
	old.Release()
	newKey.Release()
	expire(s)
	outcome := s.Change(t.Context(), "owner", []string{old.ID, newKey.ID}, func(context.Context, func(*sql.Tx) error) database.Outcome { return database.Uncertain }, func(*sql.Tx) error { return nil })
	if outcome != database.Uncertain {
		t.Fatal(outcome)
	}
	result, err := s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 0 {
		t.Fatal("不确定事务保护丢失", result, err)
	}
	if err = s.Resolve(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	result, err = s.Sweep(t.Context(), nil)
	if err != nil || result.Deleted != 2 {
		t.Fatal(result, err)
	}
}

func TestSweepFixedDeleteBudget(t *testing.T) {
	s, _ := storeFixture(t)
	for range 23 {
		lease, _ := saveObject(t, s)
		lease.Release()
	}
	expire(s)
	result, err := s.Sweep(t.Context(), nil)
	if err != nil || result.Attempted > 20 || result.Deleted > 20 || result.Checked > 100 {
		t.Fatal(result, err)
	}
	var left int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM private_key_objects WHERE state<>'deleted'`).Scan(&left); err != nil || left < 3 {
		t.Fatal(left, err)
	}
}

func TestPromotionCrashRecoveryAndSharedLastReference(t *testing.T) {
	for _, phase := range []string{"before_promotion", "after_link", "after_promotion"} {
		t.Run(phase, func(t *testing.T) {
			s, path := storeFixture(t)
			upload, _ := saveObject(t, s)
			upload.Release()
			final := filepath.Join(path, upload.ID+".key")
			temporary := filepath.Join(path, upload.ID+".upload")
			if phase == "before_promotion" {
				if err := os.Rename(final, temporary); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "after_link" {
				if err := os.Link(final, temporary); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE private_key_objects SET state='promoting' WHERE id=?`, upload.ID); err != nil {
				t.Fatal(err)
			}
			restarted := NewStore(s.db, path, time.Hour)
			restarted.now = s.now
			if err := restarted.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			expire(restarted)
			result, err := restarted.Sweep(t.Context(), nil)
			if err != nil || result.Deleted != 1 {
				t.Fatal(result, err)
			}
			for _, file := range []string{final, temporary} {
				if _, err = os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("已知崩溃对象残留", err)
				}
			}
		})
	}
	s, _ := storeFixture(t)
	upload, _ := saveObject(t, s)
	reference(t, s, upload.ID)
	upload.Release()
	if _, err := s.db.Exec(`INSERT INTO servers(id,name,host,port,username,auth_type,private_key_object_id,created_at,updated_at) SELECT 'other',name,host,port,username,auth_type,private_key_object_id,created_at,updated_at FROM servers WHERE id='server'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"server", "other"} {
		outcome := s.Change(t.Context(), id, []string{upload.ID}, database.TransactionFor(s.db), func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM servers WHERE id=?`, id); return err })
		if outcome != database.Committed {
			t.Fatal(outcome)
		}
		expire(s)
		result, err := s.Sweep(t.Context(), nil)
		want := 0
		if id == "other" {
			want = 1
		}
		if err != nil || result.Deleted != want {
			t.Fatal(id, result, err)
		}
	}
}
