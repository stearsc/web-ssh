package privatekey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/sys/unix"
)

// Object 只含持久化归属证据，不含私钥字节、口令或客户端路径。
type Object struct {
	ID            string
	Digest        [32]byte
	Size          int
	State         string
	Device, Inode sql.NullInt64
	Candidate     sql.NullInt64
	Attempts      int
}

type Store struct {
	mu        sync.Mutex
	sweepMu   sync.Mutex
	db        *sql.DB
	path      string
	grace     time.Duration
	leases    map[string]int
	uncertain map[string][]string
	now       func() time.Time
	// 单元故障注入只在测试安装，生产总是使用真实安全删除。
	remove func(*os.File, Object) error
}

func NewStore(db *sql.DB, path string, grace time.Duration) *Store {
	return &Store{db: db, path: path, grace: grace, leases: make(map[string]int), uncertain: make(map[string][]string), now: time.Now, remove: removeObject}
}

// Lease 只保护短期文件任务，不能进入长期 Session/probe。
type Lease struct {
	store *Store
	ID    string
	once  sync.Once
}

func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.store.mu.Lock()
		defer l.store.mu.Unlock()
		l.store.leases[l.ID]--
		if l.store.leases[l.ID] == 0 {
			delete(l.store.leases, l.ID)
		}
	})
}

