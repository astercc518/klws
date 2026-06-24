package cluster

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type SupervisorOpts struct {
	StopIntake      func()        // stop asynq intake (waits for in-flight handlers)
	CloseStore      func()        // close store.Manager (pools + sqlDB)
	Flush           func()        // flush logger
	Limit           int           // max concurrent account starts
	ShutdownTimeout time.Duration // bound for the disconnect/close phase
}

type managedLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Supervisor struct {
	reg   *Registry
	opts  SupervisorOpts
	mu    sync.Mutex
	loops []*managedLoop
	done  bool
}

func NewSupervisor(reg *Registry, opts SupervisorOpts) *Supervisor {
	if opts.Limit <= 0 {
		opts.Limit = 32
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}
	return &Supervisor{reg: reg, opts: opts}
}

// Go runs fn in a managed goroutine. fn receives a context cancelled at
// Shutdown and is expected to return when ctx is done (fn owns its own ticker/
// pacing). Shutdown cancels all such goroutines and waits for them to exit
// before disconnecting sessions.
//
// Go is a no-op if Shutdown has already been called; a loop registered after
// shutdown would be orphaned (its context never cancelled by Shutdown).
func (s *Supervisor) Go(fn func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	ml := &managedLoop{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		cancel() // release the context resource
		return
	}
	s.loops = append(s.loops, ml)
	s.mu.Unlock()
	go func() {
		defer close(ml.done)
		_ = fn(ctx)
	}()
}

// StartAccounts brings up accounts with bounded concurrency (SetLimit). Each
// successful Session is registered; a start error is returned in aggregate but
// does not register a half-open session.
func (s *Supervisor) StartAccounts(ctx context.Context, jids []string, start func(ctx context.Context, jid string) (*Session, error)) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.Limit)
	for _, jid := range jids {
		jid := jid
		g.Go(func() error {
			sess, err := start(gctx, jid)
			if err != nil {
				return err
			}
			s.reg.Add(sess)
			return nil
		})
	}
	return g.Wait()
}

// Shutdown performs the fixed graceful order and is idempotent:
//  1. stop asynq intake (waits for in-flight handlers)
//  2. cancel + await background loops
//  3. disconnect all sessions (conn.Disconnect then lock.Release each)
//  4. close store (pools + sqlDB)
//  5. flush logger
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	loops := s.loops
	s.mu.Unlock()

	// 1. intake
	if s.opts.StopIntake != nil {
		s.opts.StopIntake()
	}
	// 2. loops
	for _, ml := range loops {
		ml.cancel()
	}
	for _, ml := range loops {
		<-ml.done
	}
	// 3. sessions (bounded by ShutdownTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	s.reg.CloseAll(ctx)
	cancel()
	// 4. store
	if s.opts.CloseStore != nil {
		s.opts.CloseStore()
	}
	// 5. flush
	if s.opts.Flush != nil {
		s.opts.Flush()
	}
}
