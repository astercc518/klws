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

// AcquireDeviceLock 非阻塞抢占账号独占权;失败返回 ErrDeviceLocked。
func (m *Manager) AcquireDeviceLock(ctx context.Context, accountJID string) (*DeviceLock, error) {
	key := advisoryKey(accountJID)

	conn, err := m.bizPool.Acquire(ctx)
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

// Healthy 探测持锁连接是否仍存活(即锁是否仍归我持有)。
func (l *DeviceLock) Healthy(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	return l.conn.Ping(ctx) == nil
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
