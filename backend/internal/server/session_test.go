package server

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
)

func TestSessionReservationAtomicAndDeletion(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	confirm(t, s, login, v.ID, probeFor(t, s, login, v.ID))
	var wg sync.WaitGroup
	var successes atomic.Int64
	leases := make(chan *Lease, 32)
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, l, p, e := s.Reserve(t.Context(), login, v.ID, 2, func(target Target) error {
				if target.Host != "127.0.0.1" {
					t.Error(target)
				}
				return nil
			})
			if e == nil && p == nil {
				successes.Add(1)
				leases <- l
			} else if e != fault.Error("SESSION_LIMIT") {
				t.Error(e)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(leases)
	if successes.Load() != 2 {
		t.Fatal(successes.Load())
	}
	if err := s.Delete(t.Context(), login, v.ID); err != fault.Error("SERVER_HAS_ACTIVE_SESSIONS") {
		t.Fatal(err)
	}
	for l := range leases {
		wg.Add(2)
		go func() { defer wg.Done(); l.Release() }()
		go func() { defer wg.Done(); l.Release() }()
	}
	wg.Wait()
	if err := s.Delete(t.Context(), login, v.ID); err != nil {
		t.Fatal(err)
	}
}
func TestReservationInsertFailureAndUntrusted(t *testing.T) {
	s, login, _ := setup(t)
	remote := testutil.StartSSH(t)
	v := create(t, s, login, input(remote.Port()))
	_, l, p, e := s.Reserve(t.Context(), login, v.ID, 1, func(Target) error { t.Error("未信任时预占"); return nil })
	if e != nil || p == nil || l != nil {
		t.Fatal(l, p, e)
	}
	confirm(t, s, login, v.ID, p)
	_, _, _, e = s.Reserve(t.Context(), login, v.ID, 1, func(Target) error { return fault.Error("STORAGE_ERROR") })
	if e != fault.Error("STORAGE_ERROR") {
		t.Fatal(e)
	}
	if e := s.Delete(t.Context(), login, v.ID); e != nil {
		t.Fatal("失败泄漏名额", e)
	}
}

func TestReservedHostKeyMismatchReleasesBeforeProbe(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprint(edited), func(t *testing.T) {
			s, login, _ := setup(t)
			remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{})
			v := create(t, s, login, input(remote.Port()))
			confirm(t, s, login, v.ID, probeFor(t, s, login, v.ID))
			creation, lease, _, e := s.Reserve(t.Context(), login, v.ID, 1, func(Target) error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			defer lease.Release()
			// 预占之后切换真实 SSH 对端公钥，保持服务器地址与已保存信任不变。
			originalKey := remote.Key.PublicKey().Marshal()
			remote.RotateHostKey(testutil.Key(t))
			if edited {
				in := input(remote.Port())
				in.Username = "changed"
				if _, e = s.Update(t.Context(), login, v.ID, in); e != nil {
					t.Fatal(e)
				}
			}
			term, probe, e := creation.Open(t.Context(), login, time.Second, 80, 24, io.Discard)
			if term != nil {
				term.Close()
				t.Fatal("Host Key 不匹配仍建立终端")
			}
			if edited {
				if e != fault.Error("SERVER_CONFIG_CHANGED") || probe != nil {
					t.Fatal(probe, e)
				}
			} else {
				if e != nil || probe == nil {
					t.Fatal(probe, e)
				}
				st, _ := s.state(v.ID)
				st.mu.Lock()
				key := bytes.Clone(st.record.PublicKey)
				st.mu.Unlock()
				if !bytes.Equal(key, originalKey) {
					t.Fatal("未确认即改写信任")
				}
			}
			st, _ := s.state(v.ID)
			st.mu.Lock()
			occupied := st.occupied
			st.mu.Unlock()
			if occupied != 0 || remote.Shells.Load() != 0 {
				t.Fatal("探测仍占正式名额或创建了 Shell", occupied)
			}
			if e = s.Delete(t.Context(), login, v.ID); e != nil {
				t.Fatal(e)
			}
		})
	}
}