func (s *Store) object(ctx context.Context, id string) (Object, error) {
	object := Object{ID: id}
	if !safeID(id) {
		return object, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	var digest []byte
	err := s.db.QueryRowContext(ctx, `SELECT sha256,byte_size,state,file_device,file_inode,candidate_at,delete_attempts FROM private_key_objects WHERE id=?`, id).Scan(&digest, &object.Size, &object.State, &object.Device, &object.Inode, &object.Candidate, &object.Attempts)
	if err != nil || len(digest) != 32 {
		return object, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	copy(object.Digest[:], digest)
	return object, nil
}

func (s *Store) Acquire(ctx context.Context, id string) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, err := s.object(ctx, id)
	if err != nil || object.State != "ready" {
		return nil, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	s.leases[id]++
	return &Lease{store: s, ID: id}, nil
}

// Read 必须由持有者的 lease 覆盖；GC claim 永远不能穿过已有 lease。
func (s *Store) Read(ctx context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	object, err := s.object(ctx, id)
	hasLease := s.leases[id] > 0
	s.mu.Unlock()
	if err != nil || !hasLease || object.State != "ready" {
		return nil, fault.Error("PRIVATE_KEY_UNAVAILABLE")
	}
	dir, err := openDirectory(s.path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return readObject(dir, object)
}

// Save 的调用方必须先验证私钥；先登记账本再创建原始文件，提升中一直持有上传 lease。
func (s *Store) Save(ctx context.Context, raw []byte) (*Lease, error) {
	if len(raw) == 0 || len(raw) > MaxFileSize {
		return nil, fault.Error("PRIVATE_KEY_INVALID")
	}
	dir, err := openDirectory(s.path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	rawID := make([]byte, 32)
	if _, err := rand.Read(rawID); err != nil {
		return nil, fault.Error("INTERNAL_ERROR")
	}
	id := hex.EncodeToString(rawID)
	digest := sha256.Sum256(raw)
	s.mu.Lock()
	outcome := database.TransactionFor(s.db)(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO private_key_objects(id,sha256,byte_size,created_at,state,candidate_at) VALUES(?,?,?,?,'promoting',?)`, id, digest[:], len(raw), s.now().UnixMilli(), s.now().UnixMilli())
		return err
	})
	if outcome != database.Committed {
		s.mu.Unlock()
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	s.leases[id]++
	s.mu.Unlock()
	lease := &Lease{store: s, ID: id}
	success := false
	defer func() {
		if !success {
			lease.Release()
		}
	}()
	fd, err := unix.Openat(int(dir.Fd()), id+".upload", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	file := os.NewFile(uintptr(fd), "private-key-upload")
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || file.Sync() != nil || dir.Sync() != nil {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	// 身份登记不确定时也不继续写入/提升；已创建的未知文件宁可保留。
	identity := database.TransactionFor(s.db)(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET file_device=?,file_inode=? WHERE id=?`, int64(stat.Dev), int64(stat.Ino), id)
		return err
	})
	if identity != database.Committed {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	if n, err := file.Write(raw); err != nil || n != len(raw) {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	if errors.Join(file.Sync(), file.Close()) != nil {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	// linkat 不覆盖目标；随后移除暂存名并 fsync 目录，文件原始字节不可变。
	if unix.Linkat(int(dir.Fd()), id+".upload", int(dir.Fd()), id+".key", 0) != nil || unix.Unlinkat(int(dir.Fd()), id+".upload", 0) != nil || dir.Sync() != nil {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	s.mu.Lock()
	ready := database.TransactionFor(s.db)(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET state='ready' WHERE id=? AND state='promoting'`, id)
		return err
	})
	s.mu.Unlock()
	if ready != database.Committed {
		return nil, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	success = true
	return lease, nil
}

func uniqueIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	return result
}

// Change 把新引用准入、服务器事务及候选登记放在同一同步边界；禁止越过删除 claim。
func (s *Store) Change(ctx context.Context, owner string, ids []string, transact database.Transaction, operation func(*sql.Tx) error) database.Outcome {
	ids = uniqueIDs(ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		object, err := s.object(ctx, id)
		if err != nil || object.State != "ready" {
			return database.RolledBack
		}
	}
	s.uncertain[owner] = ids
	outcome := transact(ctx, func(tx *sql.Tx) error {
		if err := operation(tx); err != nil {
			return err
		}
		return refreshCandidates(ctx, tx, ids, s.now().UnixMilli())
	})
	if outcome != database.Uncertain {
		delete(s.uncertain, owner)
	}
	return outcome
}

func refreshCandidates(ctx context.Context, tx *sql.Tx, ids []string, now int64) error {
	for _, id := range ids {
		_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET candidate_at=CASE WHEN EXISTS(SELECT 1 FROM servers WHERE private_key_object_id=?) THEN NULL ELSE COALESCE(candidate_at,?) END WHERE id=?`, id, now, id)
		if err != nil {
			return err
		}
	}
	return nil
}

// Resolve 仅在服务器重载确认 DB 事实后调用；失败仍保护新旧对象。
func (s *Store) Resolve(ctx context.Context, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, exists := s.uncertain[owner]
	if !exists {
		return nil
	}
	if err := database.WithTx(ctx, s.db, func(tx *sql.Tx) error { return refreshCandidates(ctx, tx, ids, s.now().UnixMilli()) }); err != nil {
		return err
	}
	delete(s.uncertain, owner)
	return nil
}

// Reconcile 在数据库秘密和引用加载完成后运行；不扫描或删除没有账本的未知文件。
func (s *Store) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := openDirectory(s.path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return database.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		// 进程重启后旧短任务已不存在；引用优先，宽限从既有候选时刻继续计算。
		_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET
    state=CASE WHEN state='deleted' THEN state ELSE 'ready' END,
    candidate_at=CASE WHEN EXISTS(SELECT 1 FROM servers WHERE private_key_object_id=private_key_objects.id) THEN NULL ELSE COALESCE(candidate_at,?) END
    WHERE state<>'deleted'`, s.now().UnixMilli())
		return err
	})
}

type SweepResult struct {
	Checked, Deleted, Attempted int
	Paused                      bool
}

// Sweep 固定有界工作量；删除 claim 持久化后解锁 IO，新引用/lease 仍被状态拒绝。
func (s *Store) Sweep(ctx context.Context, logger *slog.Logger) (SweepResult, error) {
	result := SweepResult{}
	if !s.sweepMu.TryLock() {
		return result, nil
	}
	defer s.sweepMu.Unlock()
	started := time.Now()
	dir, err := openDirectory(s.path)
	if err != nil {
		result.Paused = true
		return result, err
	}
	defer dir.Close()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM private_key_objects WHERE candidate_at IS NOT NULL AND state IN ('ready','promoting','delete_claimed') AND candidate_at<=? ORDER BY COALESCE(last_delete_attempt_at,0),candidate_at,id LIMIT 100`, s.now().Add(-s.grace).UnixMilli())
	if err != nil {
		result.Paused = true
		return result, fault.Error("SERVER_UNAVAILABLE")
	}
	ids := make([]string, 0, 100)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		result.Paused = true
		return result, fault.Error("SERVER_UNAVAILABLE")
	}
	for _, id := range ids {
		if ctx.Err() != nil || time.Since(started) >= time.Second || result.Attempted >= 20 {
			break
		}
		result.Checked++
		s.mu.Lock()
		object, readErr := s.object(ctx, id)
		if readErr != nil {
			s.mu.Unlock()
			result.Paused = true
			return result, fault.Error("SERVER_UNAVAILABLE")
		}
		blocked := s.leases[id] > 0 || !object.Candidate.Valid || object.Candidate.Int64 > s.now().Add(-s.grace).UnixMilli() || object.State == "deleted"
		for _, protected := range s.uncertain {
			if slices.Contains(protected, id) {
				blocked = true
				break
			}
		}
		if blocked {
			s.mu.Unlock()
			continue
		}
		var references int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE private_key_object_id=?`, id).Scan(&references); err != nil {
			s.mu.Unlock()
			result.Paused = true
			return result, fault.Error("SERVER_UNAVAILABLE")
		}
		if references > 0 {
			s.mu.Unlock()
			continue
		}
		claimed := database.TransactionFor(s.db)(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET state='delete_claimed',delete_attempts=delete_attempts+1,last_delete_attempt_at=? WHERE id=?`, s.now().UnixMilli(), id)
			return err
		})
		s.mu.Unlock()
		if claimed != database.Committed {
			result.Paused = true
			return result, fault.Error("SERVER_UNAVAILABLE")
		}
		result.Attempted++
		// 在同一轮每次删除前重新验证卷可读，不能把不可读目录当对象不存在。
		check, checkErr := openDirectory(s.path)
		if checkErr != nil {
			result.Paused = true
			return result, checkErr
		}
		oldStat, _ := dir.Stat()
		newStat, _ := check.Stat()
		check.Close()
		if oldStat == nil || newStat == nil || !os.SameFile(oldStat, newStat) {
			result.Paused = true
			return result, fault.Error("PRIVATE_KEY_STORAGE_UNAVAILABLE")
		}
		deleteErr := s.remove(dir, object)
		category := ""
		state := "deleted"
		if deleteErr != nil {
			state = "ready"
			category = "filesystem_error"
			if deleteErr.Error() == "object_changed" {
				category = "object_changed"
			}
		}
		s.mu.Lock()
		recorded := database.TransactionFor(s.db)(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE private_key_objects SET state=?,last_delete_error=? WHERE id=?`, state, nullable(category), id)
			return err
		})
		s.mu.Unlock()
		if logger != nil && deleteErr != nil {
			logger.Warn("私钥对象回收失败", "object_id", id, "error_class", category, "attempts", object.Attempts+1, "attempt_time", s.now().UnixMilli())
		}
		if recorded != database.Committed {
			result.Paused = true
			return result, fault.Error("SERVER_UNAVAILABLE")
		}
		if deleteErr == nil {
			result.Deleted++
		}
	}
	return result, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
