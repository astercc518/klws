// internal/store/lock.go
package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDeviceLocked = errors.New("store: device owned by another process")

// DeviceLock 持有一条专用连接以维持 session 级 advisory lock。
type DeviceLock struct {
	conn *pgxpool.Conn
	key  int64
}

func advisoryKey(accountJID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(accountJID))
	return int64(h.Sum64())
}

// acquirePGLock 非阻塞抢占账号独占权;失败返回 ErrDeviceLocked。
// 供 pgOwnership.Acquire 调用；外部代码请使用 Manager.AcquireDeviceLock。
func (m *Manager) acquirePGLock(ctx context.Context, accountJID string) (*DeviceLock, error) {
	key := advisoryKey(accountJID)

	conn, err := m.lockPool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for lock: %w", err)
	}

	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("try advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, ErrDeviceLocked
	}
	return &DeviceLock{conn: conn, key: key}, nil
}

// Healthy re-validates advisory-lock OWNERSHIP, not merely connection liveness.
// It queries pg_locks ON the pinned connection for a granted advisory lock held
// by THIS backend (pg_backend_pid()). Each DeviceLock pins its own dedicated
// connection and acquires exactly one advisory lock, so the presence of any
// granted advisory lock on this backend confirms we still own this account's
// lock. If the connection was transparently replaced, pg_backend_pid() is a
// different backend with no such lock -> false; if the connection is dead, the
// query errors -> false. This is the fencing primitive guardSession relies on to
// prevent double-open.
func (l *DeviceLock) Healthy(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	var ok bool
	err := l.conn.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE locktype = 'advisory'
    AND pid = pg_backend_pid()
    AND granted
)`).Scan(&ok)
	return err == nil && ok
}

// BackendPID returns the PostgreSQL backend PID of the pinned connection that
// holds this advisory lock. Used by tests to inject chaos via pg_terminate_backend.
// Returns 0 if the lock or its connection is nil.
func (l *DeviceLock) BackendPID() uint32 {
	if l == nil || l.conn == nil {
		return 0
	}
	return l.conn.Conn().PgConn().PID()
}

// KillConnForTest forcibly closes the underlying TCP connection, simulating
// abrupt process death. PostgreSQL automatically releases any session-level
// advisory locks held on that backend when the connection drops. The pgxpool
// entry is also returned (as dead) so the pool can recycle it. This method is
// intended ONLY for integration tests — do not call it in production code.
func (l *DeviceLock) KillConnForTest(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	// Close the raw *pgconn.PgConn; this drops the TCP socket immediately.
	// pgxpool.Conn.Release() is called afterward so the pool can mark the slot
	// as available (it will see the dead connection and discard it).
	_ = l.conn.Conn().PgConn().Close(ctx)
	l.conn.Release()
	l.conn = nil
}

// Release 释放锁并归还连接。进程崩溃时连接断开,Postgres 自动回收锁。
func (l *DeviceLock) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", l.key)
	l.conn.Release()
	l.conn = nil
}
