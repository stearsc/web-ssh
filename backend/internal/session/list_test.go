package session

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/server"
)

func TestListEmpty(t *testing.T) {
	m := &Manager{sessions: make(map[string]*Session)}
	for _, filter := range []string{"", strings.Repeat("a", 32)} {
		items := m.List(filter)
		data, err := json.Marshal(items)
		if err != nil || string(data) != "[]" {
			t.Fatalf("empty list: %s, %v", data, err)
		}
	}
}

func TestListStatesFilterAndMillisecondOrder(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	m := &Manager{sessions: make(map[string]*Session), now: func() time.Time { return now }}
	serverA, serverB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	// IDs deliberately conflict with sub-millisecond order and insertion order.
	fixtures := []struct {
		id, serverID, status string
		delta                time.Duration
	}{
		{"04", serverA, "CLOSING", time.Millisecond},
		{"02", serverB, "CONNECTED", 100 * time.Microsecond},
		{"05", serverA, "CLOSED", -time.Second},
		{"01", serverA, "CONNECTING", 900 * time.Microsecond},
		{"03", serverA, "DISCONNECTED", -time.Millisecond},
	}
	for _, f := range fixtures {
		id := strings.Repeat("0", 30) + f.id
		m.sessions[id] = &Session{manager: m, id: id, target: server.Target{ID: f.serverID, Name: "snapshot", Host: "example.test", Port: 22, Username: "tester"}, status: f.status, created: now.Add(f.delta), disconnected: now.Add(-time.Second), expires: now.Add(1500 * time.Millisecond), cols: 80, rows: 24, generation: 7}
	}
	for _, tc := range []struct {
		name, filter string
		ids          []string
	}{
		{"all states", "", []string{"03", "01", "02", "04"}},
		{"server A", serverA, []string{"03", "01", "04"}},
		{"server B", serverB, []string{"02"}},
		{"unknown server", strings.Repeat("c", 32), []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for round := 0; round < 30; round++ {
				items := m.List(tc.filter)
				got := make([]string, 0, len(items))
				for _, v := range items {
					got = append(got, v.ID[30:])
					if v.Status == "CLOSED" || v.Name != "snapshot" || v.Generation != 7 || v.Cols != 80 || v.Rows != 24 {
						t.Fatalf("unexpected view: %+v", v)
					}
					if v.Status == "DISCONNECTED" {
						if v.RemainingSeconds == nil || *v.RemainingSeconds != 2 || v.OfflineExpiresAt == nil {
							t.Fatalf("offline fields: %+v", v)
						}
					} else if v.RemainingSeconds != nil || v.OfflineExpiresAt != nil {
						t.Fatalf("online expiry: %+v", v)
					}
					stamp, err := time.Parse("2006-01-02T15:04:05.000Z", v.CreatedAt)
					if err != nil || stamp.UnixMilli() != m.sessions[v.ID].created.UnixMilli() {
						t.Fatalf("timestamp: %s %v", v.CreatedAt, err)
					}
				}
				if !reflect.DeepEqual(got, tc.ids) {
					t.Fatalf("IDs %v, want %v", got, tc.ids)
				}
			}
		})
	}
	// List neither expires a Session nor aliases nullable view fields.
	offline := m.sessions[strings.Repeat("0", 30)+"03"]
	offline.expires = now.Add(-time.Second)
	first := m.List(serverA)
	if first[0].RemainingSeconds == nil || *first[0].RemainingSeconds != 0 {
		t.Fatal("expired DISCONNECTED row was removed")
	}
	*first[0].RemainingSeconds = 99
	*first[0].DisconnectedAt = "changed"
	*first[0].OfflineExpiresAt = "changed"
	again := m.List(serverA)
	if *again[0].RemainingSeconds != 0 || *again[0].DisconnectedAt == "changed" || *again[0].OfflineExpiresAt == "changed" {
		t.Fatal("views share mutable fields")
	}
}

