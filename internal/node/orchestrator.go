// Package node is the integration layer that composes store (locks/ownership),
// cluster (sessions/registry/supervisor), and asynq (takeover) into the
// distributed account-takeover machinery. It is the only package allowed to
// import all of those; cluster/store/dispatch must NOT import node.
package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/store"
)

// SessionFactory builds and connects a Session for an account using a held lock.
// The real factory (in cmd) does GetDeviceStore + GetBoundProxy + NewWAConn +
// Connect + NewSession; tests inject a fake returning a fakeConn-backed Session.
type SessionFactory func(ctx context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error)

// Orchestrator wires together lock acquisition, session creation, account
// ownership claiming, registry registration, and fencing guard loops.
type Orchestrator struct {
	mgr           *store.Manager
	reg           *cluster.Registry
	sup           *cluster.Supervisor
	nodeID        string
	factory       SessionFactory
	guardInterval time.Duration
}

// NewOrchestrator creates an Orchestrator. guardInterval <= 0 defaults to 10s.
func NewOrchestrator(mgr *store.Manager, reg *cluster.Registry, sup *cluster.Supervisor, nodeID string, factory SessionFactory, guardInterval time.Duration) *Orchestrator {
	if guardInterval <= 0 {
		guardInterval = 10 * time.Second
	}
	return &Orchestrator{
		mgr:           mgr,
		reg:           reg,
		sup:           sup,
		nodeID:        nodeID,
		factory:       factory,
		guardInterval: guardInterval,
	}
}

// StartAccountWithLock acquires the account's advisory lock, builds+connects its
// session, claims ownership, registers it, and spawns a fencing guard. If
// another node already holds the lock it returns (nil, nil) — caller skips.
func (o *Orchestrator) StartAccountWithLock(ctx context.Context, jid string) (*cluster.Session, error) {
	lock, err := o.mgr.AcquireDeviceLock(ctx, jid)
	if errors.Is(err, store.ErrDeviceLocked) {
		return nil, nil // owned by a live peer — skip silently
	}
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", jid, err)
	}

	sess, err := o.factory(ctx, jid, lock)
	if err != nil {
		lock.Release(ctx)
		return nil, fmt.Errorf("start session %s: %w", jid, err)
	}

	if err := o.mgr.ClaimAccount(ctx, jid, o.nodeID); err != nil {
		sess.Close(ctx) // disconnects + releases lock
		return nil, fmt.Errorf("claim %s: %w", jid, err)
	}

	o.reg.Add(sess)
	o.sup.Go(func(gctx context.Context) error {
		o.guardSession(gctx, sess)
		return nil
	})
	return sess, nil
}

// guardSession fences against double-open: if the session loses advisory-lock
// ownership (transparent reconnect / backend death), remove it from the registry
// and close it so no further sends/receives occur on a lock we no longer hold.
func (o *Orchestrator) guardSession(ctx context.Context, sess *cluster.Session) {
	t := time.NewTicker(o.guardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !sess.Healthy(ctx) {
				o.reg.Remove(sess.JID())
				sess.Close(ctx)
				return
			}
		}
	}
}
