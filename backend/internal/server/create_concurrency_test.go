package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"

	"github.com/stearsc/web-ssh/backend/internal/database"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"modernc.org/sqlite"
)

// 仅包装列表使用的 SQLite 连接；创建仍使用 setup 返回的真实事务连接。
// 两个同步点分别位于实际查询前、实际游标关闭后，不在生产代码中增加测试钩子。
type listGateConnector struct {
	driver.Connector
	queryEntered, queryProceed, rowsClosed chan struct{}
}

func (c listGateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &listGateConn{Conn: conn, gate: c}, nil
}

type listGateConn struct {
	driver.Conn
	gate listGateConnector
}

func (c *listGateConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "SELECT id FROM servers WHERE ") {
		return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	}
	close(c.gate.queryEntered)
	select {
	case <-c.gate.queryProceed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &listGateRows{Rows: rows, closed: c.gate.rowsClosed}, nil
}

type listGateRows struct {
	driver.Rows
	closed chan struct{}
	once   sync.Once
}

func (r *listGateRows) Close() error {
	err := r.Rows.Close()
	r.once.Do(func() { close(r.closed) })
	return err
}

func TestListIncludesCreateCommittedBeforePublicationCompletes(t *testing.T) {
	for _, outcome := range []database.Outcome{database.Committed, database.Uncertain} {
		t.Run(map[database.Outcome]string{database.Committed: "committed", database.Uncertain: "committed-uncertain"}[outcome], func(t *testing.T) {
			s, login, db := setup(t)
			ctx := t.Context()
			var sequence int
			var name, path string
			if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
				t.Fatal(err)
			}
			connector, err := sqlite.NewConnector(path)
			if err != nil {
				t.Fatal(err)
			}
			gate := listGateConnector{
				Connector:    connector,
				queryEntered: make(chan struct{}),
				queryProceed: make(chan struct{}),
				rowsClosed:   make(chan struct{}),
			}
			listDB := sql.OpenDB(gate)
			listDB.SetMaxOpenConns(1)
			t.Cleanup(func() { listDB.Close() })
			s.db = listDB

			committed, publish := make(chan database.Outcome, 1), make(chan struct{})
			var queryOnce, publishOnce sync.Once
			allowQuery := func() { queryOnce.Do(func() { close(gate.queryProceed) }) }
			allowPublication := func() { publishOnce.Do(func() { close(publish) }) }
			var workers sync.WaitGroup
			// 即使断言失败，也释放所有同步点并等工作协程退出，再关闭数据库。
			defer func() { allowQuery(); allowPublication(); workers.Wait() }()
			original := s.transact
			s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
				result := original(ctx, fn)
				committed <- result
				<-publish
				if result != database.Committed {
					return result
				}
				return outcome
			}
			type listResult struct {
				views []View
				err   error
			}
			listed := make(chan listResult, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				views, err := s.List(ctx, "demo", nil)
				listed <- listResult{views, err}
			}()
			// List 已完成 known 快照/重载，停在 SELECT 执行前，尚无新 State。
			<-gate.queryEntered
			type createResult struct {
				view View
				err  error
			}
			created := make(chan createResult, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				view, err := s.Create(ctx, login, input(22))
				created <- createResult{view, err}
			}()
			if result := <-committed; result != database.Committed {
				t.Fatalf("真实创建事务未提交: %v", result)
			}
			var id string
			if err := db.QueryRowContext(ctx, "SELECT id FROM servers").Scan(&id); err != nil {
				t.Fatalf("提交后未读到数据库记录: %v", err)
			}
			allowQuery()
			<-gate.rowsClosed
			// 此时 SELECT 已在提交后完成，Create 仍停在发布完成前。
			// 直接检查保护此窗口的不变量，避免测试通过与否依赖协程抢锁顺序。
			st, err := s.state(id)
			if err != nil {
				// 旧实现中此分支能确定性重现漏项：保持 Create 暂停，等待 List 返回。
				result := <-listed
				t.Fatalf("提交后 State 缺失，列表结果=%v，错误=%v", result.views, result.err)
			}
			if st.mu.TryLock() {
				st.mu.Unlock()
				t.Fatal("创建结果尚未确定就释放了服务器锁")
			}
			allowPublication()
			creation := <-created
			wantErr := error(nil)
			if outcome == database.Uncertain {
				wantErr = fault.Error("SERVER_UNAVAILABLE")
			}
			if creation.err != wantErr {
				t.Fatalf("创建结果: %v，预期: %v", creation.err, wantErr)
			}
			if outcome == database.Committed && creation.view.ID != id {
				t.Fatalf("创建返回 ID=%q，数据库 ID=%q", creation.view.ID, id)
			}
			result := <-listed
			if result.err != nil || len(result.views) != 1 || result.views[0].ID != id {
				t.Fatalf("列表遗漏已提交服务器 %q: %v, %v", id, result.views, result.err)
			}
		})
	}
}

func TestCreateRollbackRemovesRegisteredState(t *testing.T) {
	s, login, db := setup(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_create BEFORE INSERT ON servers BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	original := s.transact
	var pending *State
	s.transact = func(ctx context.Context, fn func(*sql.Tx) error) database.Outcome {
		s.mu.Lock()
		for _, st := range s.states {
			pending = st
		}
		s.mu.Unlock()
		return original(ctx, fn)
	}
	if _, err := s.Create(ctx, login, input(22)); err != fault.Error("STORAGE_ERROR") {
		t.Fatal(err)
	}
	if pending == nil {
		t.Fatal("事务前未登记待创建 State")
	}
	pending.mu.Lock()
	err := s.reload(ctx, pending)
	pending.mu.Unlock()
	if err != fault.Error("SERVER_NOT_FOUND") {
		t.Fatalf("回滚后持有旧指针的请求仍能读取对象: %v", err)
	}
	views, err := s.List(ctx, "", nil)
	if err != nil || len(views) != 0 || len(s.states) != 0 {
		t.Fatalf("回滚留下虚构服务器: %v, %v, states=%d", views, err, len(s.states))
	}
}