func TestListReleasesIndexLockBeforeRowSnapshot(t *testing.T) {
	now := time.Now()
	copying := make(chan struct{})
	resume := make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(resume) }) }
	defer unblock()
	m := &Manager{sessions: make(map[string]*Session), now: func() time.Time {
		// viewLocked calls now while copying a DISCONNECTED row under Session.mu.
		close(copying)
		<-resume
		return now
	}}
	s := &Session{manager: m, id: strings.Repeat("a", 32), status: "DISCONNECTED", created: now, expires: now.Add(time.Second)}
	m.sessions[s.id] = s
	result := make(chan []View, 1)
	go func() { result <- m.List("") }()
	select {
	case <-copying:
	case <-time.After(time.Second):
		t.Fatal("List did not reach row snapshot")
	}
	if !m.mu.TryLock() {
		t.Fatal("List holds index lock while copying a Session")
	}
	// Membership changes after the reference snapshot must not invalidate the old
	// reference or add a new row to this in-flight result.
	delete(m.sessions, s.id)
	next := &Session{manager: m, id: strings.Repeat("b", 32), status: "CONNECTING", created: now}
	m.sessions[next.id] = next
	m.mu.Unlock()
	unblock()
	select {
	case items := <-result:
		if len(items) != 1 || items[0].ID != s.id {
			t.Fatalf("not an index snapshot: %+v", items)
		}
	case <-time.After(time.Second):
		t.Fatal("List did not finish")
	}
	if items := m.List(""); len(items) != 1 || items[0].ID != next.id {
		t.Fatalf("next query missed new membership: %+v", items)
	}
}

func TestListConcurrentLifecycle(t *testing.T) {
	m, login, serverID, remote := setup(t)
	stop := make(chan struct{})
	started := make(chan struct{}, 4)
	failures := make(chan error, 4)
	var readers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		readers.Go(func() {
			started <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				items := m.List(serverID)
				for i, v := range items {
					if v.ServerID != serverID || v.Status == "CLOSED" || v.Cols != 80 || v.Rows != 24 {
						failures <- fmt.Errorf("invalid row: %+v", v)
						return
					}
					if (v.Status == "DISCONNECTED") != (v.OfflineExpiresAt != nil && v.RemainingSeconds != nil) {
						failures <- fmt.Errorf("torn state/expiry: %+v", v)
						return
					}
					if i > 0 && (items[i-1].CreatedAt > v.CreatedAt || (items[i-1].CreatedAt == v.CreatedAt && items[i-1].ID >= v.ID)) {
						failures <- fmt.Errorf("unordered or duplicate rows: %+v", items)
						return
					}
				}
				runtime.Gosched()
			}
		})
	}
	t.Cleanup(func() {
		close(stop)
		readers.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
	})
	for worker := 0; worker < 4; worker++ {
		<-started
	}
	for round := 0; round < 12; round++ {
		s := createSession(t, m, login, serverID)
		b := bind(t, m, s, login)
		items := m.List(serverID)
		if len(items) != 1 || items[0].ID != s.id || items[0].Status != "CONNECTED" || items[0].Generation != b.Generation {
			t.Fatalf("bound row: %+v", items)
		}
		s.Detach(b)
		items = m.List(serverID)
		if len(items) != 1 || items[0].Status != "DISCONNECTED" || items[0].Generation != b.Generation {
			t.Fatalf("detached row: %+v", items)
		}
		s.Close("user_closed")
		select {
		case <-s.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("close blocked during List")
		}
		if len(m.List("")) != 0 {
			t.Fatal("closed session still listed")
		}
	}
	if remote.Shells.Load() != 12 {
		t.Fatalf("List changed SSH creation count: %d", remote.Shells.Load())
	}
	m.mu.Lock()
	remaining := len(m.sessions)
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("unreclaimed index entries: %d", remaining)
	}
}

func TestListUsesTargetSnapshotAndIgnoresStoredHistory(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	original := s.View()
	port := original.Port
	_, err := m.servers.Update(t.Context(), login, id, server.Input{Name: "edited", Host: "localhost", Port: &port, Username: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	items := m.List(id)
	if len(items) != 1 || items[0].Name != original.Name || items[0].Host != original.Host || items[0].Username != original.Username {
		t.Fatalf("lost target snapshot: %+v", items)
	}
	s.Close("user_closed")
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("close timeout")
	}
	// Simulate stale persisted active metadata after the live Session was reclaimed.
	// Activity must come only from Manager, even when the stored status is active.
	if _, err = m.db.ExecContext(t.Context(), `UPDATE sessions SET status='DISCONNECTED' WHERE id=?`, s.id); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = m.db.QueryRowContext(t.Context(), `SELECT status FROM sessions WHERE id=?`, s.id).Scan(&status); err != nil || status != "DISCONNECTED" {
		t.Fatal(status, err)
	}
	if items = m.List(""); len(items) != 0 {
		t.Fatalf("revived stored history: %+v", items)
	}
}
